package loadtest

import (
	"context"
	"fmt"
	"log"
	"time"
)

const stackCleanupTimeout = 2 * time.Minute

type runTiming struct {
	parent                                   context.Context
	diagnostics                              time.Duration
	prepCtx, runCtx, diagnosticsCtx          context.Context
	prepCancel, runCancel, diagnosticsCancel context.CancelFunc
}

// Preparation and workload have no overall deadline. The scenario controls
// traffic duration; requests retain their own timeouts. Final collection stays
// bounded independently so cancellation cannot discard partial results.
func newRunTiming(parent context.Context, scenario Scenario) *runTiming {
	return &runTiming{
		parent:      parent,
		diagnostics: max(5*time.Minute, scenario.DrainDuration()+2*time.Minute),
	}
}

func phaseContext(parent context.Context, name string, duration time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeoutCause(parent, duration, fmt.Errorf("%s timeout after %s: %w", name, duration, context.DeadlineExceeded))
	deadline, _ := ctx.Deadline()
	log.Printf("loadtest: stage=%s_start timeout=%s deadline=%s", name, duration, deadline.Format(time.RFC3339))
	return ctx, cancel
}

func (t *runTiming) prepare() context.Context {
	t.prepCtx, t.prepCancel = context.WithCancel(t.parent)
	log.Printf("loadtest: stage=preparation_start")
	return t.prepCtx
}

func (t *runTiming) startRun() context.Context {
	t.prepCancel()
	t.runCtx, t.runCancel = context.WithCancel(t.parent)
	log.Printf("loadtest: stage=run_start")
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
