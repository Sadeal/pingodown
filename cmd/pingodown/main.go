package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/qdm12/golibs/logging"
	"github.com/qdm12/pingodown/internal/firewall"
	"github.com/qdm12/pingodown/internal/proxy"
)

func main() {
	logger, err := logging.NewLogger(logging.ConsoleEncoding, logging.InfoLevel, 0)
	if err != nil {
		panic(err)
	}

	// Usage: ./pingodown SERVER_PORT PING_MS [TARGET_IP]
	if len(os.Args) < 3 {
		logger.Error("Usage: ./pingodown SERVER_PORT PING_MS [TARGET_IP]")
		logger.Error("Example: ./pingodown 27055 100 172.17.0.1")
		os.Exit(1)
	}

	serverPort, err := strconv.Atoi(os.Args[1])
	if err != nil || serverPort < 1 || serverPort > 65535 {
		logger.Error("Invalid SERVER_PORT: %s", os.Args[1])
		os.Exit(1)
	}

	pingMS, err := strconv.Atoi(os.Args[2])
	if err != nil || pingMS < 0 {
		logger.Error("Invalid PING_MS: %s", os.Args[2])
		os.Exit(1)
	}

	// Target IP (Docker Container IP or Localhost)
	// Default to 172.17.0.1 (Docker Gateway) as requested to bypass loop
	targetIP := "172.17.0.1" 
	if len(os.Args) > 3 {
		targetIP = os.Args[3]
	}

	// Configuration
	listenPort := serverPort + 1 // We listen here (e.g., 27056)
	listenAddress := fmt.Sprintf(":%d", listenPort)
	serverAddress := fmt.Sprintf("%s:%d", targetIP, serverPort) // Forward to here (e.g., 172.17.0.1:27055)
	minPing := time.Duration(pingMS) * time.Millisecond

	logger.Info("Starting pingodown")
	logger.Info("Mode: Native Ping-Pong (No Plugin)")
	logger.Info("Listening on: %s (Clients connect here)", listenAddress)
	logger.Info("Forwarding to: %s (Docker/Server)", serverAddress)
	logger.Info("Minimum Ping: %d ms", pingMS)

	// Setup Firewall Redirect (27055 -> 27056)
	fw := firewall.NewFirewall(logger)
	if err := fw.AddRedirect(serverPort, listenPort); err != nil {
		logger.Error("Failed to add firewall redirect: %v", err)
		logger.Info("Continuing without redirect (ensure traffic reaches port %d)", listenPort)
	}

	// Signal Handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start Proxy
	p, err := proxy.NewProxy(listenAddress, serverAddress, logger, minPing)
	if err != nil {
		logger.Error("Failed to create proxy: %v", err)
		fw.RemoveRedirect(serverPort, listenPort)
		os.Exit(1)
	}

	errChan := make(chan error, 1)
	go func() {
		errChan <- p.Run(ctx)
	}()

	select {
	case <-sigChan:
		logger.Info("Received shutdown signal")
		cancel()
	case err := <-errChan:
		if err != nil {
			logger.Error("Proxy error: %v", err)
		}
	}

	// Cleanup
	logger.Info("Cleaning up firewall rules...")
	fw.RemoveRedirect(serverPort, listenPort)
	logger.Info("Shutdown complete")
}
