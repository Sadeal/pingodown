package ping_pong

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"sync"
	"time"

	"github.com/qdm12/golibs/logging"
	"github.com/qdm12/pingodown/internal/state"
)

// Magic bytes to identify our ping packets (to distinguish from game traffic)
// Source engine packets usually start with 0xFF. We use a custom header.
var PingHeader = []byte{0xFE, 0xFE, 0xFE, 0xFE, 0x50, 0x49, 0x4E, 0x47} // "....PING"

type PingPong interface {
	Start(ctx context.Context, proxyConn *net.UDPConn)
	IsPong(clientAddr *net.UDPAddr, data []byte) bool
}

type pingPong struct {
	state          state.State
	logger         logging.Logger
	pingInterval   time.Duration
	sentPings      map[string]time.Time // Key: ClientIP, Value: Time sent
	sentPingsMutex sync.RWMutex
}

func NewPingPong(s state.State, logger logging.Logger) PingPong {
	return &pingPong{
		state:        s,
		logger:       logger,
		pingInterval: 1 * time.Second,
		sentPings:    make(map[string]time.Time),
	}
}

func (p *pingPong) Start(ctx context.Context, proxyConn *net.UDPConn) {
	p.logger.Info("Starting Ping-Pong service (Interval: %s)", p.pingInterval)
	ticker := time.NewTicker(p.pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sendPings(proxyConn)
		}
	}
}

func (p *pingPong) sendPings(conn *net.UDPConn) {
	clients := p.state.GetClientAddresses()
	if len(clients) == 0 {
		return
	}

	// Prepare Ping Packet: Header + Timestamp (Nano)
	// Note: We only strictly need the header if we assume immediate reply,
	// but sending timestamp allows stateless RTT calculation if client echoes fully.
	// For simple reflection, we'll track send time locally.
	payload := make([]byte, len(PingHeader))
	copy(payload, PingHeader)

	for _, client := range clients {
		// Update send time
		p.sentPingsMutex.Lock()
		p.sentPings[client.IP.String()] = time.Now()
		p.sentPingsMutex.Unlock()

		_, err := conn.WriteToUDP(payload, &client)
		if err != nil {
			p.logger.Warn("Failed to send ping to %s: %v", client.String(), err)
		}
	}
}

// IsPong checks if the packet is a response to our ping.
// If yes, it calculates RTT, updates state, and returns true (consume packet).
// If no, returns false (forward packet to server).
func (p *pingPong) IsPong(clientAddr *net.UDPAddr, data []byte) bool {
	// Check size and header
	if len(data) < len(PingHeader) {
		return false
	}

	// Check if data starts with PingHeader
	if !bytes.Equal(data[:len(PingHeader)], PingHeader) {
		return false
	}

	// It is a Pong!
	clientIP := clientAddr.IP.String()
	p.sentPingsMutex.RLock()
	sentTime, ok := p.sentPings[clientIP]
	p.sentPingsMutex.RUnlock()

	if !ok {
		// We didn't track a ping for this client, ignore or treat as valid but unknown RTT
		return true
	}

	rtt := time.Since(sentTime).Milliseconds()
	if rtt < 0 {
		rtt = 0
	}

	// Update State
	// Note: Current logic is "UpdateClientPing".
	if err := p.state.UpdateClientPing(clientIP, rtt); err != nil {
		p.logger.Error("Failed to update ping for %s: %v", clientIP, err)
	}

	p.logger.Info("RTT for %s: %d ms", clientIP, rtt)
	return true
}
