package main

import (
	"context"
	"log"
	"time"

	"devshard/types"
)

const (
	executionTimeoutSweepBudget  = 8
	executionTimeoutSweepTimeout = 5 * time.Minute
)

type timeoutClock interface {
	TimeoutRemaining(inferenceID uint64, reason types.TimeoutReason) (uint64, error)
	BlocksToDuration(n uint64) time.Duration
}

// overdueTimeoutInferences returns nonces whose timeout would pass the state
// machine's height gate on the next diff: Started inferences against the
// confirm anchor, Pending ones against the start anchor, and only those whose
// race kept the payload a refusal vote must carry.
func overdueTimeoutInferences(state types.EscrowState, clock timeoutClock, budget int, hasRefusalPayload func(nonce uint64) bool) []uint64 {
	var due []uint64
	for nonce, inference := range state.Inferences {
		if len(due) == budget {
			break
		}
		var reason types.TimeoutReason
		switch inference.Status {
		case types.StatusStarted:
			reason = types.TimeoutReason_TIMEOUT_REASON_EXECUTION
		case types.StatusPending:
			if !hasRefusalPayload(nonce) {
				continue
			}
			reason = types.TimeoutReason_TIMEOUT_REASON_REFUSED
		default:
			continue
		}
		if remaining, err := clock.TimeoutRemaining(nonce, reason); err != nil || remaining > 0 {
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
	redundancy := runtime.proxy.redundancy
	if runtime.proxy.sm.Phase() != types.PhaseActive {
		return 0, 0, 0
	}
	snapshot := runtime.proxy.sm.SnapshotState()
	redundancy.forgetRefusalPayloadsUnless(func(nonce uint64) bool {
		inference, live := snapshot.Inferences[nonce]
		return live && inference.Status == types.StatusPending
	})
	if len(overdueTimeoutInferences(snapshot, runtime.proxy.sm, budget, redundancy.hasRefusalPayload)) == 0 {
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
	for _, nonce := range overdueTimeoutInferences(runtime.proxy.sm.SnapshotState(), runtime.proxy.sm, budget, redundancy.hasRefusalPayload) {
		if ctx.Err() != nil {
			break
		}
		releasePost, acquired := tryAcquireTimeoutVotePost(timeoutVotePosts)
		if !acquired {
			break
		}
		due++
		// sendTime zero makes a Pending refusal deadline already past once the
		// sweep found the height gate open.
		payload, _ := redundancy.refusalPayload(nonce)
		result, err := runtime.session.HandleTimeout(ctx, nonce, time.Time{}, payload)
		releasePost()
		if timeoutVoteUnjudged(result) {
			redundancy.noteRefusalSweepFailed(nonce)
		} else {
			redundancy.forgetRefusalPayload(nonce)
		}
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
