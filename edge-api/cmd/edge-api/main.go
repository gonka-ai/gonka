package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"common/chain"
	"edge-api/internal/server"
	"edge-api/observability"
)

const (
	defaultPort     = 18080
	envPort         = "EDGE_API_PORT"
	envChainGRPCURL = "CHAIN_GRPC_URL"
	envChainRPCURL  = "CHAIN_RPC_URL"
	shutdownTimeout = 10 * time.Second
)

func main() {
	observability.InstallLogger(os.Getenv("LOG_FORMAT"))
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}
	if cfg.ChainRPCDerived {
		slog.Warn("chain rpc endpoint derived from gRPC host; set it explicitly to override",
			"env", envChainRPCURL, "chain_rpc", cfg.ChainRPCURL)
	}
	slog.Info("edge-api starting",
		"port", cfg.Port,
		"chain_grpc", cfg.ChainGRPCURL,
		"chain_rpc", cfg.ChainRPCURL,
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

	chainClient, err := chain.NewWithQueryFallback(cfg.ChainGRPCURL, cfg.ChainRPCURL)
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
	ChainRPCURL  string
	// ChainRPCDerived records that ChainRPCURL was guessed from the gRPC host
	// rather than configured, so main can say so once at startup.
	ChainRPCDerived bool
}

// loadConfig requires CHAIN_GRPC_URL explicitly: defaulting it to localhost used
// to hide misconfiguration behind connection errors at query time. The CometBFT
// RPC endpoint is derived from that host when unset, so adding the query
// fallback does not break a deployment that only sets the gRPC endpoint.
func loadConfig() (config, error) {
	port := defaultPort
	if v := os.Getenv(envPort); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return config{}, fmt.Errorf("%s=%q: %w", envPort, v, err)
		}
		port = p
	}

	grpcURL := strings.TrimSpace(os.Getenv(envChainGRPCURL))
	if grpcURL == "" {
		return config{}, fmt.Errorf("%s is required (example: node:9090)", envChainGRPCURL)
	}

	rpcURL := strings.TrimSpace(os.Getenv(envChainRPCURL))
	derived := false
	if rpcURL == "" {
		rpcURL = chain.RPCURLFromGRPCURL(grpcURL)
		if rpcURL == "" {
			return config{}, fmt.Errorf("%s is unset and cannot be derived from %s=%q",
				envChainRPCURL, envChainGRPCURL, grpcURL)
		}
		derived = true
	}

	return config{
		Port:            port,
		ChainGRPCURL:    grpcURL,
		ChainRPCURL:     rpcURL,
		ChainRPCDerived: derived,
	}, nil
}
