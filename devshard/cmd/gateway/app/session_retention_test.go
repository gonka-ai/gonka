package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/synctest"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/nonces"
	"devshard/cmd/gateway/registry"
)

type registeredEscrows []string

func (escrowIDs registeredEscrows) Snapshot() []registry.EscrowState {
	states := make([]registry.EscrowState, 0, len(escrowIDs))
	for _, escrowID := range escrowIDs {
		states = append(states, registry.EscrowState{ID: escrowID})
	}
	return states
}

func writeEscrowSession(t *testing.T, storageDir, escrowID, fileName string) {
	t.Helper()
	escrowDir := filepath.Join(storageDir, "escrow-"+escrowID)
	if err := os.MkdirAll(escrowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if fileName == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(escrowDir, fileName), []byte("session"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func remainingEscrows(t *testing.T, storageDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(storageDir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

// Test flow:
//  1. Lay out sessions of epochs 407, 408 and 410 and keep two previous epochs.
//  2. Prune at effective epoch 410.
//  3. Assert only epoch 407 left the disk: 408 and 410 sit inside the host horizon.
func TestSessionRetentionRemovesSessionsPastTheHostHorizon(t *testing.T) {
	storageDir := t.TempDir()
	writeEscrowSession(t, storageDir, "407", "epoch_407.db")
	writeEscrowSession(t, storageDir, "408", "epoch_408.db")
	writeEscrowSession(t, storageDir, "410", "epoch_410.db")
	retention := newSessionRetention(storageDir, 2, rowLister{}, registeredEscrows{})

	removed, err := retention.prune(context.Background(), 410)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}

	if !slices.Equal(removed, []string{"407"}) {
		t.Fatalf("removed = %v, want [407]", removed)
	}
	if got, want := remainingEscrows(t, storageDir), []string{"escrow-408", "escrow-410"}; !slices.Equal(got, want) {
		t.Fatalf("remaining = %v, want %v", got, want)
	}
}

// Test flow:
//  1. Build a retention configured to keep one previous epoch.
//  2. Assert it keeps two, the host horizon, as the floor.
//  3. Build one configured to keep none.
//  4. Assert there is no retention at all, and observing through it is a no-op.
func TestSessionRetentionFloorsAtTheHostHorizonAndZeroKeepsEverything(t *testing.T) {
	floored := newSessionRetention(t.TempDir(), 1, rowLister{}, registeredEscrows{})
	if floored.previousEpochs != 2 {
		t.Fatalf("previousEpochs = %d for a configured 1, want 2", floored.previousEpochs)
	}

	disabled := newSessionRetention(t.TempDir(), 0, rowLister{}, registeredEscrows{})
	if disabled != nil {
		t.Fatalf("retention configured 0 = %+v, want none", disabled)
	}
	disabled.observe(context.Background(), chain.PhaseSnapshot{EpochIndex: 410, EffectiveEpochIndex: 410})
}

// Test flow:
//  1. Lay out epoch-400 sessions: active, settle tx being reconciled, open in the registry, parked,
//     deactivated, and one whose row settlement already dropped.
//  2. Prune at effective epoch 410, past the epoch the chain still settles 400 in.
//  3. Assert the three still owned by serving or a reconciliation stay; the parked, deactivated and rowless ones go.
func TestSessionRetentionKeepsSessionsServingOrReconciliationStillOwns(t *testing.T) {
	storageDir := t.TempDir()
	for _, escrowID := range []string{"active", "broadcast", "open", "parked", "deactivated", "settled"} {
		writeEscrowSession(t, storageDir, escrowID, "epoch_400.db")
	}
	devshards := rowLister{
		{EscrowID: "active", Active: true},
		{EscrowID: "broadcast", SettlementPending: true, SettleTxHash: "ABCDEF"},
		{EscrowID: "open"},
		{EscrowID: "parked", SettlementPending: true},
		{EscrowID: "deactivated"},
	}
	retention := newSessionRetention(storageDir, 2, devshards, registeredEscrows{"open"})

	removed, err := retention.prune(context.Background(), 410)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}

	slices.Sort(removed)
	if want := []string{"deactivated", "parked", "settled"}; !slices.Equal(removed, want) {
		t.Fatalf("removed = %v, want %v", removed, want)
	}
	want := []string{"escrow-active", "escrow-broadcast", "escrow-open"}
	if got := remainingEscrows(t, storageDir); !slices.Equal(got, want) {
		t.Fatalf("remaining = %v, want %v", got, want)
	}
}

// Test flow:
//  1. Lay out an escrow directory with no session file, a stray file and a foreign directory beside the ledger.
//  2. Prune at effective epoch 410.
//  3. Assert nothing is removed: an epoch the disk does not name is never guessed.
func TestSessionRetentionLeavesWhatItCannotDate(t *testing.T) {
	storageDir := t.TempDir()
	writeEscrowSession(t, storageDir, "opening", "")
	writeEscrowSession(t, storageDir, "stray", "notes.txt")
	if err := os.MkdirAll(filepath.Join(storageDir, "captured-requests"), 0o755); err != nil {
		t.Fatal(err)
	}
	ledgerPath := nonces.DatabasePath(storageDir)
	if err := os.WriteFile(ledgerPath, []byte("ledger"), 0o600); err != nil {
		t.Fatal(err)
	}
	retention := newSessionRetention(storageDir, 2, rowLister{}, registeredEscrows{})

	removed, err := retention.prune(context.Background(), 410)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}

	if len(removed) != 0 {
		t.Fatalf("removed = %v, want nothing", removed)
	}
	want := []string{filepath.Base(ledgerPath), "captured-requests", "escrow-opening", "escrow-stray"}
	if got := remainingEscrows(t, storageDir); !slices.Equal(got, want) {
		t.Fatalf("remaining = %v, want %v", got, want)
	}
}

// Test flow:
//  1. Observe a snapshot in PoC, where the effective epoch trails the latest by one.
//  2. Assert the pass dated sessions by the effective epoch, keeping one the latest would have removed.
//  3. Lay out an older session and observe the same epoch again.
//  4. Assert no second pass ran for an epoch already pruned.
func TestSessionRetentionPrunesOncePerEffectiveEpoch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		storageDir := t.TempDir()
		writeEscrowSession(t, storageDir, "407", "epoch_407.db")
		writeEscrowSession(t, storageDir, "408", "epoch_408.db")
		retention := newSessionRetention(storageDir, 2, rowLister{}, registeredEscrows{})
		inPoC := chain.PhaseSnapshot{EpochIndex: 411, EffectiveEpochIndex: 410}

		retention.observe(context.Background(), inPoC)
		synctest.Wait()
		if got, want := remainingEscrows(t, storageDir), []string{"escrow-408"}; !slices.Equal(got, want) {
			t.Fatalf("after the first pass remaining = %v, want %v", got, want)
		}

		writeEscrowSession(t, storageDir, "406", "epoch_406.db")
		retention.observe(context.Background(), inPoC)
		synctest.Wait()
		if got, want := remainingEscrows(t, storageDir), []string{"escrow-406", "escrow-408"}; !slices.Equal(got, want) {
			t.Fatalf("after observing the same epoch remaining = %v, want %v", got, want)
		}
	})
}

// Test flow:
//  1. Lay out an unreadable epoch-400 session listed before a readable one.
//  2. Prune at effective epoch 410.
//  3. Assert the pass reports success and still removes the readable one: one bad directory does not hold back the rest.
func TestSessionRetentionCarriesOnPastADirectoryItCannotRead(t *testing.T) {
	storageDir := t.TempDir()
	writeEscrowSession(t, storageDir, "1-unreadable", "epoch_400.db")
	writeEscrowSession(t, storageDir, "2-readable", "epoch_400.db")
	unreadableDir := filepath.Join(storageDir, "escrow-1-unreadable")
	if err := os.Chmod(unreadableDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadableDir, 0o755) })
	retention := newSessionRetention(storageDir, 2, rowLister{}, registeredEscrows{})

	removed, err := retention.prune(context.Background(), 410)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}

	if !slices.Equal(removed, []string{"2-readable"}) {
		t.Fatalf("removed = %v, want [2-readable]", removed)
	}
}

// Test flow:
//  1. Observe a snapshot whose effective epoch is not known yet, only the latest.
//  2. Assert the pass dated sessions by the latest epoch.
func TestSessionRetentionFallsBackToTheLatestEpoch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		storageDir := t.TempDir()
		writeEscrowSession(t, storageDir, "407", "epoch_407.db")
		writeEscrowSession(t, storageDir, "408", "epoch_408.db")
		retention := newSessionRetention(storageDir, 2, rowLister{}, registeredEscrows{})

		retention.observe(context.Background(), chain.PhaseSnapshot{EpochIndex: 410})
		synctest.Wait()

		if got, want := remainingEscrows(t, storageDir), []string{"escrow-408"}; !slices.Equal(got, want) {
			t.Fatalf("remaining = %v, want %v", got, want)
		}
	})
}
