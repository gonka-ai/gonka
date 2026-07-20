// Command devshardd is a standalone devshard host process managed by versiond.
//
// Versiond invokes this binary with `--port <N>` and `--data-dir <PATH>` as
// its process contract. Everything else is configured via environment variables.
package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"devshard/observability"
)

// Version is the devshard protocol name (approved_versions.name). Set at build
// via `make devshardd-build DEVSHARD_VERSION=...` / -X main.Version=... .
// Defaults to "dev" for local builds without an ldflags override.
var Version = "dev"

// BinaryVersion is the release/build identifier (e.g. 0.2.13-v2-r2). Same
// protocol can ship multiple binaries; this value is for logs only.
// Set via DEVSHARD_BINARY_VERSION at build / -X main.BinaryVersion=... .
var BinaryVersion = "dev-log"

func main() {
	if maybePrintVersionAndExit(os.Args[1:]) {
		return
	}
	if err := run(context.Background(), os.Args[1:], Version, BinaryVersion); err != nil {
		log.Fatalf("devshardd: %v", err)
	}
}

func run(parent context.Context, args []string, protocolVersion, binaryVersion string) error {
	initSdkBech32Prefix()

	cfg, err := loadRuntimeConfig(args, protocolVersion, binaryVersion)
	if err != nil {
		return err
	}

	slog.SetDefault(slog.New(newPrefixedTextHandler(cfg.BinaryLogVersion, os.Stderr, slog.LevelInfo)))

	observability.SetRuntime(cfg.BinaryLogVersion, cfg.ProtocolVersion, "standalone")
	shutdownObs, err := observability.Init(parent, observability.Config{
		ServiceName:    observability.ServiceName,
		ServiceVersion: cfg.ProtocolVersion,
	})
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = shutdownObs(shutdownCtx)
	}()

	slog.Info("devshardd starting",
		"protocol_version", cfg.ProtocolVersion,
		"binary_log_version", cfg.BinaryLogVersion,
		"runtime_version", cfg.RuntimeVersion,
		"port", cfg.Port,
		"data-dir", cfg.DataDir)

	ctx, cancel := signal.NotifyContext(parent, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	app, err := buildApp(ctx, cfg)
	if err != nil {
		return err
	}
	return app.Run(ctx)
}
