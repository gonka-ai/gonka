package main

import (
	"context"
	"errors"
	"log"

	"devshard/bridge"
)

func (g *Gateway) awaitParticipants(skipped []startupSkippedEscrow) {
	g.awaitingParticipantMu.Lock()
	defer g.awaitingParticipantMu.Unlock()
	for _, escrow := range skipped {
		if escrow.Reason != startupSkipReasonParticipantMissing {
			continue
		}
		if g.awaitingParticipant == nil {
			g.awaitingParticipant = make(map[string]struct{})
		}
		g.awaitingParticipant[escrow.EscrowID] = struct{}{}
	}
}

func (g *Gateway) isAwaitingParticipant(escrowID string) bool {
	g.awaitingParticipantMu.Lock()
	defer g.awaitingParticipantMu.Unlock()
	_, awaiting := g.awaitingParticipant[escrowID]
	return awaiting
}

func (g *Gateway) stopAwaitingParticipant(escrowID string) {
	g.awaitingParticipantMu.Lock()
	defer g.awaitingParticipantMu.Unlock()
	delete(g.awaitingParticipant, escrowID)
}

func (g *Gateway) awaitingParticipantIDs() []string {
	g.awaitingParticipantMu.Lock()
	defer g.awaitingParticipantMu.Unlock()
	escrowIDs := make([]string, 0, len(g.awaitingParticipant))
	for escrowID := range g.awaitingParticipant {
		escrowIDs = append(escrowIDs, escrowID)
	}
	return escrowIDs
}

func (g *Gateway) startRetryAwaitingParticipants() {
	if !g.awaitingParticipantRetrying.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer g.awaitingParticipantRetrying.Store(false)
		g.retryAwaitingParticipants(context.Background())
	}()
}

func (g *Gateway) retryAwaitingParticipants(ctx context.Context) {
	escrowIDs := g.awaitingParticipantIDs()
	if len(escrowIDs) == 0 || g.store == nil {
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
	for _, escrowID := range escrowIDs {
		record, found := records[escrowID]
		if !found || !record.Active {
			g.stopAwaitingParticipant(escrowID)
			continue
		}
		runtime, buildErr := gatewayRuntimeBuilder(record.RuntimeConfig, g.runtimeBuildDepsFromSettings(g.perf, state.Settings))
		switch {
		case errors.Is(buildErr, bridge.ErrParticipantNotFound):
			continue
		case errors.Is(buildErr, bridge.ErrEscrowNotFound), errors.Is(buildErr, bridge.ErrEscrowSettled), errors.Is(buildErr, errRuntimePrivateKeyMissing):
			log.Printf("devshard %s can no longer start, marking inactive: %v", escrowID, buildErr)
			if deactivateErr := g.store.SetDevshardActive(ctx, escrowID, false); deactivateErr != nil {
				log.Printf("devshard %s deactivate: %v", escrowID, deactivateErr)
				continue
			}
			g.stopAwaitingParticipant(escrowID)
			continue
		case buildErr != nil:
			log.Printf("devshard %s waiting for its participant record, start failed: %v", escrowID, buildErr)
			continue
		}
		shareChainMaxNonce([]*devshardRuntime{runtime}, g.chainMaxNonce())
		g.mu.Lock()
		if _, exists := g.runtimes[escrowID]; exists {
			g.mu.Unlock()
			runtime.close()
			g.stopAwaitingParticipant(escrowID)
			continue
		}
		g.runtimes[escrowID] = runtime
		g.runtimeOrder = append(g.runtimeOrder, runtime)
		g.attachRuntimeSharedState(runtime)
		g.sortRuntimeOrderLocked()
		g.mu.Unlock()
		g.stopAwaitingParticipant(escrowID)
		log.Printf("devshard %s participant record appeared, runtime started", escrowID)
	}
}
