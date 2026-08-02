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

	envDrainAnnounce  = "EDGE_API_DRAIN_ANNOUNCE"
	envShutdownBudget = "EDGE_API_SHUTDOWN_BUDGET"

	// defaultDrainAnnounce must exceed the balancer's health-check interval
	// (edge-api-router probes /readyz every second) so the instance is out of
	// rotation before it stops accepting.
	defaultDrainAnnounce = 5 * time.Second
	// defaultShutdownBudget matches the router's default read timeout: the
	// process should wait for exactly as long as the hop in front is still
	// willing to wait for the answer, and no longer.
	defaultShutdownBudget = 2 * time.Minute
	// observabilityShutdownTimeout flushes traces after serving has stopped.
	observabilityShutdownTimeout = 10 * time.Second
)

func main() {
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
		"drain_announce", cfg.DrainAnnounce,
		"shutdown_budget", cfg.ShutdownBudget,
	)

	shutdownObs, err := observability.Init(context.Background(), observability.Config{
		ServiceName: observability.ServiceName,
	})
	if err != nil {
		slog.Error("otel init", "error", err)
		os.Exit(1)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), observabilityShutdownTimeout)
		defer cancel()
		_ = shutdownObs(ctx)
	}()

	chainClient, err := chain.NewWithQueryFallback(cfg.ChainGRPCURL, cfg.ChainRPCURL)
	if err != nil {
		slog.Error("chain client", "error", err)
		os.Exit(1)
	}

	srv := server.New(chainClient)
	addr := fmt.Sprintf(":%d", cfg.Port)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Start(addr); err != nil && err != http.ErrServerClosed {
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

	// Report unready while still serving, so the balancer sees the failing check
	// and stops routing here before anything is refused. A second signal is the
	// explicit "do not wait" from the operator.
	srv.BeginDrain()
	awaitDrainAnnouncement(cfg.DrainAnnounce, stop)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownBudget)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		// The budget expired with requests still running. Close cuts them rather
		// than leaving the process for the runtime to SIGKILL mid-write.
		slog.Error("graceful shutdown did not finish; closing remaining connections",
			"error", err, "budget", cfg.ShutdownBudget)
		if closeErr := srv.Close(); closeErr != nil {
			slog.Error("close", "error", closeErr)
		}
		os.Exit(1)
	}
}

// awaitDrainAnnouncement keeps serving for the announce window so the balancer
// can observe the failing readiness check. A second signal cuts it short.
func awaitDrainAnnouncement(window time.Duration, force <-chan os.Signal) {
	if window <= 0 {
		return
	}
	timer := time.NewTimer(window)
	defer timer.Stop()
	slog.Info("announcing drain; still serving accepted traffic", "window", window)
	select {
	case sig := <-force:
		slog.Info("drain announcement cut short", "signal", sig.String())
	case <-timer.C:
	}
}

type config struct {
	Port         int
	ChainGRPCURL string
	ChainRPCURL  string
	// DrainAnnounce is how long the process keeps serving after /readyz starts
	// failing; ShutdownBudget is how long it then waits for accepted requests.
	DrainAnnounce  time.Duration
	ShutdownBudget time.Duration
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

	drainAnnounce, err := durationFromEnv(envDrainAnnounce, defaultDrainAnnounce)
	if err != nil {
		return config{}, err
	}
	shutdownBudget, err := durationFromEnv(envShutdownBudget, defaultShutdownBudget)
	if err != nil {
		return config{}, err
	}
	if shutdownBudget <= 0 {
		return config{}, fmt.Errorf("%s must be positive", envShutdownBudget)
	}

	return config{
		Port:            port,
		ChainGRPCURL:    grpcURL,
		ChainRPCURL:     rpcURL,
		ChainRPCDerived: derived,
		DrainAnnounce:   drainAnnounce,
		ShutdownBudget:  shutdownBudget,
	}, nil
}

// durationFromEnv rejects a malformed value instead of silently falling back:
// a typo in a shutdown budget would otherwise be found only during an outage.
// A zero announce window is legitimate — it means "no balancer in front".
func durationFromEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", key, raw, err)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s=%q must not be negative", key, raw)
	}
	return value, nil
}
