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
	"github.com/qdm12/pingodown/internal/proxy"
)

const dockerIP = "172.17.0.1" // Docker bridge IP

func main() {
	logger, err := logging.NewLogger(logging.ConsoleEncoding, logging.InfoLevel, 0)
	if err != nil {
		panic(err)
	}

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

	// We listen on the original port (host)
	listenAddress := fmt.Sprintf(":%d", serverPort)
	// We forward to the Docker container IP
	serverAddress := fmt.Sprintf("%s:%d", dockerIP, serverPort)

	minPing := time.Duration(pingMS) * time.Millisecond

	logger.Info("Starting pingodown (No-Plugin Mode)")
	logger.Info("Listen address: %s (Host)", listenAddress)
	logger.Info("Target address: %s (Docker)", serverAddress)
	logger.Info("Minimum ping: %d ms", pingMS)

	// Setup signal handler
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p, err := proxy.NewProxy(listenAddress, serverAddress, logger, minPing)
	if err != nil {
		logger.Error("Failed to create proxy: %v", err)
		os.Exit(1)
	}

	// Run proxy
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
	
	logger.Info("Shutdown complete")
}
