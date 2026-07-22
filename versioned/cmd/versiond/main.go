package main

import (
	"context"
	"errors"
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
	mux.HandleFunc("/healthz", health.Handler(mgr.Status))
	mux.HandleFunc("/ready", readinessHandler(hostLifecycle, mgr))
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

	go promoteHostWhenAvailable(pollCtx, mgr, hostLifecycle)

	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		runPollLoop(pollCtx, cfg.PollInterval, oracleClient, mgr)
	}()

	select {
	case <-ctx.Done():
		slog.Info("host shutdown requested", "reason", ctx.Err())
	case sig := <-signals:
		slog.Info("host shutdown requested", "signal", sig.String())
	}

	force := make(chan struct{})
	shutdownDone := make(chan struct{})
	go watchForceSignals(signals, shutdownDone, force)
	defer close(shutdownDone)

	if err := hostLifecycle.Transition(host.StateDraining); err != nil {
		return err
	}
	// Close admission, freeze desired-state changes, and cancel every active
	// generation operation as one host transition. Child process contexts stay
	// alive until the drain phase decides to stop or force them.
	mgr.BeginHostDrain()
	cancelPoll()

	return shutdownHost(cfg, srv, mgr, hostLifecycle, force, pollDone)
}

func runPollLoop(
	ctx context.Context,
	interval time.Duration,
	oracleClient *oracle.Client,
	mgr *process.Manager,
) {
	reconcileOnce(ctx, oracleClient, mgr)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcileOnce(ctx, oracleClient, mgr)
		}
	}
}

func reconcileOnce(ctx context.Context, oracleClient *oracle.Client, mgr *process.Manager) {
	versions, err := oracleClient.Fetch(ctx)
	if err != nil {
		if ctx.Err() == nil {
			mgr.ReportReconcileError(fmt.Errorf("oracle fetch: %w", err))
			slog.Error("oracle fetch failed, keeping current versions", "error", err)
		}
		return
	}
	// An empty API response is treated as an upstream fault while this host owns
	// children. Intentional removals still arrive as a non-empty desired set.
	if len(versions.Versions) == 0 && len(mgr.Status()) > 0 {
		mgr.ReportReconcileError(errors.New("oracle returned an empty version list"))
		slog.Warn("oracle returned empty version list, keeping current versions")
		return
	}
	if err := mgr.Reconcile(ctx, versions.Versions); err != nil &&
		!errors.Is(err, process.ErrHostDraining) && ctx.Err() == nil {
		slog.Error("reconcile failed", "error", err)
	}
}

func promoteHostWhenAvailable(
	ctx context.Context,
	mgr *process.Manager,
	hostLifecycle *host.Controller,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-mgr.Available():
		}
		if !mgr.Conditions().Available {
			continue
		}
		if hostLifecycle.Snapshot().State != host.StateStarting {
			return
		}
		if err := hostLifecycle.Transition(host.StateServing); err != nil {
			slog.Error("host state transition failed", "error", err)
		}
		return
	}
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
	pollDone <-chan struct{},
) error {
	budget := cfg.HostShutdownBudget
	if budget <= 0 {
		budget = config.DefaultHostShutdownBudget
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), budget)
	if forceRequested(force) {
		cancelShutdown()
	}
	stopForceWatch := cancelOnSignal(shutdownCtx, cancelShutdown, force)
	stopEscalationWatch := watchShutdownEscalation(shutdownCtx, srv, mgr, hostLifecycle)
	defer func() {
		stopEscalationWatch()
		stopForceWatch()
		cancelShutdown()
	}()

	select {
	case <-pollDone:
	case <-shutdownCtx.Done():
		// Reconcile work owns manager operations that must unwind before process
		// teardown mutates the same children. Its context is already canceled;
		// the external runtime reserve remains the final backstop.
		<-pollDone
	}

	if err := hostLifecycle.WaitIdle(shutdownCtx); err != nil {
		slog.Warn("host proxy drain incomplete", "error", err, "inflight", hostLifecycle.Snapshot().Inflight)
	}
	if shutdownCtx.Err() == nil {
		if err := mgr.RequestChildrenDrain(shutdownCtx); err != nil {
			slog.Warn("one or more child drain requests failed", "error", err)
		}
		if err := mgr.WaitChildrenIdle(shutdownCtx); err != nil {
			slog.Warn("child drain incomplete", "error", err)
		}
	}

	forced := shutdownCtx.Err() != nil
	if forced {
		if err := transitionHostToForcing(hostLifecycle); err != nil {
			return err
		}
	} else if err := hostLifecycle.Transition(host.StateStopping); err != nil {
		return err
	}

	httpDone := make(chan error, 1)
	go func() {
		httpDone <- srv.Shutdown(shutdownCtx)
	}()
	managerErr := mgr.Shutdown(shutdownCtx)
	httpErr := <-httpDone
	if shutdownCtx.Err() != nil {
		forced = true
		if err := transitionHostToForcing(hostLifecycle); err != nil {
			return err
		}
	}
	if managerErr != nil {
		slog.Warn("manager shutdown required forced escalation", "error", managerErr)
	}
	if httpErr != nil {
		if !forced {
			return fmt.Errorf("shutdown HTTP server: %w", httpErr)
		}
		slog.Warn("HTTP shutdown required forced close", "error", httpErr)
	}
	if err := hostLifecycle.Transition(host.StateStopped); err != nil {
		return err
	}
	return nil
}

func readinessHandler(hostLifecycle *host.Controller, mgr *process.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !versiondReady(hostLifecycle.Snapshot(), mgr.Conditions()) {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	}
}

func versiondReady(hostStatus host.Snapshot, conditions process.Conditions) bool {
	return hostStatus.State == host.StateServing &&
		hostStatus.Accepting &&
		conditions.Available &&
		conditions.Reconciled &&
		!conditions.Progressing &&
		!conditions.Degraded
}

func watchShutdownEscalation(
	ctx context.Context,
	srv *http.Server,
	mgr *process.Manager,
	hostLifecycle *host.Controller,
) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			slog.Warn(
				"host shutdown escalation requested",
				"reason", ctx.Err(),
				"proxy_inflight", hostLifecycle.Snapshot().Inflight,
			)
			mgr.ForceStopChildren()
			_ = srv.Close()
		case <-done:
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
	}
}

func transitionHostToForcing(hostLifecycle *host.Controller) error {
	state := hostLifecycle.Snapshot().State
	if state == host.StateForcing || state == host.StateStopped {
		return nil
	}
	return hostLifecycle.Transition(host.StateForcing)
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
