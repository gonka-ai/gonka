package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"common/chain"
	"edge-api/internal/server"
	"edge-api/observability"
)

const (
	defaultPort        = 18080
	defaultChainGRPC   = "localhost:9090"
	envPort            = "EDGE_API_PORT"
	envChainGRPCURL    = "CHAIN_GRPC_URL"
	shutdownTimeout    = 10 * time.Second
)

func main() {
	cfg := loadConfig()
	slog.Info("edge-api starting",
		"port", cfg.Port,
		"chain_grpc", cfg.ChainGRPCURL,
	)

	shutdownObs, err := observability.Init(context.Background(), observability.Config{
		ServiceName: observability.ServiceName,
	})
	if err != nil {
		slog.Error("otel init", "error", err)
		os.Exit(1)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = shutdownObs(ctx)
	}()

	chainClient, err := chain.New(cfg.ChainGRPCURL)
	if err != nil {
		slog.Error("chain client", "error", err)
		os.Exit(1)
	}

	e := server.New(chainClient)
	addr := fmt.Sprintf(":%d", cfg.Port)

	errCh := make(chan error, 1)
	go func() {
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		slog.Error("server", "error", err)
		os.Exit(1)
	case sig := <-stop:
		slog.Info("shutdown", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		slog.Error("shutdown", "error", err)
		os.Exit(1)
	}
}

type config struct {
	Port         int
	ChainGRPCURL string
}

func loadConfig() config {
	port := defaultPort
	if v := os.Getenv(envPort); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			slog.Error("invalid port", "env", envPort, "value", v, "error", err)
			os.Exit(1)
		}
		port = p
	}

	grpcURL := os.Getenv(envChainGRPCURL)
	if grpcURL == "" {
		grpcURL = defaultChainGRPC
	}

	return config{
		Port:         port,
		ChainGRPCURL: grpcURL,
	}
}
