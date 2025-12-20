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

const dockerGatewayIP = "172.17.0.1"

func main() {
	logger, err := logging.NewLogger(logging.ConsoleEncoding, logging.InfoLevel, 0)
	if err != nil {
		panic(err)
	}

	// Arguments: ./pingodown SERVER_PORT PING_MS
	if len(os.Args) != 3 {
		logger.Error("Usage: ./pingodown SERVER_PORT PING_MS")
		logger.Error("Example: ./pingodown 27015 100")
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

	// 1. Determine Target Address (Docker vs. External IP)
	var targetIP string
	if net_utils.IsDockerPresent() {
		// Docker mode: forward to the Docker bridge gateway
		targetIP = dockerGatewayIP
		logger.Info("Docker detected. Forwarding to Docker gateway: %s", targetIP)
	} else {
		// Non-Docker mode: forward to the server's own external IP
		extIP, err := net_utils.GetServerExternalIP()
		if err != nil {
			logger.Error("Failed to get server external IP for forwarding: %v", err)
			os.Exit(1)
		}
		targetIP = extIP
		logger.Info("No Docker detected. Forwarding to self (external IP): %s", targetIP)
	}

	// 2. Setup Ports
	// The proxy listens on a port adjacent to the main server port.
	listenPort := serverPort + 1
	if listenPort > 65535 {
		logger.Error("Calculated listen port %d exceeds 65535.", listenPort)
		os.Exit(1)
	}
	
	listenAddress := fmt.Sprintf(":%d", listenPort)
	targetAddress := fmt.Sprintf("%s:%d", targetIP, serverPort)
	minPing := time.Duration(pingMS) * time.Millisecond

	// 3. Setup Firewall Redirect
	// Redirect external traffic from serverPort -> listenPort where the proxy is listening
	fw := firewall.NewFirewall(logger)
	logger.Info("Setting up firewall redirect: UDP port %d -> %d", serverPort, listenPort)
	if err := fw.AddRedirect(serverPort, listenPort); err != nil {
		logger.Error("Failed to add firewall redirect: %v", err)
		logger.Info("Ensure you are running with sudo/root privileges.")
		os.Exit(1)
	}

	// Setup context and signal handler for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())

	// Cleanup firewall rules on shutdown
	defer func() {
		logger.Info("Cleaning up firewall redirect rule...")
		if err := fw.RemoveRedirect(serverPort, listenPort); err != nil {
			logger.Error("Failed to remove firewall redirect: %v", err)
		}
	}()

	// 4. Start Proxy
	p, err := proxy.NewProxy(listenAddress, targetAddress, logger, minPing)
	if err != nil {
		logger.Error("Failed to create proxy: %v", err)
		os.Exit(1)
	}
	
	errChan := make(chan error, 1)
	go func() {
		errChan <- p.Run(ctx)
	}()

	// Wait for shutdown signal or proxy error
	select {
	case <-sigChan:
		logger.Info("Shutdown signal received.")
	case err := <-errChan:
		logger.Error("Proxy exited with error: %v", err)
	}

	cancel()
	time.Sleep(500 * time.Millisecond) // Give a moment for goroutines to close
	logger.Info("Shutdown complete.")
}
