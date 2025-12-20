package connection

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/qdm12/golibs/logging"
)

type Connection interface {
	GetClientUDPAddress() net.UDPAddr
	ForwardServerToClient(ctx context.Context, proxy *net.UDPConn, logger logging.Logger)
	WriteToServerWithDelay(ctx context.Context, data []byte) error
	SetPing(minimumPingMS int64, clientActualPingMS int64)
	GetOutboundDelay() time.Duration
	GetLastSentToClient() time.Time
	Close() error
}

type connection struct {
	clientAddress    net.UDPAddr
	server           *net.UDPConn
	bufferSize       int
	inboundDelay     time.Duration
	outboundDelay    time.Duration
	lastSentToClient time.Time // New field
	logger           logging.Logger
	sync.RWMutex
}

func NewConnection(serverAddress, clientAddress *net.UDPAddr, bufferSize int) (Connection, error) {
	server, err := net.DialUDP("udp", nil, serverAddress)
	if err != nil {
		return nil, err
	}
	return &connection{
		clientAddress: *clientAddress,
		server:        server,
		bufferSize:    bufferSize,
	}, nil
}

func (c *connection) GetLastSentToClient() time.Time {
	c.RLock()
	defer c.RUnlock()
	return c.lastSentToClient
}

func (c *connection) GetOutboundDelay() time.Duration {
	c.RLock()
	defer c.RUnlock()
	return c.outboundDelay
}

func (c *connection) SetPing(minimumPingMS int64, clientActualPingMS int64) {
	c.Lock()
	defer c.Unlock()
	
	additionalPingMS := minimumPingMS - clientActualPingMS
	if additionalPingMS < 0 {
		additionalPingMS = 0
	}
	
	totalDelay := time.Duration(additionalPingMS) * time.Millisecond
	// Split delay 50/50 for RTT symmetry
	c.inboundDelay = totalDelay / 2
	c.outboundDelay = totalDelay / 2
}

func (c *connection) ForwardServerToClient(ctx context.Context, proxy *net.UDPConn, logger logging.Logger) {
	defer c.Close()
	buffer := make([]byte, c.bufferSize)

	for {
		// Read from Server (Docker)
		n, err := c.server.Read(buffer)
		if err != nil {
			return
		}
		
		data := make([]byte, n)
		copy(data, buffer[:n])

		// Apply Delay then Write to Client
		go func(d []byte) {
			delay := c.GetOutboundDelay()
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return
				}
			}

			_, err := proxy.WriteToUDP(d, &c.clientAddress)
			if err != nil {
				logger.Error("Write to client error: %v", err)
				return
			}
			
			// Record the timestamp AFTER writing
			c.Lock()
			c.lastSentToClient = time.Now()
			c.Unlock()
		}(data)
	}
}

// ... rest of WriteToServerWithDelay/Close methods similar to original ...
func (c *connection) WriteToServerWithDelay(ctx context.Context, data []byte) error {
	c.RLock()
	delay := c.inboundDelay
	c.RUnlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	_, err := c.server.Write(data)
	return err
}

func (c *connection) Close() error {
	return c.server.Close()
}

func (c *connection) GetClientUDPAddress() net.UDPAddr {
	return c.clientAddress
}
