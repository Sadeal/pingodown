package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/qdm12/golibs/logging"
	"github.com/qdm12/pingodown/internal/proxy"
	"github.com/qdm12/pingodown/internal/net_utils"
)

func main() {
	logger, err := logging.NewLogger(logging.ConsoleEncoding, logging.InfoLevel, 0)
	if err != nil {
		panic(err)
	}

	// Parse command line arguments
	if len(os.Args) != 3 {
		logger.Error("Usage: ./pingodown SERVER_PORT PING_MS")
		logger.Error("Example: ./pingodown 9000 100")
		os.Exit(1)
	}

	// Parse SERVER_PORT
	serverPort, err := strconv.Atoi(os.Args[1])
	if err != nil {
		logger.Error("SERVER_PORT must be a number, got: %s", os.Args[1])
		os.Exit(1)
	}
	if serverPort < 1 || serverPort > 65535 {
		logger.Error("SERVER_PORT must be between 1 and 65535, got: %d", serverPort)
		os.Exit(1)
	}

	// Parse PING (in milliseconds)
	pingMS, err := strconv.Atoi(os.Args[2])
	if err != nil {
		logger.Error("PING_MS must be a number, got: %s", os.Args[2])
		os.Exit(1)
	}
	if pingMS < 0 {
		logger.Error("PING_MS must be >= 0, got: %d", pingMS)
		os.Exit(1)
	}

	// Calculate ports
	listenPort := serverPort + 1
	pingPort := serverPort + 2

	// Validate ports don't conflict
	if listenPort > 65535 || pingPort > 65535 {
		logger.Error("Calculated ports exceed 65535. SERVER_PORT=%d is too high", serverPort)
		os.Exit(1)
	}

	// Get server external IP
	serverIP, err := net_utils.GetServerExternalIP()
	if err != nil {
		logger.Error("Failed to get server external IP: %v", err)
		os.Exit(1)
	}

	// Build addresses
	listenAddress := fmt.Sprintf(":%d", listenPort)
	pingAddress := fmt.Sprintf(":%d", pingPort)
	serverAddress := fmt.Sprintf("%s:%d", serverIP, serverPort)
	minPing := time.Duration(pingMS) * time.Millisecond

	logger.Info("Starting pingodown")
	logger.Info("Server port: %d", serverPort)
	logger.Info("Listen port: %d (clients connect here)", listenPort)
	logger.Info("Ping port: %d (ping data here)", pingPort)
	logger.Info("Server address: %s", serverAddress)
	logger.Info("Minimum ping: %d ms", pingMS)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p, err := proxy.NewProxy(listenAddress, pingAddress, serverAddress, logger, minPing)
	if err != nil {
		logger.Error("Failed to create proxy: %v", err)
		os.Exit(1)
	}

	if err := p.Run(ctx); err != nil {
		logger.Error("Proxy error: %v", err)
		os.Exit(1)
	}
}
