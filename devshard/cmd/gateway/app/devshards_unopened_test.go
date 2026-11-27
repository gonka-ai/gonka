package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"devshard/bridge"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/metrics"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/store"
	"devshard/user"
)

// Test flow:
//  1. Store five active rows, one per model, and a session factory that fails them with an unrecoverable local state, a text carrying a path, an unreachable chain, a missing key and a draining predecessor.
//  2. Publish and assert the unopened list holds all five with their models and reason classes, and that no reason is the error text.
//  3. Open the first and deactivate the other four in the store, publish again, and assert the list is empty.
func TestAnEscrowThatWillNotOpenIsListedWithItsReasonClassUntilItOpensOrIsDeactivated(t *testing.T) {
	ctx := context.Background()
	records := openedStore(t)
	for escrowID, model := range map[string]string{"escrow-1": "model-a", "escrow-2": "model-b", "escrow-3": "model-c", "escrow-4": "model-d", "escrow-5": "model-e"} {
		if err := records.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: escrowID, Model: model, PrivateKeyEnv: "DEVSHARD_UNOPENED_KEY", Active: true}); err != nil {
			t.Fatalf("UpsertDevshard(%s): %v", escrowID, err)
		}
	}
	firstOpens := false
	escrows := registry.New(registry.Deps{
		ServingSessions: func(_ context.Context, escrowID string) (registry.EscrowSession, error) {
			switch {
			case escrowID == "escrow-1" && !firstOpens:
				return nil, fmt.Errorf("replaying: %w", user.ErrLocalStateUnrecoverable)
			case escrowID == "escrow-2":
				return nil, errors.New("open /var/lib/gonka/escrow-2/session.db: input/output error")
			case escrowID == "escrow-3":
				return nil, fmt.Errorf("get escrow: %w", bridge.ErrChainUnavailable)
			case escrowID == "escrow-4":
				return nil, fmt.Errorf("session key: %w", env.ErrPrivateKeyMissing)
			case escrowID == "escrow-5":
				return nil, fmt.Errorf("escrow %s: %w", escrowID, registry.ErrDraining)
			}
			return weightlessSession{participants: []string{"validator-a"}}, nil
		},
		Now: time.Now,
	})
	t.Cleanup(func() { escrows.Close() })
	composed := &gateway{store: records, escrows: escrows, builders: 1, unopened: &unopenedEscrows{}, events: newTestJournal(t)}

	if unopened, err := composed.publishEscrows(ctx); err != nil || unopened != 5 {
		t.Fatalf("publishEscrows() = %d, %v, want 5, nil", unopened, err)
	}
	want := []metrics.UnopenedEscrow{
		{EscrowID: "escrow-1", Model: "model-a", Reason: metrics.UnopenedLocalState},
		{EscrowID: "escrow-2", Model: "model-b", Reason: metrics.UnopenedOther},
		{EscrowID: "escrow-3", Model: "model-c", Reason: metrics.UnopenedChain},
		{EscrowID: "escrow-4", Model: "model-d", Reason: metrics.UnopenedKeyMissing},
		{EscrowID: "escrow-5", Model: "model-e", Reason: metrics.UnopenedDraining},
	}
	listed := composed.unopened.List()
	slices.SortFunc(listed, func(a, b metrics.UnopenedEscrow) int { return strings.Compare(a.EscrowID, b.EscrowID) })
	if !slices.Equal(listed, want) {
		t.Fatalf("unopened after the first publish = %v, want %v", listed, want)
	}

	firstOpens = true
	for _, escrowID := range []string{"escrow-2", "escrow-3", "escrow-4", "escrow-5"} {
		if err := records.SetDevshardActive(ctx, escrowID, false); err != nil {
			t.Fatalf("SetDevshardActive(%s): %v", escrowID, err)
		}
	}
	if unopened, err := composed.publishEscrows(ctx); err != nil || unopened != 0 {
		t.Fatalf("publishEscrows() = %d, %v, want 0, nil", unopened, err)
	}
	if listed := composed.unopened.List(); len(listed) != 0 {
		t.Fatalf("unopened after the escrow opened and the others were deactivated = %v, want none", listed)
	}
}
