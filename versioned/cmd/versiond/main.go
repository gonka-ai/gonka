package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"versioned/internal/config"
	"versioned/internal/health"
	"versioned/internal/oracle"
	"versioned/internal/process"
	"versioned/internal/proxy"
)

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	slog.Info(
		"versiond startup",
		"VERSIOND_FORCE", os.Getenv("VERSIOND_FORCE"),
		"VERSIOND_BINARY_NAME", os.Getenv("VERSIOND_BINARY_NAME"),
	)

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	mgr := process.NewManager(cfg)
	oracleClient := oracle.NewClient(cfg.OracleURL)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", health.Handler(mgr.Status))
	mux.Handle("/", proxy.Handler(mgr.RouteTable()))

	listenAddr := config.ListenAddr()
	srv := &http.Server{
		Addr:    listenAddr,
		Handler: mux,
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listenAddr, err)
	}
	go func() {
		slog.Info("starting proxy server", "addr", listenAddr)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("http server error", "error", err)
		}
	}()

	// Initial fetch + reconcile
	if versions, err := oracleClient.Fetch(ctx); err != nil {
		slog.Error("initial oracle fetch failed", "error", err)
	} else if err := mgr.Reconcile(ctx, versions.Versions); err != nil {
		slog.Error("initial reconcile failed", "error", err)
	}

	// Poll loop
	go func() {
		ticker := time.NewTicker(cfg.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				versions, err := oracleClient.Fetch(ctx)
				if err != nil {
					slog.Error("oracle fetch failed, keeping current versions", "error", err)
					continue
				}
				// Guard against empty responses killing all running children.
				// An empty version list from the API is likely a misconfiguration,
				// not an intentional "stop everything" signal.
				if len(versions.Versions) == 0 && len(mgr.Status()) > 0 {
					slog.Warn("oracle returned empty version list, keeping current versions")
					continue
				}
				if err := mgr.Reconcile(ctx, versions.Versions); err != nil {
					slog.Error("reconcile failed", "error", err)
				}
			}
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	srv.Shutdown(shutdownCtx)
	mgr.Shutdown(shutdownCtx)

	return nil
}
