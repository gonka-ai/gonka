package escrow

import (
	"context"
	"errors"
	"testing"

	"devshard/cmd/gateway/store"
)

func busyManager(t *testing.T, record store.DevshardRecord, log *callLog) (*Manager, *fakeStore) {
	t.Helper()
	testStore := newFakeStore()
	testStore.devshards[record.EscrowID] = record
	testStore.calls = log
	txClient := settlingTxClient()
	txClient.calls = log
	txClient.settleTxHash = "SETTLE-TX"
	return &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{busy: true, calls: log},
	}, testStore
}

// Test flow:
//  1. Build a busy manager (settlementSource reports busy) with one active record.
//  2. Call settle with force=true.
//  3. Assert settle returns no error and "SettleEscrow" was broadcast.
//  4. Assert the stored record's SettlementPending is cleared.
func TestForcedSettleProceedsWithRequestsStillInFlight(t *testing.T) {
	record := store.DevshardRecord{EscrowID: "11", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	log := &callLog{}
	manager, testStore := busyManager(t, record, log)

	if _, err := manager.settle(context.Background(), record, true); err != nil {
		t.Fatalf("forced settle() = %v, want nil", err)
	}

	if !log.contains("SettleEscrow") {
		t.Errorf("call log = %v, want the broadcast to have happened", log.snapshot())
	}
	if settled := testStore.devshards[record.EscrowID]; settled.SettlementPending {
		t.Error("SettlementPending = true, want it cleared by the confirmed broadcast")
	}
}

// Test flow:
//  1. Build a busy manager with one active record.
//  2. Call settle with force=false.
//  3. Assert settle returns ErrDevshardBusy.
//  4. Assert "SettleEscrow" was never broadcast.
func TestUnforcedSettleStillDefersWhileBusy(t *testing.T) {
	record := store.DevshardRecord{EscrowID: "12", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	log := &callLog{}
	manager, _ := busyManager(t, record, log)

	_, err := manager.settle(context.Background(), record, false)

	if !errors.Is(err, ErrDevshardBusy) {
		t.Fatalf("settle() = %v, want ErrDevshardBusy", err)
	}
	if log.contains("SettleEscrow") {
		t.Error("a busy escrow must not be broadcast without force")
	}
}

// Test flow:
//  1. Build a busy manager with one active record.
//  2. Claim the record's settlement slot directly via manager.settlements.enter, simulating a settlement already in flight.
//  3. Call settle with force=true.
//  4. Assert settle returns ErrSettlementInFlight and never broadcasts "SettleEscrow".
func TestForcedSettleStillRefusesAConcurrentSettlement(t *testing.T) {
	record := store.DevshardRecord{EscrowID: "13", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	log := &callLog{}
	manager, _ := busyManager(t, record, log)
	leave, alreadyRunning := manager.settlements.enter(record.EscrowID)
	if alreadyRunning {
		t.Fatal("setup: the set must be free before the test claims it")
	}
	defer leave()

	_, err := manager.settle(context.Background(), record, true)

	if !errors.Is(err, ErrSettlementInFlight) {
		t.Fatalf("forced settle() = %v, want ErrSettlementInFlight", err)
	}
	if log.contains("SettleEscrow") {
		t.Error("force must not cross the double-broadcast guard")
	}
}
