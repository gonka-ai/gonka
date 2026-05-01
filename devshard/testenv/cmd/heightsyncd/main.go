// Binary heightsyncd runs the reusable blockoracle module as a standalone
// HTTP/SSE service.
//
// In the testenv it is the single authoritative publisher of block
// headers; all devshardd-testenv instances subscribe to it. In
// production, decentralized-api mounts the same module in-process, so
// this binary is testenv-only.
//
// Endpoints (see devshard/blockoracle/server):
//
//	GET /healthz
//	GET /block/latest
//	GET /block/:height
//	GET /block/:height/prove?path=
//	GET /block/stream[?from=]
//
// Phase 3; see devshard/docs/testenv.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"devshard/blockoracle/standalone"
	"devshard/testenv/config"
)

func main() {
	cfgPath := flag.String("config", envOr("CONFIG_PATH", "config.yaml"), "path to testenv config YAML")
	portOverride := flag.Int("port", 0, "override config.height_sync.port (0 = use config)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid config: %v", err)
	}
	if *portOverride > 0 {
		cfg.HeightSync.Port = *portOverride
	}
	if envPort := os.Getenv("HEIGHT_SYNC_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil && p > 0 {
			cfg.HeightSync.Port = p
		}
	}

	standaloneCfg, err := buildStandaloneConfig(cfg)
	if err != nil {
		log.Fatalf("build standalone config: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Printf("heightsyncd listening on %s chain=%q validators=%d interval=%s seed=%d initial_height=%d",
		standaloneCfg.Addr, standaloneCfg.ChainID, len(standaloneCfg.Validators),
		standaloneCfg.BlockInterval, standaloneCfg.Seed, standaloneCfg.InitialHeight)

	if err := standalone.Run(ctx, standaloneCfg); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("heightsyncd: %v", err)
	}
	log.Printf("heightsyncd: shutdown complete")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// buildStandaloneConfig maps a testenv config into the producer-side
// standalone.Config. Extracted from main so unit tests can exercise the
// validator-set plumbing without starting the process: a test-time
// caller is free to inject its own Listener / ShutdownTimeout on top
// of the returned value.
func buildStandaloneConfig(cfg *config.Config) (standalone.Config, error) {
	resolved, err := cfg.HeightSyncValidators()
	if err != nil {
		return standalone.Config{}, fmt.Errorf("height_sync validators: %w", err)
	}
	validators := make([]standalone.Validator, len(resolved))
	for i, v := range resolved {
		validators[i] = standalone.Validator{
			Signer:  v.Signer,
			Address: v.Address,
			Power:   v.Power,
		}
	}
	return standalone.Config{
		ChainID:       cfg.Chain.ID,
		Validators:    validators,
		BlockInterval: cfg.HeightSyncBlockInterval(),
		InitialHeight: cfg.HeightSync.InitialHeight,
		Seed:          cfg.HeightSync.Seed,
		Addr:          fmt.Sprintf(":%d", cfg.HeightSync.Port),
	}, nil
}
