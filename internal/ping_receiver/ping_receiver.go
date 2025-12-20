package ping_receiver

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/qdm12/golibs/logging"
)

type PingData struct {
	ClientIP string
	PingMS   int64
}

type PingReceiver interface {
	Start(ctx context.Context) error
	GetDataChan() <-chan PingData
	Close() error
}

type pingReceiver struct {
	conn      *net.UDPConn
	dataChan  chan PingData
	logger    logging.Logger
	bufSize   int
	closeOnce sync.Once
	closed    bool
}

func NewPingReceiver(listenAddress string, logger logging.Logger) (PingReceiver, error) {
	addr, err := net.ResolveUDPAddr("udp", listenAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve ping address: %w", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on ping address: %w", err)
	}

	return &pingReceiver{
		conn:     conn,
		dataChan: make(chan PingData, 100),
		logger:   logger,
		bufSize:  4096,
	}, nil
}

func (pr *pingReceiver) GetDataChan() <-chan PingData {
	return pr.dataChan
}

func (pr *pingReceiver) Start(ctx context.Context) error {
	pr.logger.Info("Ping receiver listening on %s", pr.conn.LocalAddr())

	go func() {
		buffer := make([]byte, pr.bufSize)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				bytesRead, remoteAddr, err := pr.conn.ReadFromUDP(buffer)
				if err != nil {
					pr.logger.Error("Ping receiver read error: %v", err)
					continue
				}

				data := string(buffer[:bytesRead])
				pingData, err := ParsePingData(data)
				if err != nil {
					pr.logger.Error("Failed to parse ping data from %s: %v", remoteAddr, err)
					continue
				}

				select {
				case pr.dataChan <- pingData:
					pr.logger.Info("Received ping from %s = %d ms", pingData.ClientIP, pingData.PingMS)
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return nil
}

func (pr *pingReceiver) Close() error {
	var err error
	pr.closeOnce.Do(func() {
		pr.closed = true
		close(pr.dataChan)
		err = pr.conn.Close()
	})
	return err
}

func ParsePingData(data string) (PingData, error) {
	parts := strings.Split(strings.TrimSpace(data), "|")
	if len(parts) != 2 {
		return PingData{}, fmt.Errorf("invalid format, expected 'ip|ping', got '%s'", data)
	}

	clientIP := parts[0]
	pingStr := parts[1]

	// Validate IP
	if net.ParseIP(clientIP) == nil {
		return PingData{}, fmt.Errorf("invalid IP address '%s'", clientIP)
	}

	// Parse ping
	pingMS, err := strconv.ParseInt(pingStr, 10, 64)
	if err != nil {
		return PingData{}, fmt.Errorf("invalid ping value '%s': %w", pingStr, err)
	}
	if pingMS < 0 {
		return PingData{}, fmt.Errorf("ping cannot be negative: %d", pingMS)
	}

	return PingData{
		ClientIP: clientIP,
		PingMS:   pingMS,
	}, nil
}
