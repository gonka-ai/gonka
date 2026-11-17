package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/logging"
)

// Step 1 lets requests finish for httpGracefulShare and then cancels the ones still open, waiting at most httpCancelledWait, so the steps below keep laterStepsReserve of the grace period; journalCloseFloor lets the journal drain after a step above it spent the budget. See operations.md, "Shutdown".
const (
	httpCancelledWait = time.Second
	laterStepsReserve = 5 * time.Second
	httpGracefulShare = shutdownGracePeriod - laterStepsReserve - httpCancelledWait
	journalCloseFloor = time.Second
)

func (g *gateway) serve(ctx context.Context) error {
	backgroundCtx, stopBackground := context.WithCancel(ctx)
	defer stopBackground()

	configuration := g.config.Load()
	var started bootState
	if err := startAll(g.bootSteps(g, ctx, backgroundCtx, configuration, &started)); err != nil {
		return errors.Join(err, stopThenShutDown(stopBackground, g.shutdown))
	}
	engine := configuration.Engine
	logging.Info("gateway started",
		logkey.Version, Version, logkey.Port, configuration.Server.Port,
		logkey.StorageDir, configuration.Server.StorageDir, logkey.EscrowBuilders, g.builders,
		logkey.ReceiptTimeoutMS, engine.ReceiptTimeoutMS,
		logkey.FirstTokenFloorMS, engine.FirstTokenFloorMS,
		logkey.FirstTokenCeilingMS, engine.FirstTokenCeilingMS,
		logkey.InterChunkStallMS, engine.InterChunkStallMS,
		logkey.LoserGraceMS, engine.LoserGraceMS,
		logkey.HedgeFirstTokenFloorMS, engine.HedgeFirstTokenFloorMS,
		logkey.MaxAttemptsPerRequest, engine.MaxAttemptsPerRequest)

	var listenErr error
	select {
	case listenErr = <-started.listening:
	case <-ctx.Done():
	}
	shutdownErr := stopThenShutDown(stopBackground, g.shutdown)
	<-started.republished
	if listenErr == nil {
		listenErr = <-started.listening
	}
	if listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
		return errors.Join(fmt.Errorf("http server: %w", listenErr), shutdownErr)
	}
	logging.Info("gateway stopped")
	return shutdownErr
}

func stopThenShutDown(stopBackground context.CancelFunc, shutdown func(time.Duration) error) error {
	stopBackground()
	return shutdown(shutdownGracePeriod)
}

// bootState carries out of the boot steps what serve blocks on once they have all run.
type bootState struct {
	unopenedEscrows int
	republished     <-chan struct{}
	listening       chan error
}

// bootStep is one step of the boot contract; the boot stops at the first one that fails. See operations.md, "Boot".
type bootStep struct {
	name  string
	start func() error
}

// bootOrder is the nine-step contract every boot follows. See operations.md, "Boot".
func (g *gateway) bootOrder(ctx, backgroundCtx context.Context, settings *config.Config, started *bootState) []bootStep {
	return []bootStep{
		{name: "chain observer", start: func() error { g.observer.Start(backgroundCtx); return nil }},
		{name: "warmup prober", start: func() error { g.warmup.Start(backgroundCtx); return nil }},
		{name: "host pings", start: func() error { g.hostPings.Start(backgroundCtx); return nil }},
		{name: "seed devshards", start: func() error { return seedDevshards(ctx, g.store, settings.Server.DevshardsJSON) }},
		{name: "publish escrows", start: func() error {
			unopened, err := g.publishEscrows(ctx)
			started.unopenedEscrows = unopened
			return err
		}},
		{name: "nonce ledger", start: func() error { g.nonces.Start(backgroundCtx, g.escrows, g.events); return nil }},
		{name: "escrow lifecycle", start: func() error { g.manager.Start(backgroundCtx); return nil }},
		{name: "devshard write republish", start: func() error {
			started.republished = g.republishOnDevshardWrites(backgroundCtx, started.unopenedEscrows)
			return nil
		}},
		{name: "http listener", start: func() error {
			started.listening = make(chan error, 1)
			go func() { started.listening <- g.server.ListenAndServe() }()
			return nil
		}},
	}
}

// startAll stops at the first step that fails, so nothing behind a half-built one is started. See operations.md, "Boot".
func startAll(steps []bootStep) error {
	for _, step := range steps {
		if err := step.start(); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return nil
}

// needsQuiesced marks a step that destroys state the steps above it may still use. See rules.md, "6. Boot and shutdown order is a contract".
type shutdownStep struct {
	name          string
	stop          func(context.Context) error
	needsQuiesced bool
}
type httpListener interface {
	Shutdown(ctx context.Context) error
}
type stopper interface{ Stop() }

// idleConnections is satisfied by *http.Client, whose pooled sockets nothing above it closes.
type idleConnections interface{ CloseIdleConnections() }

type shutdownParts struct {
	listener        httpListener
	endRequests     context.CancelFunc
	races           stopper
	dispatchers     stopper
	escrowLifecycle stopper
	chainObserver   stopper
	nonceSweep      stopper
	sessions        io.Closer
	events          io.Closer
	nonceLedger     io.Closer
	governanceFeed  io.Closer
	heightFollower  io.Closer
	storage         io.Closer
	publicAPI       idleConnections
}

// shutdownOrder is the thirteen-step contract every shutdown follows. See operations.md, "Shutdown".
func shutdownOrder(parts shutdownParts) []shutdownStep {
	return []shutdownStep{
		{name: "http server", stop: stopListener(parts.listener, parts.endRequests)},
		{name: "races", stop: waitFor(parts.races)},
		{name: "dispatchers", stop: waitFor(parts.dispatchers)},
		{name: "escrow lifecycle", stop: waitFor(parts.escrowLifecycle)},
		{name: "chain observer", stop: waitFor(parts.chainObserver)},
		{name: "nonce sweep", stop: waitFor(parts.nonceSweep)},
		{name: "escrow sessions", stop: closeOf(parts.sessions), needsQuiesced: true},
		{name: "journal", stop: closeWithin(parts.events, journalCloseFloor)},
		// After every emitter above, so the final snapshot holds the counters the run ended with.
		{name: "nonce accounting", stop: closeOf(parts.nonceLedger)},
		{name: "runtime params", stop: closeOf(parts.governanceFeed)},
		// After the sessions that carried its readings, before the store they persisted into.
		{name: "height follower", stop: closeOf(parts.heightFollower)},
		{name: "store", stop: closeOf(parts.storage)},
		// Last: every step above can still reach the public API. See ../README.md, "Shutdown".
		{name: "public api connections", stop: closeIdle(parts.publicAPI)},
	}
}

func stopListener(listener httpListener, endRequests context.CancelFunc) func(context.Context) error {
	return func(ctx context.Context) error {
		bounded, cancelBounded := context.WithTimeout(ctx, httpGracefulShare+httpCancelledWait)
		defer cancelBounded()
		stopped := make(chan error, 1)
		go func() { stopped <- listener.Shutdown(bounded) }()
		graceful := time.NewTimer(httpGracefulShare)
		defer graceful.Stop()
		select {
		case err := <-stopped:
			return err
		case <-graceful.C:
		}
		endRequests()
		return <-stopped
	}
}

// waitFor bounds a drain by the shutdown budget without cancelling it. See ../README.md, "Shutdown".
func waitFor(component stopper) func(context.Context) error {
	return func(ctx context.Context) error {
		stopped := make(chan struct{})
		go func() { defer close(stopped); component.Stop() }()
		select {
		case <-stopped:
			return nil
		case <-ctx.Done():
			return fmt.Errorf("abandoned with work still running: %w", ctx.Err())
		}
	}
}

func closeOf(component io.Closer) func(context.Context) error {
	return func(context.Context) error { return component.Close() }
}

// closeWithin bounds a close by the shutdown budget, and by at least floor, as waitFor bounds a drain. See ../README.md, "Shutdown".
func closeWithin(component io.Closer, floor time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		closed := make(chan error, 1)
		go func() { closed <- component.Close() }()
		floorTimer := time.NewTimer(floor)
		defer floorTimer.Stop()
		select {
		case err := <-closed:
			return err
		case <-floorTimer.C:
		}
		select {
		case err := <-closed:
			return err
		case <-ctx.Done():
		}
		select {
		case err := <-closed:
			return err
		default:
			return fmt.Errorf("abandoned with events still queued: %w", ctx.Err())
		}
	}
}

func closeIdle(component idleConnections) func(context.Context) error {
	return func(context.Context) error {
		component.CloseIdleConnections()
		return nil
	}
}

// stopAll runs every step even after a failure, skipping and reporting a needsQuiesced one. See ../README.md, "Shutdown".
func stopAll(ctx context.Context, steps []shutdownStep) error {
	var problems []error
	quiesced := true
	for _, step := range steps {
		if step.needsQuiesced && !quiesced {
			problems = append(problems, fmt.Errorf("skipping %s: work above it is still running", step.name))
			continue
		}
		if err := step.stop(ctx); err != nil {
			problems = append(problems, fmt.Errorf("stopping %s: %w", step.name, err))
			quiesced = false
		}
	}
	return errors.Join(problems...)
}

func (g *gateway) shutdown(grace time.Duration) error {
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), grace)
	defer cancelDrain()
	return stopAll(drainCtx, shutdownOrder(g.partsToStop()))
}

func (g *gateway) partsToStop() shutdownParts {
	return shutdownParts{
		listener:        g.server,
		endRequests:     g.endRequests,
		races:           g.races,
		dispatchers:     g.router,
		escrowLifecycle: g.manager,
		chainObserver:   g.observer,
		nonceSweep:      g.nonces,
		sessions:        g.escrows,
		events:          g.events,
		nonceLedger:     g.nonces,
		governanceFeed:  g.governance,
		heightFollower:  g.heights,
		storage:         g.store,
		publicAPI:       g.publicAPI,
	}
}

// bootBudget sizes the build limit and the idle pool those builds reuse together. See ../README.md, "Wiring order, and the knots in it".
type bootBudget struct {
	builders int
	client   *http.Client
}

func newBootBudget(builders int) bootBudget {
	if builders < 1 {
		builders = 1
	}
	pooled := http.DefaultTransport.(*http.Transport).Clone()
	pooled.MaxIdleConns = builders
	pooled.MaxIdleConnsPerHost = builders
	return bootBudget{
		builders: builders,
		client:   &http.Client{Timeout: chainRequestTimeout, Transport: pooled},
	}
}
