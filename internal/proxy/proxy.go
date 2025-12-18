package proxy

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/qdm12/golibs/logging"
	"github.com/qdm12/pingodown/internal/connection"
	"github.com/qdm12/pingodown/internal/ping_receiver"
	"github.com/qdm12/pingodown/internal/state"
)

type Proxy interface {
	Run(ctx context.Context) error
}

type proxy struct {
	bufferSize      int
	proxyConn       *net.UDPConn
	serverAddress   *net.UDPAddr
	state           state.State
	logger          logging.Logger
	minPingMS       int64
	pingReceiver    ping_receiver.PingReceiver
}

func NewProxy(listenAddress, pingAddress, serverAddress string, logger logging.Logger, minPing time.Duration) (Proxy, error) {
	s := state.NewState()
	p := &proxy{
		bufferSize: 65535,
		state:      s,
		logger:     logger,
		minPingMS:  minPing.Milliseconds(),
	}

	// Create main proxy connection (for clients)
	var err error
	proxyAddress, err := net.ResolveUDPAddr("udp", listenAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve proxy address: %w", err)
	}

	p.proxyConn, err = net.ListenUDP("udp", proxyAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on proxy address: %w", err)
	}

	// Resolve server address (external IP:PORT)
	p.serverAddress, err = net.ResolveUDPAddr("udp", serverAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve server address: %w", err)
	}

	// Create ping receiver
	p.pingReceiver, err = ping_receiver.NewPingReceiver(pingAddress, logger)
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
	p.logger.Info("Running proxy to %s on %s", p.serverAddress, p.proxyConn.LocalAddr())

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
			conn, err := p.state.GetConnection(packet.clientAddress)
			if err != nil {
				p.logger.Info("New client %s connecting", packet.clientAddress)
				
				// Get client IP from address
				clientIP := packet.clientAddress.IP.String()

				// Wait for ping data (up to 5 seconds)
				clientActualPing := p.minPingMS
				waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				for i := 0; i < 50; i++ { // Try 50 times with 100ms intervals
					if actualPing, err := p.state.GetClientPing(clientIP); err == nil {
						clientActualPing = actualPing
						p.logger.Info("Got ping data for client %s after %dms: %d ms", clientIP, i*100, clientActualPing)
						break
					}
					select {
					case <-waitCtx.Done():
						p.logger.Warn("Timeout waiting for ping data from client %s, using minimum ping %d ms", clientIP, p.minPingMS)
						break
					case <-time.After(100 * time.Millisecond):
					}
				}
				cancel()

				// Create connection
				conn, err = connection.NewConnection(p.serverAddress, packet.clientAddress, p.bufferSize)
				if err != nil {
					p.logger.Error("Failed to create connection: %v", err)
					continue
				}

				// Set ping with dynamic calculation
				conn.SetPing(p.minPingMS, clientActualPing)
				p.logger.Info("Set ping for client %s: min=%d ms, actual=%d ms, additional=%d ms", 
					clientIP, p.minPingMS, clientActualPing, p.minPingMS-clientActualPing)

				conn = p.state.SetConnection(conn)
				go conn.ForwardServerToClient(ctx, p.proxyConn, p.logger)
			}

			// Forward packet to server with delay (capture conn in closure properly)
			func(c connection.Connection, data []byte) {
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
