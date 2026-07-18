package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"versioned/internal/config"
	"versioned/internal/health"
	"versioned/internal/host"
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

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)

	pollCtx, cancelPoll := context.WithCancel(ctx)
	defer cancelPoll()

	mgr := process.NewManager(cfg)
	hostLifecycle := host.NewController()
	oracleClient := oracle.NewClient(cfg.OracleURL)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", health.Handler(mgr.Status, func(ctx context.Context) health.Summary {
		hostStatus := hostLifecycle.Snapshot()
		return health.BuildSummary(
			string(hostStatus.State),
			hostStatus.Accepting,
			hostStatus.Inflight,
			mgr.StatusWithInflight(ctx),
		)
	}))
	mux.Handle("/", hostLifecycle.Admission(proxy.Handler(mgr.RouteTable())))

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
	if versions, err := oracleClient.Fetch(pollCtx); err != nil {
		slog.Error("initial oracle fetch failed", "error", err)
	} else if err := mgr.Reconcile(pollCtx, versions.Versions); err != nil {
		slog.Error("initial reconcile failed", "error", err)
	} else if err := hostLifecycle.Transition(host.StateServing); err != nil {
		return err
	}

	// Poll loop
	var pollWG sync.WaitGroup
	pollWG.Add(1)
	go func() {
		defer pollWG.Done()
		ticker := time.NewTicker(cfg.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				versions, err := oracleClient.Fetch(pollCtx)
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
				if err := mgr.Reconcile(pollCtx, versions.Versions); err != nil {
					slog.Error("reconcile failed", "error", err)
					continue
				}
				if hostLifecycle.Snapshot().State == host.StateStarting {
					if err := hostLifecycle.Transition(host.StateServing); err != nil {
						slog.Error("host state transition failed", "error", err)
					}
				}
			}
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("host shutdown requested", "reason", ctx.Err())
	case sig := <-signals:
		slog.Info("host shutdown requested", "signal", sig.String())
	}

	if err := hostLifecycle.Transition(host.StateDraining); err != nil {
		return err
	}
	// Close admission and freeze desired-state changes as one host transition.
	// Child lifetimes are owned by Manager and remain independent from pollCtx.
	mgr.BeginHostDrain()
	cancelPoll()
	pollWG.Wait()

	force := make(chan struct{})
	shutdownDone := make(chan struct{})
	go watchForceSignals(signals, shutdownDone, force)
	defer close(shutdownDone)

	return shutdownHost(cfg, srv, mgr, hostLifecycle, force)
}

func watchForceSignals(signals <-chan os.Signal, shutdownDone <-chan struct{}, force chan<- struct{}) {
	for {
		select {
		case sig := <-signals:
			if sig == syscall.SIGTERM {
				slog.Warn("duplicate SIGTERM ignored while host is draining")
				continue
			}
			slog.Warn("forcing host shutdown after interrupt", "signal", sig.String())
			close(force)
			return
		case <-shutdownDone:
			return
		}
	}
}

func shutdownHost(
	cfg config.Config,
	srv *http.Server,
	mgr *process.Manager,
	hostLifecycle *host.Controller,
	force <-chan struct{},
) error {
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), cfg.HostDrainTimeout)
	defer cancelDrain()
	stopForceWatch := cancelOnSignal(drainCtx, cancelDrain, force)

	if err := hostLifecycle.WaitIdle(drainCtx); err != nil {
		slog.Warn("host proxy drain incomplete", "error", err, "inflight", hostLifecycle.Snapshot().Inflight)
	}
	if forceRequested(force) {
		stopForceWatch()
		return forceHostShutdown(srv, mgr, hostLifecycle)
	}

	if err := mgr.RequestChildrenDrain(drainCtx); err != nil {
		slog.Warn("one or more child drain requests failed", "error", err)
	}
	if err := mgr.WaitChildrenIdle(drainCtx); err != nil {
		slog.Warn("child drain incomplete", "error", err)
	}
	stopForceWatch()
	if forceRequested(force) {
		return forceHostShutdown(srv, mgr, hostLifecycle)
	}

	if err := hostLifecycle.Transition(host.StateStopping); err != nil {
		return err
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), mgr.ShutdownTimeout())
	stopShutdownForceWatch := cancelOnSignal(shutdownCtx, cancelShutdown, force)
	err := mgr.Shutdown(shutdownCtx)
	stopShutdownForceWatch()
	cancelShutdown()
	if err != nil {
		slog.Warn("manager shutdown required forced escalation", "error", err)
		if transitionErr := hostLifecycle.Transition(host.StateForcing); transitionErr != nil {
			return transitionErr
		}
	}

	if forceRequested(force) {
		return forceHostShutdown(srv, mgr, hostLifecycle)
	}
	httpShutdownCtx, cancelHTTPShutdown := context.WithTimeout(context.Background(), mgr.ShutdownTimeout())
	stopHTTPForceWatch := cancelOnSignal(httpShutdownCtx, cancelHTTPShutdown, force)
	err = srv.Shutdown(httpShutdownCtx)
	stopHTTPForceWatch()
	cancelHTTPShutdown()
	if err != nil {
		_ = srv.Close()
		if forceRequested(force) {
			return forceHostShutdown(srv, mgr, hostLifecycle)
		}
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}
	if err := hostLifecycle.Transition(host.StateStopped); err != nil {
		return err
	}
	return nil
}

func forceHostShutdown(srv *http.Server, mgr *process.Manager, hostLifecycle *host.Controller) error {
	if hostLifecycle.Snapshot().State != host.StateForcing {
		if err := hostLifecycle.Transition(host.StateForcing); err != nil {
			return err
		}
	}
	mgr.ForceStopChildren()
	_ = srv.Close()
	if err := mgr.Shutdown(context.Background()); err != nil {
		return err
	}
	return hostLifecycle.Transition(host.StateStopped)
}

func cancelOnSignal(parent context.Context, cancel context.CancelFunc, signal <-chan struct{}) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-signal:
			cancel()
		case <-parent.Done():
		case <-done:
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
	}
}

func forceRequested(force <-chan struct{}) bool {
	select {
	case <-force:
		return true
	default:
		return false
	}
}
