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
		shareChainMaxNonce([]*devshardRuntime{runtime}, g.chainMaxNonce())
		g.mu.Lock()
		if _, exists := g.runtimes[escrowID]; exists {
			g.mu.Unlock()
			runtime.close()
			g.stopAwaitingStart(escrowID)
			continue
		}
		g.runtimes[escrowID] = runtime
		g.runtimeOrder = append(g.runtimeOrder, runtime)
		g.attachRuntimeSharedState(runtime)
		g.sortRuntimeOrderLocked()
		g.mu.Unlock()
		g.stopAwaitingStart(escrowID)
		g.metrics.ClearStartupSkippedEscrow(escrowID)
		log.Printf("devshard %s runtime started after a startup skip (%s)", escrowID, reason)
	}
}
