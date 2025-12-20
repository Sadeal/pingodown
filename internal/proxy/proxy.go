package proxy

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/qdm12/golibs/logging"
	"github.com/qdm12/pingodown/internal/connection"
	"github.com/qdm12/pingodown/internal/ping_pong"
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
	pingPong      ping_pong.PingPong
	localIPs      []string
}

func NewProxy(listenAddress, serverAddress string, logger logging.Logger, minPing time.Duration) (Proxy, error) {
	s := state.NewState()

	// Resolve local IPs for loop protection
	localIPs, err := getLocalIPs()
	if err != nil {
		return nil, fmt.Errorf("failed to get local IPs: %w", err)
	}

	p := &proxy{
		bufferSize: 65535,
		state:      s,
		logger:     logger,
		minPingMS:  minPing.Milliseconds(),
		localIPs:   localIPs,
	}

	// Create ping pong service
	p.pingPong = ping_pong.NewPingPong(s, logger)

	// Create main proxy connection (for clients)
	proxyAddress, err := net.ResolveUDPAddr("udp", listenAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve proxy address: %w", err)
	}

	p.proxyConn, err = net.ListenUDP("udp", proxyAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on proxy address: %w", err)
	}

	// Resolve server address
	// CRITICAL: This must be the Docker Container IP or Localhost Mapped port, NOT the public IP.
	p.serverAddress, err = net.ResolveUDPAddr("udp", serverAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve server address: %w", err)
	}

	return p, nil
}

type clientPacket struct {
	clientAddress *net.UDPAddr
	data          []byte
}

func (p *proxy) Run(ctx context.Context) error {
	p.logger.Info("Running proxy -> %s (Listening on %s)", p.serverAddress, p.proxyConn.LocalAddr())
	p.logger.Info("Ignoring traffic from local IPs: %v", p.localIPs)

	packets := make(chan clientPacket, 100)

	// Start Ping Pong Service
	go p.pingPong.Start(ctx, p.proxyConn)

	// Goroutine to read packets
	go func() {
		if err := p.readFromClients(p.proxyConn, packets, p.bufferSize); err != nil {
			p.logger.Error("Error reading from clients: %v", err)
		}
	}()

	// Main packet processing loop
	for {
		select {
		case packet := <-packets:
			// 1. SELF-IP CHECK
			if p.isLocalIP(packet.clientAddress.IP.String()) {
				// Silently drop packets from ourselves to avoid loops/spam
				continue
			}

			// 2. CHECK IF PONG
			if p.pingPong.IsPong(packet.clientAddress, packet.data) {
				// It was a pong, state is updated, discard packet
				// Now check if we need to update delays for existing connection
				p.updateConnectionDelay(packet.clientAddress)
				continue
			}

			// 3. HANDLE GAME TRAFFIC
			conn, err := p.state.GetConnection(packet.clientAddress)
			if err != nil {
				// New Client
				p.logger.Info("New client %s connecting", packet.clientAddress)
				
				// Connect to Server
				conn, err = connection.NewConnection(p.serverAddress, packet.clientAddress, p.bufferSize)
				if err != nil {
					p.logger.Error("Failed to create connection: %v", err)
					continue
				}
				conn = p.state.SetConnection(conn)

				// Start forwarding Server -> Client
				go conn.ForwardServerToClient(ctx, p.proxyConn, p.logger)
			}

			// Apply delay logic if ping is known
			p.updateConnectionDelay(packet.clientAddress)

			// Forward Client -> Server
			go func(c connection.Connection, data []byte) {
				if err := c.WriteToServerWithDelay(ctx, data); err != nil {
					p.logger.Error("Error writing to server: %v", err)
				}
			}(conn, packet.data)

		case <-ctx.Done():
			p.logger.Info("Context canceled, closing proxy")
			return p.proxyConn.Close()
		}
	}
}

func (p *proxy) updateConnectionDelay(clientAddr *net.UDPAddr) {
	clientIP := clientAddr.IP.String()
	actualPing, err := p.state.GetClientPing(clientIP)
	if err != nil {
		// No ping data yet, no delay
		return
	}

	conn, err := p.state.GetConnection(clientAddr)
	if err != nil {
		return
	}

	// Calculate and set delay
	// "If ping received... add delay" logic
	conn.SetPing(p.minPingMS, actualPing)
}

func (p *proxy) readFromClients(proxy *net.UDPConn, packets chan<- clientPacket, bufferSize int) error {
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

// Helpers

func (p *proxy) isLocalIP(ip string) bool {
	for _, local := range p.localIPs {
		if local == ip {
			return true
		}
	}
	return false
}

func getLocalIPs() ([]string, error) {
	var ips []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, i := range ifaces {
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil {
				ips = append(ips, ip.String())
			}
		}
	}
	return ips, nil
}
