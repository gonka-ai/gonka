package registry

import (
	"testing"

	"devshard/cmd/gateway/scheduler"
	"devshard/types"
)

// Test flow:
//  1. Build a registry with two active escrows, hold a request in flight on each, then retire one at its nonce cap and the other plainly.
//  2. Assert only the nonce-capped one is found for an escalation and its escalation hold admits, while the plainly retired one's refuses.
//  3. Release every hold and assert the nonce-capped escrow closes and is no longer found once its races are gone.
func TestOnlyAnEscrowRetiredAtItsNonceCapTakesItsRacesEscalations(t *testing.T) {
	t.Parallel()
	capped, plain := newFakeSession("hostA"), newFakeSession("hostB")
	capped.setPhase(types.PhaseActive)
	plain.setPhase(types.PhaseActive)
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": capped, "2": plain}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	mustAdd(t, registry, "2", "qwen")
	candidates := map[string]scheduler.Escrow{}
	for _, candidate := range registry.Candidates("qwen") {
		candidates[candidate.ID] = candidate
	}
	_, releaseCapped, _ := registry.AcquireCommitted("1")
	_, releasePlain, _ := registry.AcquireCommitted("2")
	if err := registry.RetireAtNonceCap("1"); err != nil {
		t.Fatalf("RetireAtNonceCap(1) = %v", err)
	}
	if err := registry.Retire("2"); err != nil {
		t.Fatalf("Retire(2) = %v", err)
	}

	retired, found := registry.RetiredAtNonceCap("1")
	if !found || retired.ID != "1" {
		t.Fatalf("RetiredAtNonceCap(1) = %v, %t, want the draining escrow", retired.ID, found)
	}
	if _, found := registry.RetiredAtNonceCap("2"); found {
		t.Fatal("RetiredAtNonceCap(2) found an escrow retired for another reason")
	}
	releaseEscalation, held := retired.EscalationHold()
	if !held {
		t.Fatal("the nonce-capped escrow's escalation hold refused while its race still holds it")
	}
	if _, held := candidates["2"].EscalationHold(); held {
		t.Fatal("the plainly retired escrow's escalation hold admitted")
	}
	if _, held := candidates["1"].Hold(); held {
		t.Fatal("the nonce-capped escrow's live hold admitted after retirement")
	}

	releaseEscalation()
	releaseCapped()
	releasePlain()
	awaitDrainClose(t, registry, func() bool { return capped.closeCalls.Load() == 1 })
	if _, found := registry.RetiredAtNonceCap("1"); found {
		t.Fatal("RetiredAtNonceCap(1) found an escrow no race holds any more")
	}
	if _, held := candidates["1"].EscalationHold(); held {
		t.Fatal("the escalation hold admitted a closed escrow")
	}
}
