package proxy

import (
	"context"
	"fmt"
	"net"
	"sync"
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
	connections     map[string]connection.Connection
	connMutex       sync.Mutex
	wg              sync.WaitGroup
}

func NewProxy(listenAddress, pingAddress, serverAddress string, logger logging.Logger, minPing time.Duration) (Proxy, error) {
	s := state.NewState()
	p := &proxy{
		bufferSize:  65535,
		state:       s,
		logger:      logger,
		minPingMS:   minPing.Milliseconds(),
		connections: make(map[string]connection.Connection),
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
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		if err := readFromClients(p.proxyConn, packets, p.bufferSize); err != nil {
			p.logger.Error("Error reading from clients: %v", err)
		}
	}()

	// Goroutine to handle ping data updates
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case pingData := <-p.pingReceiver.GetDataChan():
				if err := p.state.UpdateClientPing(pingData.ClientIP, pingData.PingMS); err != nil {
					p.logger.Error("Failed to update client ping: %v", err)
				}
				p.logger.Info("Received ping %d ms from %s", pingData.PingMS, pingData.ClientIP)
				
				// If we have a connection for this client, apply the ping immediately
				p.connMutex.Lock()
				if conn, exists := p.connections[pingData.ClientIP]; exists {
					additionalPing := p.minPingMS - pingData.PingMS
					if additionalPing < 0 {
						additionalPing = 0
					}
					
					if additionalPing > 0 {
						conn.SetPing(p.minPingMS, pingData.PingMS)
						p.logger.Info("Saw ping %d, ping is below %d ms, adding %d ms delay", pingData.PingMS, p.minPingMS, additionalPing)
					} else {
						p.logger.Info("Saw ping %d, ping is not below %d ms, no delay added", pingData.PingMS, p.minPingMS)
					}
				}
				p.connMutex.Unlock()
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
				p.connMutex.Lock()
				p.connections[clientIP] = conn
				p.connMutex.Unlock()
				
				// Check if we already have ping data for this client and apply it IMMEDIATELY
				if actualPing, err := p.state.GetClientPing(clientIP); err == nil {
					additionalPing := p.minPingMS - actualPing
					if additionalPing < 0 {
						additionalPing = 0
					}
					
					if additionalPing > 0 {
						conn.SetPing(p.minPingMS, actualPing)
						p.logger.Info("Saw ping %d, ping is below %d ms, adding %d ms delay", actualPing, p.minPingMS, additionalPing)
					} else {
						p.logger.Info("Saw ping %d, ping is not below %d ms, no delay added", actualPing, p.minPingMS)
					}
				} else {
					p.logger.Info("No ping data yet for client %s, will apply when ping arrives", clientIP)
				}
				
				// Start server-to-client forwarding
				p.wg.Add(1)
				go func(c connection.Connection) {
					defer p.wg.Done()
					c.ForwardServerToClient(ctx, p.proxyConn, p.logger)
				}(conn)
			}

			// Forward packet to server with delay
			func(c connection.Connection, data []byte) {
				if err := c.WriteToServerWithDelay(ctx, data); err != nil {
					p.logger.Error("Error writing to server: %v", err)
				}
			}(conn, packet.data)

		case <-ctx.Done():
			p.logger.Info("Context canceled, closing connections and proxy")
			p.closeAllConnections()
			p.pingReceiver.Close()
			p.proxyConn.Close()
			p.wg.Wait()
			return nil
		}
	}
}

// closeAllConnections closes all active client connections
func (p *proxy) closeAllConnections() {
	p.connMutex.Lock()
	defer p.connMutex.Unlock()

	for clientIP, conn := range p.connections {
		p.logger.Info("Closing connection for client %s", clientIP)
		if err := conn.Close(); err != nil {
			p.logger.Error("Error closing connection for %s: %v", clientIP, err)
		}
	}
	p.connections = make(map[string]connection.Connection)
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
