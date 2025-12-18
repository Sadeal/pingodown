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
				p.logger.Info("Updated ping for client %s: %d ms", pingData.ClientIP, pingData.PingMS)
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

				// Create connection immediately
				conn, err = connection.NewConnection(p.serverAddress, packet.clientAddress, p.bufferSize)
				if err != nil {
					p.logger.Error("Failed to create connection: %v", err)
					continue
				}

				// Save connection
				conn = p.state.SetConnection(conn)
				
				// Start server-to-client forwarding
				go conn.ForwardServerToClient(ctx, p.proxyConn, p.logger)
				
				// Start goroutine to monitor and apply ping delays continuously
				go p.monitorAndApplyPing(ctx, clientIP, conn)
			}

			// Forward packet to server with delay
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

// monitorAndApplyPing continuously monitors ping data and applies/updates delay
func (p *proxy) monitorAndApplyPing(ctx context.Context, clientIP string, conn connection.Connection) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	lastAppliedPing := int64(-1) // Track last ping to avoid re-applying same value

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Try to get current ping data
			actualPing, err := p.state.GetClientPing(clientIP)
			if err == nil {
				// Got ping data
				if actualPing != lastAppliedPing {
					// Ping changed or first time - apply it
					conn.SetPing(p.minPingMS, actualPing)
					additionalPing := p.minPingMS - actualPing
					if additionalPing < 0 {
						additionalPing = 0
					}
					p.logger.Info("Applied ping for client %s: min=%d ms, actual=%d ms, additional=%d ms",
						clientIP, p.minPingMS, actualPing, additionalPing)
					lastAppliedPing = actualPing
				}
			}
			// If no ping data yet, continue waiting
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
