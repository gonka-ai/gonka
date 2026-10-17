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
// Ownership: a retry reserves the escrow under g.mu before building, because
// session workers can already write its journal during the build. Admin builds
// hold g.mu throughout; non-resident settlements claim the escrow under g.mu
// for their whole operation, even after marking it inactive. Conflicting starts
// and settlements return busy before building. Registration rechecks that the
// escrow is still waiting and active, since an operator may deactivate it while
// the retry builds. A claimed escrow has no runtime and takes no rotation target.

func recordStartupSkippedEscrows(metrics *DevshardMetrics, skipped []startupSkippedEscrow) {
	for _, escrow := range skipped {
		metrics.RecordStartupSkippedEscrow(escrow.EscrowID, escrow.Model, escrow.Reason)
	}
}

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

// awaitsRetry reports whether the retry may register a runtime for the escrow:
// it waits and no settlement has claimed it.
func (g *Gateway) awaitsRetry(escrowID string) bool {
	g.awaitingStartMu.Lock()
	defer g.awaitingStartMu.Unlock()
	_, awaiting := g.awaitingStart[escrowID]
	return awaiting && g.startClaims[escrowID] == 0
}

func (g *Gateway) stopAwaitingStart(escrowID string) {
	g.awaitingStartMu.Lock()
	defer g.awaitingStartMu.Unlock()
	delete(g.awaitingStart, escrowID)
	g.metrics.ClearStartupSkippedEscrow(escrowID)
}

// Callers hold g.mu while deciding to build or claim a non-resident escrow.
func (g *Gateway) startIsBusy(escrowID string) bool {
	g.awaitingStartMu.Lock()
	defer g.awaitingStartMu.Unlock()
	return g.startBuilds[escrowID] || g.startClaims[escrowID] > 0
}

// Reserve before building: a build starts session workers and can write the
// journal, so discarding a duplicate after it returns is too late. Admin builds
// hold g.mu throughout; settlements reserve a claim under that same lock.
func (g *Gateway) reserveRetryBuild(escrowID string) (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, resident := g.runtimes[escrowID]; resident || !g.awaitsRetry(escrowID) || g.startIsBusy(escrowID) {
		return nil, false
	}
	g.awaitingStartMu.Lock()
	if g.startBuilds == nil {
		g.startBuilds = make(map[string]bool)
	}
	g.startBuilds[escrowID] = true
	g.awaitingStartMu.Unlock()
	return func() {
		g.awaitingStartMu.Lock()
		delete(g.startBuilds, escrowID)
		g.awaitingStartMu.Unlock()
	}, true
}

// claimEscrowForSettlement excludes starts for the full lifetime of a
// non-resident settlement. Ending the startup wait does not release the claim:
// the transient session may still be finalizing or broadcasting. The caller
// holds g.mu; the returned function releases ownership when the operation ends.
func (g *Gateway) claimEscrowForSettlement(escrowID string) func() {
	g.awaitingStartMu.Lock()
	defer g.awaitingStartMu.Unlock()
	if g.startClaims == nil {
		g.startClaims = make(map[string]int)
	}
	g.startClaims[escrowID]++
	return func() {
		g.awaitingStartMu.Lock()
		defer g.awaitingStartMu.Unlock()
		switch claims := g.startClaims[escrowID]; {
		case claims <= 1:
			delete(g.startClaims, escrowID)
		default:
			g.startClaims[escrowID] = claims - 1
		}
	}
}

// retryCandidates returns the waiting escrows that no settlement has claimed,
// with their skip reasons.
func (g *Gateway) retryCandidates() map[string]string {
	g.awaitingStartMu.Lock()
	defer g.awaitingStartMu.Unlock()
	reasons := make(map[string]string, len(g.awaitingStart))
	for escrowID, reason := range g.awaitingStart {
		if g.startClaims[escrowID] > 0 {
			continue
		}
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
	reasons := g.retryCandidates()
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
		g.retrySkippedEscrow(ctx, record, reason, state.Settings)
	}
}

func (g *Gateway) retrySkippedEscrow(ctx context.Context, record GatewayDevshardState, reason string, settings GatewaySettings) {
	leave, reserved := g.reserveRetryBuild(record.ID)
	if !reserved {
		return
	}
	defer leave()
	escrowID := record.ID
	runtime, buildErr := gatewayRuntimeBuilder(record.RuntimeConfig, g.runtimeBuildDepsFromSettings(g.perf, settings))
	switch {
	case errors.Is(buildErr, bridge.ErrParticipantNotFound):
		return
	case errors.Is(buildErr, user.ErrLocalStateUnrecoverable), errors.Is(buildErr, bridge.ErrEscrowNotFound),
		errors.Is(buildErr, bridge.ErrEscrowSettled), errors.Is(buildErr, errRuntimePrivateKeyMissing):
		g.deactivateFailedRetry(ctx, escrowID, buildErr)
		return
	case buildErr != nil:
		log.Printf("devshard %s start retry failed (%s), will retry: %v", escrowID, reason, buildErr)
		return
	}
	g.registerRetriedRuntime(ctx, escrowID, reason, runtime)
}

// A final error belongs to the build that just finished. Another path may have
// started or claimed the escrow in the meantime. Check ownership and persist
// deactivation under the same lock used by admin registration and settlement
// claims, just as registerRetriedRuntime does for a successful build.
func (g *Gateway) deactivateFailedRetry(ctx context.Context, escrowID string, buildErr error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.runtimes[escrowID]; exists {
		g.stopAwaitingStart(escrowID)
		return
	}
	if !g.awaitsRetry(escrowID) {
		return
	}
	log.Printf("devshard %s can no longer start, marking inactive: %v", escrowID, buildErr)
	if err := g.store.SetDevshardActive(ctx, escrowID, false); err != nil {
		log.Printf("devshard %s deactivate: %v", escrowID, err)
		return
	}
	g.stopAwaitingStart(escrowID)
}

// registerRetriedRuntime publishes a runtime the retry built, unless the escrow
// was taken away while the build ran: started by another path, claimed by a
// settlement, deactivated or removed. Checks and registration share one g.mu
// section.
func (g *Gateway) registerRetriedRuntime(ctx context.Context, escrowID, reason string, runtime *devshardRuntime) {
	shareChainMaxNonce([]*devshardRuntime{runtime}, g.chainMaxNonce())
	g.mu.Lock()
	if _, exists := g.runtimes[escrowID]; exists {
		g.stopAwaitingStart(escrowID)
		g.mu.Unlock()
		runtime.discard()
		log.Printf("devshard %s started by another path while the retry was building, dropping the retried runtime", escrowID)
		return
	}
	if !g.awaitsRetry(escrowID) {
		g.mu.Unlock()
		runtime.discard()
		log.Printf("devshard %s was taken away while the retry was building, dropping the retried runtime", escrowID)
		return
	}
	record, found, err := g.store.GetDevshard(ctx, escrowID)
	if err != nil {
		g.mu.Unlock()
		runtime.discard()
		log.Printf("devshard %s start retry: read record: %v", escrowID, err)
		return
	}
	if !found || !record.Active {
		g.stopAwaitingStart(escrowID)
		g.mu.Unlock()
		runtime.discard()
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
	log.Printf("devshard %s runtime started after a startup skip (%s)", escrowID, reason)
}
