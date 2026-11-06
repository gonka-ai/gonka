package loadtest

import (
	"context"
	"fmt"
	"log"
	"time"
)

const defaultPreparationTimeout = 45 * time.Minute
const stackCleanupTimeout = 2 * time.Minute

type runTiming struct {
	parent                                   context.Context
	preparation, run, diagnostics            time.Duration
	prepCtx, runCtx, diagnosticsCtx          context.Context
	prepCancel, runCancel, diagnosticsCancel context.CancelFunc
}

func newRunTiming(parent context.Context, opts RunnerConfig, scenario Scenario) (*runTiming, error) {
	if opts.PreparationTimeout < 0 || opts.RunTimeout < 0 || opts.DiagnosticsTimeout < 0 {
		return nil, fmt.Errorf("preparation, run and diagnostics timeouts must be positive")
	}
	t := &runTiming{parent: parent, preparation: opts.PreparationTimeout, run: opts.RunTimeout, diagnostics: opts.DiagnosticsTimeout}
	if t.preparation == 0 {
		t.preparation = defaultPreparationTimeout
	}
	if t.run == 0 {
		t.run = max(20*time.Minute, scenario.Duration()+scenario.RequestTimeout()+5*time.Minute)
	}
	if t.diagnostics == 0 {
		t.diagnostics = max(5*time.Minute, scenario.DrainDuration()+2*time.Minute)
	}
	return t, nil
}

func phaseContext(parent context.Context, name string, duration time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeoutCause(parent, duration, fmt.Errorf("%s timeout after %s: %w", name, duration, context.DeadlineExceeded))
	deadline, _ := ctx.Deadline()
	log.Printf("loadtest: stage=%s_start timeout=%s deadline=%s", name, duration, deadline.Format(time.RFC3339))
	return ctx, cancel
}

func (t *runTiming) prepare() context.Context {
	t.prepCtx, t.prepCancel = phaseContext(t.parent, "preparation", t.preparation)
	return t.prepCtx
}

func (t *runTiming) startRun() context.Context {
	t.prepCancel()
	t.runCtx, t.runCancel = phaseContext(t.parent, "run", t.run)
	return t.runCtx
}

// Diagnostics must survive a workload deadline or Ctrl+C, but never wait forever.
func (t *runTiming) startDiagnostics() context.Context {
	if t.diagnosticsCtx == nil {
		t.diagnosticsCtx, t.diagnosticsCancel = phaseContext(context.WithoutCancel(t.parent), "diagnostics", t.diagnostics)
	}
	return t.diagnosticsCtx
}

func (t *runTiming) close() {
	if t.prepCancel != nil {
		t.prepCancel()
	}
	if t.runCancel != nil {
		t.runCancel()
	}
	if t.diagnosticsCancel != nil {
		t.diagnosticsCancel()
	}
}
