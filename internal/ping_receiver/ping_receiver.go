package ping_receiver

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/qdm12/golibs/logging"
)

type PingData struct {
	ClientIP string
	PingMS   int64
}

type PingReceiver interface {
	Start(ctx context.Context) error
	GetDataChan() <-chan PingData
	AddIP(ip string)
	Close() error
}

type pingReceiver struct {
	dataChan    chan PingData
	logger      logging.Logger
	targets     map[string]time.Time // IP -> Last Sent Time
	targetsLock sync.RWMutex
	conn        *net.IPConn
	closed      bool
}

func NewPingReceiver(logger logging.Logger) (PingReceiver, error) {
	return &pingReceiver{
		dataChan: make(chan PingData, 100),
		logger:   logger,
		targets:  make(map[string]time.Time),
	}, nil
}

func (pr *pingReceiver) GetDataChan() <-chan PingData {
	return pr.dataChan
}

func (pr *pingReceiver) AddIP(ip string) {
	pr.targetsLock.Lock()
	defer pr.targetsLock.Unlock()
	if _, exists := pr.targets[ip]; !exists {
		pr.targets[ip] = time.Now()
		pr.logger.Info("Started monitoring ping for %s", ip)
	}
}

func (pr *pingReceiver) Start(ctx context.Context) error {
	// Listen for ICMP packets (requires root)
	conn, err := net.ListenIP("ip4:icmp", &net.IPAddr{IP: net.ParseIP("0.0.0.0")})
	if err != nil {
		return fmt.Errorf("failed to listen for ICMP: %w (ensure you are running as root)", err)
	}
	pr.conn = conn
	pr.logger.Info("Pinger started (ICMP)")

	// Routine to read replies
	go func() {
		buf := make([]byte, 1500)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				pr.conn.SetReadDeadline(time.Now().Add(1 * time.Second))
				n, addr, err := pr.conn.ReadFromIP(buf)
				if err != nil {
					continue
				}
				
				// Process ICMP Echo Reply (Type 0)
				if n > 0 && buf[0] == 0 { 
					// Simple RTT calculation based on time since we last pinged this specific IP
					// Note: A more robust impl would match Sequence IDs, but for a game proxy this is sufficient
					pr.targetsLock.RLock()
					sentTime, ok := pr.targets[addr.IP.String()]
					pr.targetsLock.RUnlock()

					if ok {
						rtt := time.Since(sentTime).Milliseconds()
						select {
						case pr.dataChan <- PingData{ClientIP: addr.IP.String(), PingMS: rtt}:
						default:
						}
					}
				}
			}
		}
	}()

	// Routine to send pings
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		seq := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pr.targetsLock.Lock()
				for ipStr := range pr.targets {
					ip := net.ParseIP(ipStr)
					if ip == nil {
						continue
					}
					
					// Update send time
					pr.targets[ipStr] = time.Now()

					// Construct ICMP Echo Request
					// Type(8), Code(0), Checksum(0), ID, Seq, Payload
					msg := make([]byte, 8+8) // Header + 8 bytes payload
					msg[0] = 8 // Echo Request
					msg[1] = 0
					msg[2] = 0 // Checksum placeholder
					msg[3] = 0
					
					msg[4] = 0 // ID
					msg[5] = 1
					msg[6] = byte(seq >> 8)
					msg[7] = byte(seq)
					
					// Calculate Checksum
					check := checkSum(msg)
					msg[2] = byte(check >> 8)
					msg[3] = byte(check)

					pr.conn.WriteToIP(msg, &net.IPAddr{IP: ip})
				}
				pr.targetsLock.Unlock()
				seq++
			}
		}
	}()

	return nil
}

func (pr *pingReceiver) Close() error {
	pr.closed = true
	close(pr.dataChan)
	if pr.conn != nil {
		return pr.conn.Close()
	}
	return nil
}

func checkSum(msg []byte) uint16 {
	sum := 0
	for n := 0; n < len(msg)-1; n += 2 {
		sum += int(msg[n])<<8 + int(msg[n+1])
	}
	if len(msg)%2 != 0 {
		sum += int(msg[len(msg)-1]) << 8
	}
	sum = (sum >> 16) + (sum & 0xffff)
	sum += (sum >> 16)
	return uint16(^sum)
}
