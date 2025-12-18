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
	Close() error
}

// Information maintained for each client-server connection
type connection struct {
	clientAddress  net.UDPAddr
	server         *net.UDPConn
	bufferSize     int
	inboundDelay   time.Duration
	outboundDelay  time.Duration
	logger         logging.Logger
	sync.RWMutex
}

// Generate a new connection by opening a UDP connection to the server
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

// NewConnectionWithLogger creates a connection with logger for debugging
func NewConnectionWithLogger(serverAddress, clientAddress *net.UDPAddr, bufferSize int, logger logging.Logger) (Connection, error) {
	server, err := net.DialUDP("udp", nil, serverAddress)
	if err != nil {
		return nil, err
	}

	return &connection{
		clientAddress: *clientAddress,
		server:        server,
		bufferSize:    bufferSize,
		logger:        logger,
	}, nil
}

func (c *connection) close() error {
	return c.server.Close()
}

func (c *connection) Close() error {
	return c.close()
}

func (c *connection) getInboundDelay() time.Duration {
	c.RLock()
	defer c.RUnlock()
	return c.inboundDelay
}

func (c *connection) getOutboundDelay() time.Duration {
	c.RLock()
	defer c.RUnlock()
	return c.outboundDelay
}

func (c *connection) SetPing(minimumPingMS int64, clientActualPingMS int64) {
	c.Lock()
	defer c.Unlock()

	// Calculate additional ping needed
	additionalPingMS := minimumPingMS - clientActualPingMS
	if additionalPingMS < 0 {
		additionalPingMS = 0
	}

	// Convert to duration and split equally
	additionalPing := time.Duration(additionalPingMS) * time.Millisecond
	c.inboundDelay = additionalPing / 2
	c.outboundDelay = additionalPing / 2

	if c.logger != nil {
		c.logger.Info("SetPing called for client %s: minimum=%d ms, actual=%d ms, additional delay=%d ms, inbound=%d ms, outbound=%d ms",
			c.clientAddress, minimumPingMS, clientActualPingMS, additionalPingMS, c.inboundDelay.Milliseconds(), c.outboundDelay.Milliseconds())
	}
}

func (c *connection) GetClientUDPAddress() net.UDPAddr {
	return c.clientAddress
}

func (c *connection) ForwardServerToClient(ctx context.Context, proxy *net.UDPConn, logger logging.Logger) {
	defer func() {
		logger.Info("Closing connection with client %s", c.clientAddress)
		if err := c.close(); err != nil {
			logger.Error("Error closing connection: %v", err)
		}
	}()

	packets := make(chan []byte) // unbuffered

	go func() {
		if err := c.readFromServer(packets); err != nil {
			logger.Error("Error reading from server: %v", err)
		}
	}()

	for {
		select {
		case packet := <-packets:
			go func(data []byte) {
				err := writeToClientWithDelay(ctx, c.getOutboundDelay(), proxy, &c.clientAddress, data)
				if err != nil {
					logger.Error("Error: %v", err)
				}
			}(packet)
		case <-ctx.Done():
			logger.Info("Context canceled, closing connection")
			c.close()
			return
		}
	}
}

func (c *connection) readFromServer(packets chan<- []byte) error {
	buffer := make([]byte, c.bufferSize)
	for {
		bytesRead, err := c.server.Read(buffer)
		if err != nil {
			return err
		}

		data := make([]byte, bytesRead)
		copy(data, buffer[:bytesRead])
		packets <- data
	}
}

func (c *connection) WriteToServerWithDelay(ctx context.Context, data []byte) error {
	return writeToServerWithDelay(ctx, c.getInboundDelay(), c.server, data)
}

func writeToServerWithDelay(ctx context.Context, delay time.Duration, server *net.UDPConn, data []byte) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	timer := time.AfterFunc(delay, func() {
		err = writeToServer(server, data)
		cancel() // Done when write is done or context canceled externally
	})

	timer.Stop()
	return err
}

func writeToClientWithDelay(ctx context.Context, delay time.Duration, proxy *net.UDPConn, client *net.UDPAddr, data []byte) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	timer := time.AfterFunc(delay, func() {
		err = writeToClient(proxy, client, data)
		cancel() // Done when write is done or context is canceled externally
	})

	timer.Stop()
	return err
}

func writeToClient(proxy *net.UDPConn, client *net.UDPAddr, data []byte) error {
	bytesWritten, err := proxy.WriteToUDP(data, client)
	if err != nil {
		return err
	} else if bytesWritten != len(data) {
		return fmt.Errorf("read %d bytes from server and wrote %d bytes to client", len(data), bytesWritten)
	}
	return nil
}

func writeToServer(server *net.UDPConn, data []byte) error {
	n, err := server.Write(data)
	if err != nil {
		return err
	} else if n != len(data) {
		return fmt.Errorf("wrote %d bytes but data was %d bytes", n, len(data))
	}
	return nil
}
