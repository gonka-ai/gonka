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

// overdueTimeoutInferences returns nonces whose protocol timeout is already
// past. Started inferences use the confirm anchor. Pending ones use the start
// anchor, and only when the race kept the payload a refusal vote must carry.
// Legacy ages the wall anchor plus TimeoutBuffer and the sweep grace. Header
// mode ages the stamp. A missing stamp or a missing wall anchor is not due.
// This selection does not record a clock mismatch.
func overdueTimeoutInferences(state types.EscrowState, now time.Time, store headerTimeStore, budget int, hasRefusalPayload func(nonce uint64) bool) []uint64 {
	var due []uint64
	extra := sweepExtra(state.Config.HeightsyncTimeoutClock)
	for nonce, inference := range state.Inferences {
		if len(due) == budget {
			break
		}
		reason, ok := timeoutReason(inference, hasRefusalPayload, nonce)
		if !ok {
			continue
		}
		deadline, armed := protocolTimeoutDue(state.Config, inference, reason, store, now, extra)
		if !armed || now.Before(deadline) {
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
	if runtime.proxy.sm.Phase() != types.PhaseActive || runtime.session.NonceCapReached() {
		return 0, 0, 0
	}
	snapshot := runtime.proxy.sm.SnapshotState()
	store := storedHeaders(runtime.proxy.sm)
	redundancy.forgetRefusalPayloadsUnless(func(nonce uint64) bool {
		inference, live := snapshot.Inferences[nonce]
		return live && inference.Status == types.StatusPending
	})
	if len(overdueTimeoutInferences(snapshot, time.Now(), store, budget, redundancy.hasRefusalPayload)) == 0 {
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
	snap := runtime.proxy.sm.SnapshotState()
	votedAt := time.Now()
	for _, nonce := range overdueTimeoutInferences(snap, votedAt, store, budget, redundancy.hasRefusalPayload) {
		if ctx.Err() != nil {
			break
		}
		releasePost, acquired := tryAcquireTimeoutVotePost(timeoutVotePosts)
		if !acquired {
			break
		}
		due++
		// The vote passes the wall instant the selection already found past.
		sendTime := time.Time{}
		if rec := snap.Inferences[nonce]; rec != nil {
			if reason, ok := timeoutReason(rec, redundancy.hasRefusalPayload, nonce); ok {
				sendTime = timeoutWall(rec, reason)
			}
		}
		payload, _ := redundancy.refusalPayload(nonce)
		result, err := runtime.session.HandleTimeout(ctx, nonce, sendTime, payload)
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
