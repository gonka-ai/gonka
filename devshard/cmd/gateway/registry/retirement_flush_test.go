package registry

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"devshard/types"
)

func sessionHoldingGossip(pending int) *fakeSession {
	session := newFakeSession("hostA")
	for range pending {
		session.pendingTxs = append(session.pendingTxs, &types.DevshardTx{})
	}
	return session
}

// Test flow:
//  1. Build a registry with a session holding no pending gossip, and add one escrow.
//  2. Retire that escrow.
//  3. Assert no pending-diff call was sent.
//  4. Assert the narrator recorded nothing about a flush.
func TestAnEscrowWithNothingPendingRetiresWithoutADiff(t *testing.T) {
	t.Parallel()
	session := sessionHoldingGossip(0)
	narrator := &recordingEscrowNarrator{}
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Membership:      newRecordingMembership(),
		Narrator:        narrator,
		Now:             fixedClock(),
	})
	t.Cleanup(func() { registry.Close() })
	if err := registry.Add(context.Background(), "1", "model-a"); err != nil {
		t.Fatalf("Add(): %v", err)
	}

	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(): %v", err)
	}

	if calls := session.pendingDiffCalls.Load(); calls != 0 {
		t.Fatalf("pending diffs sent = %d, want none for an escrow holding nothing", calls)
	}
	if slices.ContainsFunc(narrator.recorded(), func(line string) bool { return strings.HasPrefix(line, "flushed") }) {
		t.Fatalf("narrated %v, want nothing said about a diff that was never sent", narrator.recorded())
	}
}

// Test flow:
//  1. Build a registry with a session holding 3 pending gossip transactions, recording the order in which the diff send and the ledger's Retiring hook fire, and add one escrow.
//  2. Retire that escrow.
//  3. Assert exactly one pending-diff call was sent.
//  4. Assert the diff fired before the ledger read.
//  5. Assert the narrator recorded "flushed 1 pending 3 error <nil>".
func TestARetiringEscrowCarriesItsGossipBeforeTheLedgerReadsIt(t *testing.T) {
	t.Parallel()
	session := sessionHoldingGossip(3)
	var order []string
	session.onPendingDiff = func() { order = append(order, "diff") }
	narrator := &recordingEscrowNarrator{}
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Membership:      newRecordingMembership(),
		Narrator:        narrator,
		Retiring:        func(string, EscrowSession) { order = append(order, "ledger") },
		Now:             fixedClock(),
	})
	t.Cleanup(func() { registry.Close() })
	if err := registry.Add(context.Background(), "1", "model-a"); err != nil {
		t.Fatalf("Add(): %v", err)
	}

	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(): %v", err)
	}

	if calls := session.pendingDiffCalls.Load(); calls != 1 {
		t.Fatalf("pending diffs sent = %d, want exactly one last diff", calls)
	}
	assertSameOrder(t, order, []string{"diff", "ledger"})
	assertNarrated(t, narrator, "flushed 1 pending 3 error <nil>")
}

// Test flow:
//  1. Build a registry with a session holding 2 pending gossip transactions whose diff send always fails, and add one escrow.
//  2. Retire that escrow.
//  3. Assert the session still closed once, despite the failed diff.
//  4. Assert the narrator recorded "flushed 1 pending 2 error host unreachable".
func TestAFailedLastDiffIsNarratedAndTheEscrowStillCloses(t *testing.T) {
	t.Parallel()
	session := sessionHoldingGossip(2)
	session.pendingDiffErr = errors.New("host unreachable")
	narrator := &recordingEscrowNarrator{}
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Membership:      newRecordingMembership(),
		Narrator:        narrator,
		Now:             fixedClock(),
	})
	t.Cleanup(func() { registry.Close() })
	if err := registry.Add(context.Background(), "1", "model-a"); err != nil {
		t.Fatalf("Add(): %v", err)
	}

	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(): %v", err)
	}

	if closes := session.closeCalls.Load(); closes != 1 {
		t.Fatalf("session closes = %d, want the retirement to finish despite the failed diff", closes)
	}
	assertNarrated(t, narrator, "flushed 1 pending 2 error host unreachable")
}

func assertSameOrder(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func assertNarrated(t *testing.T, narrator *recordingEscrowNarrator, want string) {
	t.Helper()
	if !slices.Contains(narrator.recorded(), want) {
		t.Fatalf("narrated %v, want %q among them", narrator.recorded(), want)
	}
}
