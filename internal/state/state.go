package state

import (
	"fmt"
	"net"
	"sync"

	"github.com/qdm12/pingodown/internal/connection"
)

type State interface {
	GetClientAddresses() (clientAddresses []net.UDPAddr)
	SetConnection(conn connection.Connection) connection.Connection
	GetConnection(clientAddress *net.UDPAddr) (conn connection.Connection, err error)
	UpdateClientPing(clientIP string, pingMS int64) error
	GetClientPing(clientIP string) (int64, error)
}

type state struct {
	// Key is the client address (IP:PORT)
	connections map[string]connection.Connection
	// Key is the client IP address only
	clientPings      map[string]int64
	connectionsMutex sync.RWMutex
}

func NewState() State {
	return &state{
		connections: make(map[string]connection.Connection),
		clientPings: make(map[string]int64),
	}
}

func (s *state) GetClientAddresses() (clientAddresses []net.UDPAddr) {
	s.connectionsMutex.RLock()
	defer s.connectionsMutex.RUnlock()

	for _, conn := range s.connections {
		clientAddresses = append(clientAddresses, conn.GetClientUDPAddress())
	}

	return clientAddresses
}

func (s *state) GetConnection(clientAddress *net.UDPAddr) (conn connection.Connection, err error) {
	s.connectionsMutex.RLock()
	defer s.connectionsMutex.RUnlock()

	key := clientAddress.String()
	conn, ok := s.connections[key]
	if !ok {
		return nil, fmt.Errorf("no connection found for client address %s", key)
	}

	return conn, nil
}

func (s *state) SetConnection(conn connection.Connection) connection.Connection {
	s.connectionsMutex.Lock()
	defer s.connectionsMutex.Unlock()

	addr := conn.GetClientUDPAddress()
	key := (&addr).String()
	if existing, ok := s.connections[key]; ok {
		return existing
	}

	s.connections[key] = conn
	return conn
}

func (s *state) UpdateClientPing(clientIP string, pingMS int64) error {
	s.connectionsMutex.Lock()
	defer s.connectionsMutex.Unlock()

	if pingMS < 0 {
		return fmt.Errorf("ping cannot be negative: %d", pingMS)
	}

	s.clientPings[clientIP] = pingMS
	return nil
}

func (s *state) GetClientPing(clientIP string) (int64, error) {
	s.connectionsMutex.RLock()
	defer s.connectionsMutex.RUnlock()

	pingMS, ok := s.clientPings[clientIP]
	if !ok {
		return 0, fmt.Errorf("no ping data found for client IP %s", clientIP)
	}

	return pingMS, nil
}
