// Binary mockchain serves the testenv mock cosmos/tendermint gRPC service.
//
// Phase 2 (see devshard/docs/testenv.md) wires it against config.yaml and
// implements the MainnetBridge-facing queries plus a SettleEscrow action
// that runs devshard/state.VerifySettlement.
//
// The server exposes:
//
//   - gRPC on  :$MOCK_CHAIN_PORT  (service MockChain + gRPC reflection)
//   - HTTP on  :$MOCK_CHAIN_PORT+1 (/health, /status, /ready)
//
// gRPC reflection is enabled so docker-compose healthchecks can use
// `grpc_health_probe -tls=false -addr=localhost:PORT` or generic gRPC
// probes.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"devshard/testenv/config"
	"devshard/testenv/proto/mockchainpb"
)

func main() {
	cfgPath := flag.String("config", envOr("CONFIG_PATH", "config.yaml"), "path to testenv config YAML")
	portOverride := flag.Int("port", 0, "override config.mock_chain.port (0 = use config)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid config: %v", err)
	}
	if *portOverride > 0 {
		cfg.MockChain.Port = *portOverride
	}
	if envPort := os.Getenv("MOCK_CHAIN_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil && p > 0 {
			cfg.MockChain.Port = p
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("mockchain: %v", err)
	}
}

// run starts both listeners and blocks until ctx is cancelled.
func run(ctx context.Context, cfg *config.Config) error {
	srv := newMockServer(cfg)

	grpcAddr := fmt.Sprintf(":%d", cfg.MockChain.Port)
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", grpcAddr, err)
	}

	gs := grpc.NewServer()
	mockchainpb.RegisterMockChainServer(gs, srv)
	hs := health.NewServer()
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	hs.SetServingStatus("devshard.testenv.mockchain.v1.MockChain", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(gs, hs)
	reflection.Register(gs)

	httpAddr := fmt.Sprintf(":%d", cfg.MockChain.Port+1)
	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	httpMux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	httpMux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(srv.status()))
	})
	httpSrv := &http.Server{
		Addr:              httpAddr,
		Handler:           httpMux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("mockchain gRPC listening on %s (escrow=%s hosts=%d slots=%d)",
		grpcAddr, cfg.Escrow.ID, len(cfg.Hosts), cfg.Escrow.Slots)
	log.Printf("mockchain HTTP status listening on %s", httpAddr)

	var wg sync.WaitGroup
	wg.Add(2)
	errCh := make(chan error, 2)

	go func() {
		defer wg.Done()
		if err := gs.Serve(lis); err != nil {
			errCh <- fmt.Errorf("grpc serve: %w", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http serve: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		log.Printf("mockchain: server error: %v", err)
	}

	log.Printf("mockchain: shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	gs.GracefulStop()
	wg.Wait()
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
