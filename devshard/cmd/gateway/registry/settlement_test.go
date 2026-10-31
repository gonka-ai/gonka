package registry

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/types"
)

func settleableSession(nonce uint64) *fakeSession {
	session := newFakeSession("hostA", "hostB")
	session.nonce.Store(nonce)
	session.escrowState = types.EscrowState{
		EscrowID:                    "5",
		StateRootAndProtocolVersion: "v2",
		Balance:                     900,
		Fees:                        11,
		HostStats: map[uint32]*types.HostStats{
			2: {Missed: 1, Invalid: 0, Cost: 30, RequiredValidations: 3, CompletedValidations: 2},
			0: {Missed: 0, Invalid: 4, Cost: 10},
			1: nil,
		},
	}
	session.signatures = map[uint64]map[uint32][]byte{
		nonce: {2: []byte("sig-2"), 0: []byte("sig-0")},
	}
	return session
}

// Test flow:
//  1. Build a registry with separate serving and read-only session factories, neither holding a resident session for escrow 5.
//  2. Call `BuildSettlement` for escrow 5.
//  3. Assert it returns a settlement input for escrow 5 nonce 9.
//  4. Assert the read-only factory was called once and the serving factory was never called: building a payload needs neither chain nor hosts.
//  5. Assert the rehydrated read-only session was closed once.
func TestBuildSettlementRehydratesANonResidentEscrowReadOnly(t *testing.T) {
	t.Parallel()
	serving := newSessions(map[string]*fakeSession{"5": settleableSession(9)})
	readOnly := newSessions(map[string]*fakeSession{"5": settleableSession(9)})
	registry := New(Deps{ServingSessions: serving.open, ReadOnlySessions: readOnly.open, Now: fixedClock()})

	input, err := registry.BuildSettlement(context.Background(), "5")
	if err != nil {
		t.Fatalf("BuildSettlement = %v, want nil", err)
	}
	if input.EscrowID != 5 || input.Nonce != 9 {
		t.Errorf("settlement input = escrow %d nonce %d, want escrow 5 nonce 9", input.EscrowID, input.Nonce)
	}
	if got := readOnly.calls.Load(); got != 1 {
		t.Errorf("read-only factory calls = %d, want 1", got)
	}
	if got := serving.calls.Load(); got != 0 {
		t.Errorf("serving factory calls = %d, want 0: building a payload needs neither chain nor hosts", got)
	}
	if got := readOnly.byEscrow["5"].closeCalls.Load(); got != 1 {
		t.Errorf("Close calls on the rehydrated session = %d, want 1", got)
	}
}

// Test flow:
//  1. Build a registry with a resident serving session for escrow 5 at nonce 4, added to the registry, plus a separate read-only factory.
//  2. Call `BuildSettlement` for escrow 5.
//  3. Assert it returns the resident session's nonce 4.
//  4. Assert the read-only factory was never called and the resident session was never closed.
func TestBuildSettlementUsesTheResidentSessionWithoutRehydrating(t *testing.T) {
	t.Parallel()
	resident := settleableSession(4)
	serving := newSessions(map[string]*fakeSession{"5": resident})
	readOnly := newSessions(map[string]*fakeSession{"5": settleableSession(4)})
	registry := New(Deps{ServingSessions: serving.open, ReadOnlySessions: readOnly.open, Now: fixedClock()})
	mustAdd(t, registry, "5", "qwen")

	input, err := registry.BuildSettlement(context.Background(), "5")
	if err != nil {
		t.Fatalf("BuildSettlement = %v, want nil", err)
	}
	if input.Nonce != 4 {
		t.Errorf("settlement nonce = %d, want 4 (the resident session's)", input.Nonce)
	}
	if got := readOnly.calls.Load(); got != 0 {
		t.Errorf("read-only factory calls = %d, want 0", got)
	}
	if got := resident.closeCalls.Load(); got != 0 {
		t.Errorf("Close calls on the resident session = %d, want 0", got)
	}
}

// Test flow:
//  1. Build a registry around a settleable read-only session with host stats and signatures on slots 0, 1 (nil), and 2.
//  2. Call `BuildSettlement`.
//  3. Assert the host stats and slot signatures come back ordered by slot with nil entries dropped.
//  4. Assert fees, version, state root, and rest hash are all populated as expected.
func TestBuildSettlementOrdersHostStatsAndSignaturesBySlot(t *testing.T) {
	t.Parallel()
	registry := New(Deps{
		ReadOnlySessions: newSessions(map[string]*fakeSession{"5": settleableSession(9)}).open,
		Now:              fixedClock(),
	})

	input, err := registry.BuildSettlement(context.Background(), "5")
	if err != nil {
		t.Fatalf("BuildSettlement = %v, want nil", err)
	}
	wantStats := []chain.SettlementHostStat{
		{SlotID: 0, Invalid: 4, Cost: 10},
		{SlotID: 2, Missed: 1, Cost: 30, RequiredValidations: 3, CompletedValidations: 2},
	}
	if !reflect.DeepEqual(input.HostStats, wantStats) {
		t.Errorf("host stats = %+v, want %+v (slot order, nil entries dropped)", input.HostStats, wantStats)
	}
	wantSigs := []chain.SettlementSlotSig{
		{SlotID: 0, Signature: []byte("sig-0")},
		{SlotID: 2, Signature: []byte("sig-2")},
	}
	if !reflect.DeepEqual(input.SlotSigs, wantSigs) {
		t.Errorf("slot signatures = %+v, want %+v", input.SlotSigs, wantSigs)
	}
	if got, want := input.Fees, uint64(11); got != want {
		t.Errorf("fees = %d, want %d", got, want)
	}
	if got, want := input.Version, "v2"; got != want {
		t.Errorf("version = %q, want %q", got, want)
	}
	if len(input.StateRoot) == 0 || len(input.RestHash) == 0 {
		t.Errorf("state root (%d bytes) and rest hash (%d bytes) must both be populated", len(input.StateRoot), len(input.RestHash))
	}
}

// Test flow:
//  1. Build a registry around a read-only session keyed by a non-numeric escrow ID.
//  2. Call `BuildSettlement` for that ID.
//  3. Assert it returns an error: the chain message carries a numeric escrow id.
func TestBuildSettlementRejectsANonNumericEscrowID(t *testing.T) {
	t.Parallel()
	registry := New(Deps{
		ReadOnlySessions: newSessions(map[string]*fakeSession{"escrow-five": settleableSession(9)}).open,
		Now:              fixedClock(),
	})

	_, err := registry.BuildSettlement(context.Background(), "escrow-five")

	if err == nil {
		t.Fatal("BuildSettlement = nil, want an error: the chain message carries a numeric escrow id")
	}
}

// Test flow:
//  1. Build a registry with separate serving and read-only factories, neither holding a resident session for escrow 5, and share the chain's max_nonce.
//  2. Call `Finalize` for escrow 5.
//  3. Assert the serving factory was called once and the read-only factory was never called: finalizing collects host signatures, which a read-only rehydration cannot do.
//  4. Assert the rehydrated serving session's Finalize and Close were each called once.
func TestFinalizeRehydratesANonResidentEscrowWithAServingSession(t *testing.T) {
	t.Parallel()
	serving := newSessions(map[string]*fakeSession{"5": settleableSession(9)})
	readOnly := newSessions(map[string]*fakeSession{"5": settleableSession(9)})
	registry := New(Deps{ServingSessions: serving.open, ReadOnlySessions: readOnly.open, Now: fixedClock()})
	registry.ShareChainMaxNonce(1_700)

	if err := registry.Finalize(context.Background(), "5"); err != nil {
		t.Fatalf("Finalize = %v, want nil", err)
	}

	if got := serving.calls.Load(); got != 1 {
		t.Errorf("serving factory calls = %d, want 1", got)
	}
	if got := readOnly.calls.Load(); got != 0 {
		t.Errorf("read-only factory calls = %d, want 0: it has no host clients to finalize against", got)
	}
	rehydrated := serving.byEscrow["5"]
	if got := rehydrated.finalizeCalls.Load(); got != 1 {
		t.Errorf("Finalize calls on the rehydrated session = %d, want 1", got)
	}
	if got := rehydrated.closeCalls.Load(); got != 1 {
		t.Errorf("Close calls on the rehydrated session = %d, want 1", got)
	}
}

// Test flow:
//  1. Build a registry with a resident serving session for escrow 5, added to the registry, plus a separate read-only factory, and share the chain's max_nonce.
//  2. Call `Finalize` for escrow 5.
//  3. Assert the serving factory was called only for the Add and the read-only factory was never called.
//  4. Assert Finalize was called once on the resident session and it was never closed.
func TestFinalizeUsesTheResidentSessionWithoutRehydrating(t *testing.T) {
	t.Parallel()
	resident := settleableSession(9)
	serving := newSessions(map[string]*fakeSession{"5": resident})
	readOnly := newSessions(map[string]*fakeSession{"5": settleableSession(9)})
	registry := New(Deps{ServingSessions: serving.open, ReadOnlySessions: readOnly.open, Now: fixedClock()})
	mustAdd(t, registry, "5", "qwen")
	registry.ShareChainMaxNonce(1_700)

	if err := registry.Finalize(context.Background(), "5"); err != nil {
		t.Fatalf("Finalize = %v, want nil", err)
	}

	if got := serving.calls.Load(); got != 1 {
		t.Errorf("serving factory calls = %d, want 1 (the Add only)", got)
	}
	if got := readOnly.calls.Load(); got != 0 {
		t.Errorf("read-only factory calls = %d, want 0", got)
	}
	if got := resident.finalizeCalls.Load(); got != 1 {
		t.Errorf("Finalize calls = %d, want 1", got)
	}
	if got := resident.closeCalls.Load(); got != 0 {
		t.Errorf("Close calls on the resident session = %d, want 0", got)
	}
}

// Test flow:
//  1. Build a registry around a resident session already in the settlement phase, added to the registry, and share the chain's max_nonce.
//  2. Call `Finalize`.
//  3. Assert Finalize is still called once: only the session itself knows whether its quorum is held.
func TestFinalizeLetsAnEscrowAlreadyInSettlementCollectMissingSignatures(t *testing.T) {
	t.Parallel()
	resident := settleableSession(9)
	resident.setPhase(types.PhaseSettlement)
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"5": resident}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "5", "qwen")
	registry.ShareChainMaxNonce(1_700)

	if err := registry.Finalize(context.Background(), "5"); err != nil {
		t.Fatalf("Finalize = %v, want nil", err)
	}

	if got := resident.finalizeCalls.Load(); got != 1 {
		t.Errorf("Finalize calls on an escrow already in settlement = %d, want 1: only the session knows whether its quorum is held", got)
	}
}

// Test flow:
//  1. Build a registry with one escrow and share the chain's max_nonce, acquire a request, then retire the escrow so it starts draining.
//  2. Arm the session's onFinalize hook to release the request and wait for the registry to finish closing before recording the close-call count.
//  3. Call `Finalize`.
//  4. Assert the session had not yet been closed while Finalize was running, and was closed exactly once afterward: the drain closed the store under the settlement, not before or during it.
func TestADrainedEscrowClosesOnlyAfterTheFinalizeRunningOnIt(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	registry.ShareChainMaxNonce(1_700)
	_, releaseRequest, acquired := registry.Acquire("1")
	if !acquired {
		t.Fatal("Acquire(1) = false, want true")
	}
	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(1) = %v, want nil", err)
	}
	var closedDuringFinalize int64
	session.onFinalize = func() {
		releaseRequest()
		registry.closing.Wait()
		closedDuringFinalize = session.closeCalls.Load()
	}

	if err := registry.Finalize(context.Background(), "1"); err != nil {
		t.Fatalf("Finalize(1) = %v, want nil", err)
	}
	registry.closing.Wait()

	if closedDuringFinalize != 0 {
		t.Fatalf("Close calls while Finalize ran = %d, want 0: the drain closed the store under the settlement", closedDuringFinalize)
	}
	if got := session.closeCalls.Load(); got != 1 {
		t.Fatalf("Close calls after Finalize = %d, want 1", got)
	}
}

// Test flow:
//  1. Build a registry with one escrow and hold it for settlement.
//  2. Assert the escrow does not report as busy under the hold.
//  3. Assert its routable candidate reports zero active users: routing must not price a read as traffic.
func TestASettlementHoldIsNotARequest(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")

	_, release, held := registry.HoldSettlement("1")
	if !held {
		t.Fatal("HoldSettlement(1) = false, want true")
	}
	defer release()

	if registry.IsBusy("1") {
		t.Error("IsBusy(1) under a settlement hold = true: a finalize would refuse itself as busy")
	}
	if candidate, _ := registry.Routable("1"); candidate.ActiveUsers != 0 {
		t.Errorf("ActiveUsers under a settlement hold = %d, want 0: routing must not price a read as traffic", candidate.ActiveUsers)
	}
}

// Test flow:
//  1. Build a registry with one escrow, hold it for settlement, then retire it.
//  2. Assert the session was not closed while the hold was held.
//  3. Release the hold and wait for the registry to finish closing.
//  4. Assert the session was closed exactly once afterward.
func TestRetiringAnEscrowUnderASettlementHoldClosesItOnlyAfterTheHold(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	_, release, held := registry.HoldSettlement("1")
	if !held {
		t.Fatal("HoldSettlement(1) = false, want true")
	}

	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(1) = %v, want nil", err)
	}
	closedUnderTheHold := session.closeCalls.Load()
	release()
	registry.closing.Wait()

	if closedUnderTheHold != 0 {
		t.Fatalf("Close calls under the settlement hold = %d, want 0", closedUnderTheHold)
	}
	if got := session.closeCalls.Load(); got != 1 {
		t.Fatalf("Close calls after the hold = %d, want 1", got)
	}
}

// Test flow:
//  1. Build a registry with one escrow, acquire and then retire it, and arm the session's onFlush hook to block until signaled.
//  2. Release the acquired request so the drain starts flushing, then attempt to hold the escrow for settlement while the flush is blocked.
//  3. Let the flush finish and wait for the close to complete.
//  4. Assert the hold was refused: a hold on a closing session must not outlive its store.
//  5. Assert the session was closed exactly once.
func TestASettlementHoldIsRefusedOnceTheCloseHasStarted(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	_, releaseRequest, acquired := registry.Acquire("1")
	if !acquired {
		t.Fatal("Acquire(1) = false, want true")
	}
	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(1) = %v, want nil", err)
	}
	flushing, finishFlush := make(chan struct{}), make(chan struct{})
	session.onFlush = func() {
		close(flushing)
		<-finishFlush
	}

	releaseRequest()
	<-flushing
	_, releaseHold, held := registry.HoldSettlement("1")
	close(finishFlush)
	registry.closing.Wait()

	if held {
		releaseHold()
		registry.closing.Wait()
		t.Fatal("HoldSettlement(1) during the close = true, want false: a hold on a closing session outlives its store")
	}
	if got := session.closeCalls.Load(); got != 1 {
		t.Fatalf("Close calls = %d, want exactly 1", got)
	}
}

// Test flow:
//  1. Build a registry around a resident session whose Finalize call always fails, added to the registry, and share the chain's max_nonce.
//  2. Call `Finalize`.
//  3. Assert the returned error wraps the resident session's failure.
func TestFinalizeReportsTheSessionFailure(t *testing.T) {
	t.Parallel()
	resident := settleableSession(9)
	resident.finalizeErr = errors.New("quorum not reached")
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"5": resident}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "5", "qwen")
	registry.ShareChainMaxNonce(1_700)

	err := registry.Finalize(context.Background(), "5")

	if !errors.Is(err, resident.finalizeErr) {
		t.Errorf("Finalize = %v, want it to wrap %v", err, resident.finalizeErr)
	}
}

// Test flow:
//  1. Build a registry with neither a serving nor a read-only session factory configured.
//  2. Call `Finalize` and assert it returns an error.
//  3. Call `BuildSettlement` and assert it also returns an error.
func TestSettlementPathsReportAMissingFactory(t *testing.T) {
	t.Parallel()
	registry := New(Deps{Now: fixedClock()})

	if err := registry.Finalize(context.Background(), "5"); err == nil {
		t.Error("Finalize without a serving factory = nil, want an error")
	}
	if _, err := registry.BuildSettlement(context.Background(), "5"); err == nil {
		t.Error("BuildSettlement without a read-only factory = nil, want an error")
	}
}

// Test flow:
//  1. Table-driven: a resident escrow and a non-resident one, each with a Retiring observer that records the finalize calls its session had when it read, and the chain's max_nonce shared.
//  2. Finalize the escrow.
//  3. Assert the observer read it once, after its finalize.
func TestTheLedgerReadsAnEscrowAfterItsFinalize(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		resident bool
	}{
		{name: "resident", resident: true},
		{name: "rehydrated"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			session := settleableSession(9)
			var finalizesWhenRead []int64
			registry := New(Deps{
				ServingSessions: newSessions(map[string]*fakeSession{"5": session}).open,
				Retiring: func(_ string, retiring EscrowSession) {
					finalizesWhenRead = append(finalizesWhenRead, session.finalizeCalls.Load())
				},
				Now: fixedClock(),
			})
			registry.ShareChainMaxNonce(1_700)
			if testCase.resident {
				mustAdd(t, registry, "5", "qwen")
			}

			if err := registry.Finalize(context.Background(), "5"); err != nil {
				t.Fatalf("Finalize = %v, want nil", err)
			}

			if !slices.Equal(finalizesWhenRead, []int64{1}) {
				t.Fatalf("finalize calls when the ledger read = %v, want [1]", finalizesWhenRead)
			}
		})
	}
}

// Test flow:
//  1. Build a registry around a settleable session whose settlement signatures, checked and spread over each validator's slots, differ from its raw signature map; then one whose signatures fall short of quorum.
//  2. Call `BuildSettlement` for each.
//  3. Assert the payload carries the checked signatures, and short of a checked quorum it carries every signature held: a read-only session cannot resolve a warm key the chain still accepts.
//  4. Assert a payload short of quorum is narrated unverifiable once, however many checks found it short.
func TestBuildSettlementSignsWithTheCheckedSettlementSignatures(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name           string
		checked        map[uint32][]byte
		shortfall      error
		wantSigs       []chain.SettlementSlotSig
		wantNarrations int
	}{
		{name: "checked signatures", checked: map[uint32][]byte{1: []byte("checked-1"), 0: []byte("checked-0")},
			wantSigs: []chain.SettlementSlotSig{{SlotID: 0, Signature: []byte("checked-0")}, {SlotID: 1, Signature: []byte("checked-1")}}},
		{name: "short of quorum", shortfall: errors.New("insufficient signatures: 1/2 weight"),
			wantSigs:       []chain.SettlementSlotSig{{SlotID: 0, Signature: []byte("sig-0")}, {SlotID: 2, Signature: []byte("sig-2")}},
			wantNarrations: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			session := settleableSession(9)
			session.settlementSignatures, session.settlementShortfall = testCase.checked, testCase.shortfall
			narrator := &recordingEscrowNarrator{}
			registry := New(Deps{ReadOnlySessions: newSessions(map[string]*fakeSession{"5": session}).open, Narrator: narrator, Now: fixedClock()})

			input, err := registry.BuildSettlement(context.Background(), "5")

			if err != nil {
				t.Fatalf("BuildSettlement = %v, want nil", err)
			}
			if !reflect.DeepEqual(input.SlotSigs, testCase.wantSigs) {
				t.Fatalf("slot signatures = %v, want %v", input.SlotSigs, testCase.wantSigs)
			}
			if recorded := narrator.recorded(); len(recorded) != testCase.wantNarrations {
				t.Fatalf("narrated %v, want %d unverifiable lines", recorded, testCase.wantNarrations)
			}
		})
	}
}
