package main

import (
	"context"
	"fmt"
	"net"
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

	if len(os.Args) != 3 {
		logger.Error("Usage: ./pingodown SERVER_PORT PING_MS")
		logger.Error("Example: ./pingodown 27015 50")
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

	// We listen on serverPort + 1 (redirect target)
	listenPort := serverPort + 1
	listenAddress := fmt.Sprintf(":%d", listenPort)

	// TARGET: Docker Container directly (usually 172.17.0.1 for default bridge)
	// This bypasses Docker's own userland proxy and iptables NAT loop
	dockerGateway := "172.17.0.1"
	serverAddress := fmt.Sprintf("%s:%d", dockerGateway, serverPort)
	minPing := time.Duration(pingMS) * time.Millisecond

	logger.Info("Starting pingodown")
	logger.Info("Target Server (Docker): %s", serverAddress)
	logger.Info("Proxy Listening on: %s", listenAddress)
	logger.Info("Target Minimum Ping: %d ms", pingMS)

	// Setup Firewall Redirect (External -> Proxy)
	fw := firewall.NewFirewall(logger)
	if err := fw.AddRedirect(serverPort, listenPort); err != nil {
		logger.Error("Failed to add firewall redirect: %v. Run as root/sudo.", err)
		// We continue; user might have setup rules manually
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize Proxy
	p, err := proxy.NewProxy(listenAddress, serverAddress, logger, minPing)
	if err != nil {
		logger.Error("Failed to create proxy: %v", err)
		fw.RemoveRedirect(serverPort, listenPort)
		os.Exit(1)
	}

	// Cleanup handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := p.Run(ctx); err != nil {
			logger.Error("Proxy stopped with error: %v", err)
			cancel()
		}
	}()

	<-sigChan
	logger.Info("Shutting down...")
	cancel()
	
	if err := fw.RemoveRedirect(serverPort, listenPort); err != nil {
		logger.Error("Failed to remove firewall rules: %v", err)
	}
	logger.Info("Shutdown complete")
}
