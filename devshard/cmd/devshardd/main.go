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
)

// Version is the devshardd version. Set via ldflags
// -X main.Version=... . Defaults to "dev" for local builds without an
// ldflags override.
var Version = "dev"

func main() {
	if err := run(context.Background(), os.Args[1:], Version); err != nil {
		log.Fatalf("devshardd: %v", err)
	}
}

func run(parent context.Context, args []string, buildVersion string) error {
	initSdkBech32Prefix()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := loadRuntimeConfig(args, buildVersion)
	if err != nil {
		return err
	}

	slog.Info("devshardd starting",
		"build_version", cfg.BuildVersion,
		"selected_version", cfg.SelectedVersion,
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
