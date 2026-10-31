package escrow

import (
	"context"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/store"
)

func settledOnChainTxClient(log *callLog) *fakeTxClient {
	client := settlingTxClient()
	client.calls = log
	client.getEscrowFn = func(ctx context.Context, escrowID string) (chain.EscrowInfo, bool, error) {
		return chain.EscrowInfo{EscrowID: escrowID, Settled: true}, true, nil
	}
	return client
}

// Test flow:
//  1. Park an escrow the chain already reports settled.
//  2. Settle the parked rows once.
//  3. Assert nothing was finalized or broadcast, the row was dropped, and the pass reports no failure: a settled escrow has nothing left to settle.
func TestAParkedEscrowTheChainReportsSettledIsDroppedWithoutABroadcast(t *testing.T) {
	testStore := newFakeStore()
	row := parkedRowOfEpoch("1", ownEpochOutside)
	testStore.devshards[row.EscrowID] = row
	log := &callLog{}
	source := &fakeSettlementSource{calls: log}
	manager := &Manager{
		tx:               settledOnChainTxClient(log),
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: source,
		config:           holderWithSettlementEnabled(true),
		chainFacts:       &fakeTxClient{},
		now:              time.Now,
	}

	err := manager.settlePending(context.Background(), snapshotBeforeTheSwitch, []store.DevshardRecord{row})

	if err != nil {
		t.Fatalf("settlePending() = %v, want nil", err)
	}
	if log.contains("Finalize") || log.contains("SettleEscrow") {
		t.Fatal("a settled escrow was finalized or broadcast again")
	}
	if _, present := testStore.devshards[row.EscrowID]; present {
		t.Fatal("the settled escrow's row is still stored, want it dropped")
	}
}

// Test flow:
//  1. Store a serving escrow the chain reports present but settled.
//  2. Run the escrow check on it.
//  3. Assert it was taken out of routing and parked, so the next settle pass drops it.
func TestAnEscrowCheckRetiresAnEscrowTheChainReportsSettled(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["1"] = store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true}
	routing := &fakeSettlementSource{}
	manager := &Manager{tx: settledOnChainTxClient(nil), store: testStore, settlementSource: routing}

	if err := manager.TriggerEscrowCheck(context.Background(), "1"); err != nil {
		t.Fatalf("TriggerEscrowCheck() = %v, want nil", err)
	}

	row := testStore.devshards["1"]
	if row.Active || !row.SettlementPending {
		t.Fatalf("row active %v pending %v, want parked", row.Active, row.SettlementPending)
	}
	if !routing.retired {
		t.Fatal("the settled escrow still routes")
	}
}

// Test flow:
//  1. Hold no row for an escrow the chain still reports present but settled, as after this gateway's own settle dropped it.
//  2. Run the escrow check on it.
//  3. Assert the check succeeds: there is nothing left to park, and a failed check would schedule itself again every tick until the chain prunes the escrow.
func TestAnEscrowCheckOfASettledEscrowWithNoRowIsDone(t *testing.T) {
	manager := &Manager{tx: settledOnChainTxClient(nil), store: newFakeStore(), settlementSource: &fakeSettlementSource{}}

	if err := manager.TriggerEscrowCheck(context.Background(), "1"); err != nil {
		t.Fatalf("TriggerEscrowCheck() = %v, want nil for an escrow already dropped", err)
	}
}
