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
	"github.com/qdm12/pingodown/internal/net_utils"
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
		os.Exit(1)
	}

	serverPort, _ := strconv.Atoi(os.Args[1])
	pingMS, _ := strconv.Atoi(os.Args[2])
	
	// Default Target: Docker Gateway
	targetIP := "172.17.0.1"
	if len(os.Args) > 3 {
		targetIP = os.Args[3]
	}

	// Configuration
	listenPort := serverPort + 1
	listenAddress := fmt.Sprintf(":%d", listenPort)
	serverAddress := fmt.Sprintf("%s:%d", targetIP, serverPort)
	minPing := time.Duration(pingMS) * time.Millisecond

	// Get External IP for SNAT
	externalIP, err := net_utils.GetServerExternalIP()
	if err != nil {
		logger.Error("Failed to get external IP: %v", err)
		os.Exit(1)
	}

	logger.Info("--- PingoDown Config ---")
	logger.Info("External IP:   %s", externalIP)
	logger.Info("Listen Port:   %d (Real)", listenPort)
	logger.Info("Fake Port:     %d (Client sees this)", serverPort)
	logger.Info("Target Server: %s", serverAddress)
	logger.Info("------------------------")

	fw := firewall.NewFirewall(logger)
	
	// Apply Firewall Rules (Incoming REDIRECT + Outgoing SNAT)
	if err := fw.AddRedirect(serverPort, listenPort, externalIP); err != nil {
		logger.Error("Firewall setup failed: %v", err)
		// We proceed, but connection will likely fail without SNAT
	}

	// Signal Handler
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start Proxy
	p, err := proxy.NewProxy(listenAddress, serverAddress, logger, minPing)
	if err != nil {
		logger.Error("Proxy creation failed: %v", err)
		fw.RemoveRedirect(serverPort, listenPort, externalIP)
		os.Exit(1)
	}

	go func() {
		if err := p.Run(ctx); err != nil {
			logger.Error("Proxy stopped: %v", err)
			cancel()
		}
	}()

	<-sigChan
	logger.Info("Shutting down...")
	fw.RemoveRedirect(serverPort, listenPort, externalIP)
}
