package main

import (
	"context"
	"errors"
	"log"

	"devshard/bridge"
	"devshard/user"
)

// The startup build skips an escrow and keeps it active in two cases: a group
// slot has no participant record on chain yet, or the build failed for a reason
// that is not final (a transient chain call, a busy SQLite file, a recovery
// step that did not finish). Such an escrow waits for its start here. The
// balance-check tick retries the build; a start registers the runtime, a final
// reason deactivates the escrow, anything else waits for the next tick.
//
// Ownership: the retry registers its runtime only while the escrow is still in
// the waiting set and still active in the store, and it checks both under g.mu
// in the same critical section that registers. Every path that takes an escrow
// away from the retry (settlement of a non-resident escrow, activation by the
// operator) removes it from the waiting set under g.mu first, so a runtime that
// the retry built in the meantime is dropped instead of registered.

func (g *Gateway) awaitSkippedEscrows(skipped []startupSkippedEscrow) {
	g.awaitingStartMu.Lock()
	defer g.awaitingStartMu.Unlock()
	for _, escrow := range skipped {
		switch escrow.Reason {
		case startupSkipReasonParticipantMissing, startupSkipReasonRuntimeBuildFailed:
		default:
			continue
		}
		if g.awaitingStart == nil {
			g.awaitingStart = make(map[string]string)
		}
		g.awaitingStart[escrow.EscrowID] = escrow.Reason
	}
}

// isAwaitingStart reports whether the escrow is active in the store but has no
// runtime yet. Rotation does not count such an escrow as serving.
func (g *Gateway) isAwaitingStart(escrowID string) bool {
	g.awaitingStartMu.Lock()
	defer g.awaitingStartMu.Unlock()
	_, awaiting := g.awaitingStart[escrowID]
	return awaiting
}

func (g *Gateway) stopAwaitingStart(escrowID string) {
	g.awaitingStartMu.Lock()
	defer g.awaitingStartMu.Unlock()
	delete(g.awaitingStart, escrowID)
}

// claimAwaitingStart takes a waiting escrow away from the retry for the
// duration of another operation on it. The returned function gives the escrow
// back with its reason, for the case where that operation fails and leaves the
// escrow active without a runtime; it does nothing for an escrow that was not
// waiting. The caller holds g.mu, so the claim and the retry's registration
// check cannot interleave.
func (g *Gateway) claimAwaitingStart(escrowID string) func() {
	g.awaitingStartMu.Lock()
	defer g.awaitingStartMu.Unlock()
	reason, waiting := g.awaitingStart[escrowID]
	if !waiting {
		return func() {}
	}
	delete(g.awaitingStart, escrowID)
	return func() {
		g.awaitingStartMu.Lock()
		defer g.awaitingStartMu.Unlock()
		if g.awaitingStart == nil {
			g.awaitingStart = make(map[string]string)
		}
		g.awaitingStart[escrowID] = reason
	}
}

func (g *Gateway) awaitingStartReasons() map[string]string {
	g.awaitingStartMu.Lock()
	defer g.awaitingStartMu.Unlock()
	reasons := make(map[string]string, len(g.awaitingStart))
	for escrowID, reason := range g.awaitingStart {
		reasons[escrowID] = reason
	}
	return reasons
}

func (g *Gateway) startRetrySkippedEscrows() {
	if !g.awaitingStartRetrying.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer g.awaitingStartRetrying.Store(false)
		g.retrySkippedEscrows(context.Background())
	}()
}

func (g *Gateway) retrySkippedEscrows(ctx context.Context) {
	reasons := g.awaitingStartReasons()
	if len(reasons) == 0 || g.store == nil {
		return
	}
	state, ok, err := g.store.LoadState(ctx)
	if err != nil || !ok {
		return
	}
	records := make(map[string]GatewayDevshardState, len(state.Devshards))
	for _, record := range state.Devshards {
		records[record.ID] = record
	}
	for escrowID, reason := range reasons {
		record, found := records[escrowID]
		if !found || !record.Active {
			g.stopAwaitingStart(escrowID)
			continue
		}
		runtime, buildErr := gatewayRuntimeBuilder(record.RuntimeConfig, g.runtimeBuildDepsFromSettings(g.perf, state.Settings))
		switch {
		case errors.Is(buildErr, bridge.ErrParticipantNotFound):
			continue
		case errors.Is(buildErr, user.ErrLocalStateUnrecoverable), errors.Is(buildErr, bridge.ErrEscrowNotFound),
			errors.Is(buildErr, bridge.ErrEscrowSettled), errors.Is(buildErr, errRuntimePrivateKeyMissing):
			log.Printf("devshard %s can no longer start, marking inactive: %v", escrowID, buildErr)
			if deactivateErr := g.store.SetDevshardActive(ctx, escrowID, false); deactivateErr != nil {
				log.Printf("devshard %s deactivate: %v", escrowID, deactivateErr)
				continue
			}
			g.stopAwaitingStart(escrowID)
			continue
		case buildErr != nil:
			log.Printf("devshard %s start retry failed (%s), will retry: %v", escrowID, reason, buildErr)
			continue
		}
		g.registerRetriedRuntime(ctx, escrowID, reason, runtime)
	}
}

// registerRetriedRuntime publishes a runtime the retry built, unless the escrow
// was taken away while the build ran: started by another path, settled,
// deactivated or removed. Checks and registration share one g.mu section.
func (g *Gateway) registerRetriedRuntime(ctx context.Context, escrowID, reason string, runtime *devshardRuntime) {
	shareChainMaxNonce([]*devshardRuntime{runtime}, g.chainMaxNonce())
	g.mu.Lock()
	if _, exists := g.runtimes[escrowID]; exists {
		g.mu.Unlock()
		runtime.close()
		g.stopAwaitingStart(escrowID)
		g.metrics.ClearStartupSkippedEscrow(escrowID)
		log.Printf("devshard %s started by another path while the retry was building, dropping the retried runtime", escrowID)
		return
	}
	if !g.isAwaitingStart(escrowID) {
		g.mu.Unlock()
		runtime.close()
		log.Printf("devshard %s was taken away while the retry was building, dropping the retried runtime", escrowID)
		return
	}
	record, found, err := g.store.GetDevshard(ctx, escrowID)
	if err != nil {
		g.mu.Unlock()
		runtime.close()
		log.Printf("devshard %s start retry: read record: %v", escrowID, err)
		return
	}
	if !found || !record.Active {
		g.stopAwaitingStart(escrowID)
		g.mu.Unlock()
		runtime.close()
		log.Printf("devshard %s was deactivated or removed while the retry was building, dropping the retried runtime", escrowID)
		return
	}
	restoreHoldFromRecord(runtime, record)
	g.runtimes[escrowID] = runtime
	g.runtimeOrder = append(g.runtimeOrder, runtime)
	g.attachRuntimeSharedState(runtime)
	g.sortRuntimeOrderLocked()
	g.stopAwaitingStart(escrowID)
	g.mu.Unlock()
	g.metrics.ClearStartupSkippedEscrow(escrowID)
	log.Printf("devshard %s runtime started after a startup skip (%s)", escrowID, reason)
}
