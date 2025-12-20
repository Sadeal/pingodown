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

	// Create main proxy connection (binds to ServerPort)
	var err error
	proxyAddr, err := net.ResolveUDPAddr("udp", listenAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve listen address: %w", err)
	}

	p.proxyConn, err = net.ListenUDP("udp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on proxy address: %w", err)
	}

	// Resolve server address (Docker Internal IP:PORT)
	p.serverAddress, err = net.ResolveUDPAddr("udp", serverAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve server address: %w", err)
	}

	// Create ping receiver (Active Pinger)
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
	p.logger.Info("Running proxy. Listening on %s -> Forwarding to %s", p.proxyConn.LocalAddr(), p.serverAddress)
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
				if err := p.state.UpdateClientPing(pingData.ClientIP, pingData.PingMS); err != nil {
					p.logger.Error("Failed to update client ping: %v", err)
					continue
				}

				// Try to get connection and apply ping if it exists
				if conn, err := p.state.GetConnection(&net.UDPAddr{IP: net.ParseIP(pingData.ClientIP)}); err == nil {
					additionalPing := p.minPingMS - pingData.PingMS
					if additionalPing < 0 {
						additionalPing = 0
					}

					if additionalPing > 0 {
						conn.SetPing(p.minPingMS, pingData.PingMS)
						// p.logger.Info("Adjusted delay for %s: ping %d ms -> add %d ms", pingData.ClientIP, pingData.PingMS, additionalPing)
					}
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
			// Check if packet is from ourselves (Docker reflection or loopback)
			if net_utils.IsLocalIP(packet.clientAddress.IP.String()) {
				// Silently ignore packets from self/docker gateway to prevent loops
				continue
			}

			conn, err := p.state.GetConnection(packet.clientAddress)
			if err != nil {
				p.logger.Info("New client %s connecting", packet.clientAddress)
				clientIP := packet.clientAddress.IP.String()

				// Register IP for active pinging
				p.pingReceiver.AddIP(clientIP)

				// Create connection immediately
				conn, err = connection.NewConnection(p.serverAddress, packet.clientAddress, p.bufferSize)
				if err != nil {
					p.logger.Error("Failed to create connection: %v", err)
					continue
				}

				conn = p.state.SetConnection(conn)

				// Start server-to-client forwarding
				go conn.ForwardServerToClient(ctx, p.proxyConn, p.logger)
			}

			// Forward packet to Docker server
			go func(c connection.Connection, data []byte) {
				if err := c.WriteToServerWithDelay(ctx, data); err != nil {
					p.logger.Error("Error writing to server: %v", err)
				}
			}(conn, packet.data)

		case <-ctx.Done():
			p.logger.Info("Context canceled, closing proxy connection")
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
