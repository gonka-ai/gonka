package main

import (
	"context"
	"log"
	"time"

	"devshard/types"
	"devshard/user"
)

const (
	executionTimeoutSweepBudget  = 8
	executionTimeoutSweepGrace   = 2 * time.Minute
	executionTimeoutSweepTimeout = 5 * time.Minute
)

func executionTimeoutSweepWindow(config types.SessionConfig) time.Duration {
	return time.Duration(config.ExecutionTimeout)*time.Second + user.TimeoutBuffer + executionTimeoutSweepGrace
}

func deadlineFromStamp(stampedAt int64, now time.Time, window time.Duration) time.Time {
	return time.Unix(min(stampedAt, now.Unix()), 0).Add(window)
}

func overdueStartedInferences(state types.EscrowState, now time.Time, budget int) []uint64 {
	window := executionTimeoutSweepWindow(state.Config)
	var due []uint64
	for nonce, inference := range state.Inferences {
		if len(due) == budget {
			break
		}
		if inference.Status != types.StatusStarted || inference.ConfirmedAt <= 0 {
			continue
		}
		if now.Before(deadlineFromStamp(inference.ConfirmedAt, now, window)) {
			continue
		}
		due = append(due, nonce)
	}
	return due
}

func (g *Gateway) startExecutionTimeoutSweep() {
	if !g.executionTimeoutSweepRunning.CompareAndSwap(false, true) {
		return
	}
	g.mu.Lock()
	runtimes := append([]*devshardRuntime(nil), g.runtimeOrder...)
	g.mu.Unlock()
	go func() {
		defer g.executionTimeoutSweepRunning.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), executionTimeoutSweepTimeout)
		defer cancel()
		due, applied, failed := g.sweepExecutionTimeouts(ctx, runtimes, executionTimeoutSweepBudget)
		if due > 0 {
			log.Printf("execution_timeout_sweep_completed due=%d applied=%d failed=%d", due, applied, failed)
		}
	}()
}

func (g *Gateway) sweepExecutionTimeouts(ctx context.Context, runtimes []*devshardRuntime, budget int) (due, applied, failed int) {
	if budget <= 0 || len(runtimes) == 0 {
		return 0, 0, 0
	}
	start := int((g.executionTimeoutSweepCursor.Add(1) - 1) % uint64(len(runtimes)))
	remaining := budget
	for offset := range runtimes {
		if remaining <= 0 || ctx.Err() != nil {
			break
		}
		runtimeDue, runtimeApplied, runtimeFailed := g.sweepRuntimeExecutionTimeouts(ctx, runtimes[(start+offset)%len(runtimes)], remaining)
		remaining -= runtimeDue
		due += runtimeDue
		applied += runtimeApplied
		failed += runtimeFailed
	}
	return due, applied, failed
}

func (g *Gateway) sweepRuntimeExecutionTimeouts(ctx context.Context, runtime *devshardRuntime, budget int) (due, applied, failed int) {
	if runtime == nil || runtime.session == nil || runtime.proxy == nil || runtime.proxy.sm == nil {
		return 0, 0, 0
	}
	if runtime.proxy.sm.Phase() != types.PhaseActive || len(overdueStartedInferences(runtime.proxy.sm.SnapshotState(), time.Now(), budget)) == 0 {
		return 0, 0, 0
	}
	g.mu.Lock()
	isRegistered := g.runtimes[runtime.id] == runtime
	if isRegistered {
		g.startRaceCleanup(runtime)
	}
	g.mu.Unlock()
	if !isRegistered {
		return 0, 0, 0
	}
	defer g.releaseRaceCleanup(runtime)
	unlockFinalize := g.lockFinalize(runtime.id)
	defer unlockFinalize()
	if runtime.proxy.sm.Phase() != types.PhaseActive {
		return 0, 0, 0
	}
	for _, nonce := range overdueStartedInferences(runtime.proxy.sm.SnapshotState(), time.Now(), budget) {
		if ctx.Err() != nil {
			break
		}
		due++
		result, err := runtime.session.HandleTimeout(ctx, nonce, time.Time{}, nil)
		switch {
		case result.Applied:
			applied++
		case err != nil:
			failed++
			log.Printf("execution_timeout_sweep_not_applied escrow=%s nonce=%d outcome=%q detail=%q error=%q",
				runtime.id, nonce, result.Outcome, result.DetailReason, err.Error())
		}
	}
	return due, applied, failed
}
