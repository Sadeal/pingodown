package proxy

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/qdm12/golibs/logging"
	"github.com/qdm12/pingodown/internal/connection"
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
}

func NewProxy(listenAddress, serverAddress string, logger logging.Logger, minPing time.Duration) (Proxy, error) {
	s := state.NewState()

	proxyAddr, err := net.ResolveUDPAddr("udp", listenAddress)
	if err != nil {
		return nil, fmt.Errorf("resolve proxy addr: %w", err)
	}

	proxyConn, err := net.ListenUDP("udp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("listen proxy: %w", err)
	}

	targetAddr, err := net.ResolveUDPAddr("udp", serverAddress)
	if err != nil {
		return nil, fmt.Errorf("resolve server addr: %w", err)
	}

	return &proxy{
		bufferSize:    65535,
		proxyConn:     proxyConn,
		serverAddress: targetAddr,
		state:         s,
		logger:        logger,
		minPingMS:     minPing.Milliseconds(),
	}, nil
}

type clientPacket struct {
	clientAddress *net.UDPAddr
	data          []byte
	receivedAt    time.Time
}

func (p *proxy) Run(ctx context.Context) error {
	p.logger.Info("Proxy running. Forwarding %s <-> %s", p.proxyConn.LocalAddr(), p.serverAddress)
	
	packets := make(chan clientPacket, 100)

	// Read Loop
	go func() {
		buf := make([]byte, p.bufferSize)
		for {
			n, addr, err := p.proxyConn.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() == nil {
					p.logger.Error("Read error: %v", err)
				}
				return
			}
			
			// Self-traffic protection
			if addr.IP.Equal(p.serverAddress.IP) || addr.IP.IsLoopback() {
				continue 
			}

			data := make([]byte, n)
			copy(data, buf[:n])
			packets <- clientPacket{clientAddress: addr, data: data, receivedAt: time.Now()}
		}
	}()

	// Processing Loop
	for {
		select {
		case packet := <-packets:
			clientIP := packet.clientAddress.IP.String()
			conn, err := p.state.GetConnection(packet.clientAddress)

			// 1. New Client Handling
			if err != nil {
				p.logger.Info("New client detected: %s", packet.clientAddress)
				conn, err = connection.NewConnection(p.serverAddress, packet.clientAddress, p.bufferSize)
				if err != nil {
					p.logger.Error("Failed to create connection: %v", err)
					continue
				}
				conn = p.state.SetConnection(conn)
				
				// Start async forwarder (Server -> Client)
				go conn.ForwardServerToClient(ctx, p.proxyConn, p.logger)
			}

			// 2. Passive Ping Calculation (The "Ping-Pong" Strategy)
			// If we sent a packet to this client recently, this incoming packet 
			// gives us a rough RTT estimate: (Now - LastSendTime)
			lastSent := conn.GetLastSentToClient()
			if !lastSent.IsZero() {
				rtt := packet.receivedAt.Sub(lastSent).Milliseconds()
				
				// Sanity check: Ping < 1s (ignore idle timeouts/connects)
				if rtt > 0 && rtt < 1000 {
					// Update State (You might want to add averaging in state.go)
					p.state.UpdateClientPing(clientIP, rtt)
					
					// Apply Delay Logic
					p.applyDelay(conn, rtt, clientIP)
				}
			}

			// 3. Forward Client -> Server
			go func(c connection.Connection, d []byte) {
				if err := c.WriteToServerWithDelay(ctx, d); err != nil {
					p.logger.Error("Write to server failed: %v", err)
				}
			}(conn, packet.data)

		case <-ctx.Done():
			p.proxyConn.Close()
			return nil
		}
	}
}

func (p *proxy) applyDelay(conn connection.Connection, actualPing int64, clientIP string) {
	neededDelay := p.minPingMS - actualPing
	if neededDelay <= 0 {
		// Ping is high enough, no artificial delay needed
		conn.SetPing(p.minPingMS, actualPing) // Updates internal stats, sets delay to 0
		return
	}
	
	// Check if delay changed significantly to avoid spam
	prevDelay := conn.GetOutboundDelay()
	newDelayDuration := time.Duration(neededDelay/2) * time.Millisecond
	
	if (newDelayDuration - prevDelay).Abs() > 5*time.Millisecond {
		p.logger.Info("Client %s: Ping %d ms (Target %d ms). Adding %d ms delay.", 
			clientIP, actualPing, p.minPingMS, neededDelay)
		conn.SetPing(p.minPingMS, actualPing)
	}
}
