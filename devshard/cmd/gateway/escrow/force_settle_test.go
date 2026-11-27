package escrow

import (
	"context"
	"errors"
	"slices"
	"strings"
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
		narrator:         silentNarrator{},
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
//  2. Claim the record's settlement slot directly via manager.settling.inFlight.enter, simulating a settlement already in flight.
//  3. Call settle with force=true.
//  4. Assert settle returns ErrSettlementInFlight and never broadcasts "SettleEscrow".
func TestForcedSettleStillRefusesAConcurrentSettlement(t *testing.T) {
	record := store.DevshardRecord{EscrowID: "13", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	log := &callLog{}
	manager, _ := busyManager(t, record, log)
	leave, alreadyRunning := manager.settling.inFlight.enter(record.EscrowID)
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

// Test flow:
//  1. Build a busy manager whose settlement source reports nonce 4 unresolved on a forced finalize, and a recording narrator.
//  2. Call settle with force=true.
//  3. Assert the forced finalize ran once, the settle was broadcast, the narrator named escrow 14 with nonce 4 and the counter reads 1.
func TestAForcedSettleNarratesAndCountsTheNoncesItFinalizedPast(t *testing.T) {
	record := store.DevshardRecord{EscrowID: "14", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	log := &callLog{}
	manager, _ := busyManager(t, record, log)
	source := manager.settlementSource.(*fakeSettlementSource)
	source.unresolved = []uint64{4}
	narrator := &recordingLifecycleNarrator{}
	manager.narrator = narrator

	if _, err := manager.settle(context.Background(), record, true); err != nil {
		t.Fatalf("forced settle() = %v, want nil", err)
	}

	if source.forced != 1 || !log.contains("SettleEscrow") {
		t.Errorf("forced finalizes = %d, call log = %v, want one forced finalize and the broadcast", source.forced, log.snapshot())
	}
	if got := narrator.recorded(); !slices.Contains(got, "settled past unresolved 14 [4]") {
		t.Errorf("narration = %v, want the settle past unresolved nonce 4 of escrow 14", got)
	}
	if got := manager.SettledUnresolvedNonces(); got != 1 {
		t.Errorf("SettledUnresolvedNonces() = %d, want 1", got)
	}
}

// Test flow:
//  1. Build a manager whose settlement source is idle and reports a nonce unresolved on a forced finalize.
//  2. Call settle with force=false.
//  3. Assert the ordinary finalize ran, the forced one never did, nothing was narrated and the counter reads 0.
func TestAnUnforcedSettleNeverTakesTheForcedFinalize(t *testing.T) {
	record := store.DevshardRecord{EscrowID: "15", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	log := &callLog{}
	manager, _ := busyManager(t, record, log)
	source := manager.settlementSource.(*fakeSettlementSource)
	source.busy = false
	source.unresolved = []uint64{4}
	narrator := &recordingLifecycleNarrator{}
	manager.narrator = narrator

	if _, err := manager.settle(context.Background(), record, false); err != nil {
		t.Fatalf("settle() = %v, want nil", err)
	}

	if source.forced != 0 || !log.contains("Finalize") {
		t.Errorf("forced finalizes = %d, call log = %v, want only the ordinary finalize", source.forced, log.snapshot())
	}
	for _, line := range narrator.recorded() {
		if strings.HasPrefix(line, "settled past unresolved") {
			t.Errorf("narration = %v, want no settle past unresolved nonces", narrator.recorded())
		}
	}
	if got := manager.SettledUnresolvedNonces(); got != 0 {
		t.Errorf("SettledUnresolvedNonces() = %d, want 0", got)
	}
}

// Test flow:
//  1. Build a busy manager whose settlement source reports nonce 4 unresolved but fails the forced finalize, and a recording narrator.
//  2. Call settle with force=true.
//  3. Assert settle returns the failure, nothing was broadcast, no settle past unresolved nonces was narrated and the counter reads 0.
func TestAFailedForcedFinalizeCountsAndNarratesNoUnresolvedNonces(t *testing.T) {
	record := store.DevshardRecord{EscrowID: "16", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	log := &callLog{}
	manager, _ := busyManager(t, record, log)
	source := manager.settlementSource.(*fakeSettlementSource)
	source.unresolved = []uint64{4}
	source.finalizeErr = errors.New("hosts unreachable")
	narrator := &recordingLifecycleNarrator{}
	manager.narrator = narrator

	_, err := manager.settle(context.Background(), record, true)

	if !errors.Is(err, source.finalizeErr) {
		t.Fatalf("forced settle() = %v, want the finalize failure", err)
	}
	if log.contains("SettleEscrow") {
		t.Error("a failed finalize must not be broadcast")
	}
	for _, line := range narrator.recorded() {
		if strings.HasPrefix(line, "settled past unresolved") {
			t.Errorf("narration = %v, want no settle past unresolved nonces", narrator.recorded())
		}
	}
	if got := manager.SettledUnresolvedNonces(); got != 0 {
		t.Errorf("SettledUnresolvedNonces() = %d, want 0", got)
	}
}
