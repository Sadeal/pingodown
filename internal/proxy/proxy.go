package proxy

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/qdm12/golibs/logging"
	"github.com/qdm12/pingodown/internal/connection"
	"github.com/qdm12/pingodown/internal/net_utils"
	"github.com/qdm12/pingodown/internal/ping_receiver"
	"github.com/qdm12/pingodown/internal/state"
)

type Proxy interface {
	Run(ctx context.Context) error
}

type proxy struct {
	bufferSize    int
	proxyConn     *net.UDPConn
	serverAddress *net.UDPAddr
	state         state.State
	logger        logging.Logger
	minPingMS     int64
	pingReceiver  ping_receiver.PingReceiver
}

func NewProxy(listenAddress, serverAddress string, logger logging.Logger, minPing time.Duration) (Proxy, error) {
	s := state.NewState()
	p := &proxy{
		bufferSize: 65535,
		state:      s,
		logger:     logger,
		minPingMS:  minPing.Milliseconds(),
	}

	// Create main proxy connection (ListenPort)
	var err error
	proxyAddr, err := net.ResolveUDPAddr("udp", listenAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve listen address: %w", err)
	}

	p.proxyConn, err = net.ListenUDP("udp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on proxy address: %w", err)
	}

	// Resolve target server address (Docker IP or Localhost)
	p.serverAddress, err = net.ResolveUDPAddr("udp", serverAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve server address: %w", err)
	}

	// Create active ping receiver
	p.pingReceiver, err = ping_receiver.NewPingReceiver(logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create ping receiver: %w", err)
	}

	return p, nil
}

type clientPacket struct {
	clientAddress *net.UDPAddr
	data          []byte
}

func (p *proxy) Run(ctx context.Context) error {
	p.logger.Info("Proxy started: Listening on %s -> Forwarding to %s", p.proxyConn.LocalAddr(), p.serverAddress)
	packets := make(chan clientPacket, 100)

	// Start ping receiver
	if err := p.pingReceiver.Start(ctx); err != nil {
		return fmt.Errorf("failed to start ping receiver: %w", err)
	}

	// Goroutine to read game packets from clients
	go func() {
		if err := readFromClients(p.proxyConn, packets, p.bufferSize); err != nil {
			p.logger.Error("Error reading from clients: %v", err)
		}
	}()

	// Goroutine to handle ping data updates
	go func() {
		for {
			select {
			case pingData := <-p.pingReceiver.GetDataChan():
				// Update state
				if err := p.state.UpdateClientPing(pingData.ClientIP, pingData.PingMS); err != nil {
					p.logger.Error("Failed to update client ping: %v", err)
					continue
				}

				// Apply delay to existing connection
				if conn, err := p.state.GetConnection(&net.UDPAddr{IP: net.ParseIP(pingData.ClientIP)}); err == nil {
					conn.SetPing(p.minPingMS, pingData.PingMS)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Main packet processing loop
	for {
		select {
		case packet := <-packets:
			// 1. FILTER SELF/LOCAL TRAFFIC
			// Prevents infinite loops and processing packets from Docker gateway/Localhost
			if net_utils.IsLocalIP(packet.clientAddress.IP.String()) {
				continue
			}

			conn, err := p.state.GetConnection(packet.clientAddress)
			if err != nil {
				p.logger.Info("New client detected: %s", packet.clientAddress)
				
				// 2. Start Active Pinging for this client
				p.pingReceiver.AddIP(packet.clientAddress.IP.String())

				// Create connection
				conn, err = connection.NewConnection(p.serverAddress, packet.clientAddress, p.bufferSize)
				if err != nil {
					p.logger.Error("Failed to create connection: %v", err)
					continue
				}
				conn = p.state.SetConnection(conn)

				// Start backward forwarding (Server -> Client)
				go conn.ForwardServerToClient(ctx, p.proxyConn, p.logger)
			}

			// Forward packet (Client -> Server) with delay logic embedded in connection
			go func(c connection.Connection, data []byte) {
				if err := c.WriteToServerWithDelay(ctx, data); err != nil {
					p.logger.Error("Error writing to server: %v", err)
				}
			}(conn, packet.data)

		case <-ctx.Done():
			p.logger.Info("Stopping proxy...")
			p.pingReceiver.Close()
			return p.proxyConn.Close()
		}
	}
}

func readFromClients(proxy *net.UDPConn, packets chan<- clientPacket, bufferSize int) error {
	buffer := make([]byte, bufferSize)
	for {
		bytesRead, clientAddress, err := proxy.ReadFromUDP(buffer)
		if err != nil {
			return fmt.Errorf("error reading from UDP: %w", err)
		}

		data := make([]byte, bytesRead)
		copy(data, buffer[:bytesRead])

		packets <- clientPacket{
			clientAddress: clientAddress,
			data:          data,
		}
	}
}
