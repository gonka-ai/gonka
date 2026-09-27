package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"devshard/types"
	"devshard/user"
)

type keyedInFlight struct {
	guard sync.Mutex
	keys  map[string]struct{}
}

func (inFlight *keyedInFlight) enter(key string) (leave func(), entered bool) {
	inFlight.guard.Lock()
	defer inFlight.guard.Unlock()
	if inFlight.keys == nil {
		inFlight.keys = make(map[string]struct{})
	}
	if _, isBusy := inFlight.keys[key]; isBusy {
		return nil, false
	}
	inFlight.keys[key] = struct{}{}
	return func() {
		inFlight.guard.Lock()
		delete(inFlight.keys, key)
		inFlight.guard.Unlock()
	}, true
}

const escrowHoldReleaseResponses = 32

func servingEscrowsNeededForHolds(heldCount int) int {
	return (heldCount + 1) / 2
}

func escrowHoldReleaseBalance(config types.SessionConfig) uint64 {
	return balanceMinimumThreshold + escrowHoldReleaseResponses*RequestMaxTokensCap*config.TokenPrice
}

func escrowHoldPendingWindow(config types.SessionConfig) time.Duration {
	return time.Duration(config.RefusalTimeout)*time.Second + user.TimeoutBuffer + balanceCheckInterval
}

type escrowHoldInFlightSummary struct {
	pendingCount          int
	pendingCost           uint64
	startedCount          int
	startedCost           uint64
	challengedCount       int
	challengedCost        uint64
	overduePendingCount   int
	overduePendingCost    uint64
	latestPendingDeadline time.Time
}

func summarizeEscrowHoldInFlight(inferences map[uint64]*types.InferenceRecord, config types.SessionConfig, now time.Time) escrowHoldInFlightSummary {
	var summary escrowHoldInFlightSummary
	pendingWindow := escrowHoldPendingWindow(config)
	for _, inference := range inferences {
		switch inference.Status {
		case types.StatusPending:
			summary.pendingCount++
			summary.pendingCost += inference.ReservedCost
			deadline := deadlineFromStamp(inference.StartedAt, now, pendingWindow)
			if !now.Before(deadline) {
				summary.overduePendingCount++
				summary.overduePendingCost += inference.ReservedCost
			}
			if deadline.After(summary.latestPendingDeadline) {
				summary.latestPendingDeadline = deadline
			}
		case types.StatusStarted:
			summary.startedCount++
			summary.startedCost += inference.ReservedCost
		case types.StatusChallenged:
			summary.challengedCount++
			summary.challengedCost += inference.ActualCost
		}
	}
	return summary
}

func (summary escrowHoldInFlightSummary) recoverable(hasBackgroundWork bool) uint64 {
	recoverable := summary.pendingCost + summary.startedCost + summary.challengedCost
	if hasBackgroundWork {
		return recoverable
	}
	return recoverable - summary.overduePendingCost
}

func (summary escrowHoldInFlightSummary) breakdown() string {
	return fmt.Sprintf("pending=%d pending_cost=%d started=%d started_cost=%d challenged=%d challenged_cost=%d overdue_pending=%d overdue_pending_cost=%d latest_pending_deadline=%s",
		summary.pendingCount, summary.pendingCost, summary.startedCount, summary.startedCost,
		summary.challengedCount, summary.challengedCost, summary.overduePendingCount, summary.overduePendingCost, summary.latestPendingDeadlineLabel())
}

func (summary escrowHoldInFlightSummary) latestPendingDeadlineLabel() string {
	if summary.latestPendingDeadline.IsZero() {
		return "none"
	}
	return summary.latestPendingDeadline.UTC().Format(time.RFC3339Nano)
}

func (rt *devshardRuntime) clearHold() {
	rt.holdSince.Store(0)
}

func (g *Gateway) canReplaceEscrowModel(modelID string) bool {
	g.mu.Lock()
	settings := g.settings
	g.mu.Unlock()
	_, ok := replacementModelForDepletedEscrow(settings, modelID)
	return ok
}

func (g *Gateway) holdOrReplaceDepletedEscrow(runtime *devshardRuntime, reason string) {
	if runtime.holdSince.Load() != 0 {
		return
	}
	state := runtime.proxy.sm.SnapshotState()
	recoverable := summarizeEscrowHoldInFlight(state.Inferences, state.Config, time.Now()).recoverable(runtime.escrowHasBackgroundWork())
	releaseBalance := escrowHoldReleaseBalance(state.Config)
	if state.Balance+recoverable < releaseBalance || !g.canReplaceEscrowModel(runtime.model) {
		g.scheduleDepletedEscrowReplacement(runtime.id, runtime.model, reason)
		return
	}
	since, isHeld, err := g.store.HoldDevshardIfActive(runtime.id, time.Now().UTC())
	if err != nil {
		log.Printf("escrow_hold_persist_failed escrow=%s error=%v", runtime.id, err)
		return
	}
	if !isHeld {
		return
	}
	runtime.holdSince.Store(since.UnixNano())
	log.Printf("escrow_hold_started escrow=%s reason=%q balance=%d recoverable_in_flight=%d release_balance=%d on_hold_since=%s",
		runtime.id, reason, state.Balance, recoverable, releaseBalance, since.Format(time.RFC3339Nano))
}

func (g *Gateway) holdOrReplaceExhaustedEscrow(escrowID, modelID string) {
	g.mu.Lock()
	runtime, isResident := g.runtimes[escrowID]
	g.mu.Unlock()
	if !isResident || runtime.proxy == nil || runtime.proxy.sm == nil {
		g.scheduleDepletedEscrowReplacement(escrowID, modelID, "balance_exhausted")
		return
	}
	g.holdOrReplaceDepletedEscrow(runtime, "balance_exhausted")
}

func (g *Gateway) resolveHeldEscrow(runtime *devshardRuntime, now time.Time) {
	state := runtime.proxy.sm.SnapshotState()
	summary := summarizeEscrowHoldInFlight(state.Inferences, state.Config, now)
	recoverable := summary.recoverable(runtime.escrowHasBackgroundWork())
	releaseBalance := escrowHoldReleaseBalance(state.Config)
	switch {
	case state.Balance >= releaseBalance:
		g.releaseEscrowHold(runtime, "balance_recovered")
	case !g.canReplaceEscrowModel(runtime.model):
		g.releaseEscrowHold(runtime, "no_replacement_model")
	case state.Balance+recoverable < releaseBalance:
		log.Printf("escrow_hold_unrecoverable escrow=%s balance=%d recoverable_in_flight=%d release_balance=%d held_for=%s %s",
			runtime.id, state.Balance, recoverable, releaseBalance, now.Sub(time.Unix(0, runtime.holdSince.Load())), summary.breakdown())
		g.scheduleDepletedEscrowReplacement(runtime.id, runtime.model, "low_balance")
	}
}

func (g *Gateway) releaseEscrowHold(runtime *devshardRuntime, reason string) {
	if err := g.store.ReleaseDevshardHold(runtime.id); err != nil {
		log.Printf("escrow_hold_release_persist_failed escrow=%s error=%v", runtime.id, err)
		return
	}
	runtime.clearHold()
	log.Printf("escrow_hold_released escrow=%s reason=%q", runtime.id, reason)
}

func (g *Gateway) releaseEscrowHolds(runtimes []*devshardRuntime, reason string) {
	for _, runtime := range runtimes {
		if runtime != nil && runtime.holdSince.Load() != 0 {
			g.releaseEscrowHold(runtime, reason)
		}
	}
}

func (g *Gateway) escrowHoldCounts(modelID string) (servingCount, heldCount int) {
	g.mu.Lock()
	runtimes := append([]*devshardRuntime(nil), g.runtimeOrder...)
	g.mu.Unlock()
	for _, runtime := range runtimes {
		if runtime == nil || runtime.model != modelID || !runtime.active.Load() {
			continue
		}
		if runtime.holdSince.Load() != 0 {
			heldCount++
			continue
		}
		if accepts, _ := runtime.acceptsNewInferences(); accepts {
			servingCount++
		}
	}
	return servingCount, heldCount
}

func (g *Gateway) topUpServingEscrowsForHolds(runtimes []*devshardRuntime) {
	heldModels := make(map[string]struct{})
	for _, runtime := range runtimes {
		if runtime != nil && runtime.active.Load() && runtime.holdSince.Load() != 0 {
			heldModels[runtime.model] = struct{}{}
		}
	}
	for modelID := range heldModels {
		g.scheduleHoldTopUp(modelID)
	}
}

func (g *Gateway) scheduleHoldTopUp(modelID string) {
	leave, entered := g.holdTopUpsInFlight.enter(modelID)
	if !entered {
		return
	}
	go func() {
		defer leave()
		ctx, cancel := context.WithTimeout(context.Background(), autoSettlementAttemptTimeout)
		defer cancel()
		if err := g.topUpServingEscrows(ctx, modelID); err != nil {
			log.Printf("escrow_hold_top_up_failed model=%q error=%v", modelID, err)
		}
	}()
}

func (g *Gateway) topUpServingEscrows(ctx context.Context, modelID string) error {
	g.mu.Lock()
	settings := g.settings
	phaseGate := g.phaseGate
	g.mu.Unlock()
	model, ok := replacementModelForDepletedEscrow(settings, modelID)
	if !ok || phaseGate == nil {
		return nil
	}
	snapshot := phaseGate.Snapshot()
	if snapshot.EpochIndex == 0 {
		return nil
	}
	role, epoch := rotationPlacement(snapshot, settings.EscrowRotation.PrePoCBlocks)
	if served, known := g.rotationModelServedByNetwork(modelID); known && !served {
		return nil
	}
	unlockTarget := g.rotationTargetLocks.lock(rotationTargetKey(modelID, role, epoch))
	defer unlockTarget()
	servingCount, heldCount := g.escrowHoldCounts(modelID)
	unheldCount, err := g.activeRotationEscrowCount(role, epoch, modelID)
	if err != nil {
		return fmt.Errorf("count escrows for held model: %w", err)
	}
	missingCount := max(servingEscrowsNeededForHolds(heldCount)-servingCount, rotationTargetForRole(model, role)-unheldCount)
	if heldCount == 0 || missingCount <= 0 || g.rotationCreateGated(modelID, role) {
		return nil
	}
	for created := 0; created < missingCount; created++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := gatewayCreateRotationEscrow(g, ctx, settings, model, role, epoch)
		if err != nil {
			g.recordRotationCreateFailure(modelID, role)
			return fmt.Errorf("create escrow for held model: %w", err)
		}
		log.Printf("escrow_hold_top_up_created model=%q role=%s epoch=%d escrow=%d serving=%d held=%d",
			modelID, role, epoch, result.EscrowID, servingCount+created+1, heldCount)
	}
	g.resetRotationBreaker(modelID, role)
	return nil
}

func (g *Gateway) restoreEscrowHolds() {
	if g == nil || g.store == nil {
		return
	}
	state, ok, err := g.store.LoadState()
	if err != nil || !ok {
		if err != nil {
			log.Printf("escrow_hold_restore_load_failed error=%v", err)
		}
		return
	}
	for _, devshard := range state.Devshards {
		if !devshard.Active || devshard.OnHoldSince == "" {
			continue
		}
		since, err := time.Parse(time.RFC3339Nano, devshard.OnHoldSince)
		if err != nil {
			log.Printf("escrow_hold_restore_unparseable escrow=%s on_hold_since=%q error=%v", devshard.ID, devshard.OnHoldSince, err)
			since = time.Now().UTC()
		}
		g.mu.Lock()
		runtime, isResident := g.runtimes[devshard.ID]
		g.mu.Unlock()
		if isResident {
			runtime.holdSince.Store(since.UnixNano())
			log.Printf("escrow_hold_restored escrow=%s on_hold_since=%s", devshard.ID, devshard.OnHoldSince)
		}
	}
}
