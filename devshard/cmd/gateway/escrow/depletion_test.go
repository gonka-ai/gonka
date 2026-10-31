package escrow

import (
	"context"
	"maps"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
	"devshard/signing"
)

func workingCreateEscrowFn(newEscrowID uint64) func(context.Context, *signing.Secp256k1Signer, uint64, string, func(string) error) (chain.CreateEscrowResult, error) {
	return func(ctx context.Context, signer *signing.Secp256k1Signer, amount uint64, modelID string, onPrepared func(string) error) (chain.CreateEscrowResult, error) {
		if err := onPrepared("tx-" + modelID); err != nil {
			return chain.CreateEscrowResult{}, err
		}
		return chain.CreateEscrowResult{EscrowID: newEscrowID}, nil
	}
}

func activeRecord(id, model string) store.DevshardRecord {
	return store.DevshardRecord{EscrowID: id, Model: model, PrivateKeyEnv: "MODEL_A_KEY", Active: true, RotationRole: roleRegular}
}

// assertParked asserts a retired escrow's row survives, inactive and marked for a later settle.
func assertParked(t *testing.T, testStore *fakeStore, escrowID string) {
	t.Helper()
	record, ok := testStore.devshards[escrowID]
	if !ok {
		t.Fatalf("escrow %s is gone from the registry; its private_key_env is the only way to settle it", escrowID)
	}
	if record.Active {
		t.Errorf("escrow %s is still active after retirement", escrowID)
	}
	if !record.SettlementPending {
		t.Errorf("escrow %s is not marked settlement-pending, so nothing will ever settle it", escrowID)
	}
}

// Test flow:
//  1. Call `OnBalanceExhausted` for escrow "1" twice with the same reason, then once for escrow "2".
//  2. Assert `depleted.reasons` holds exactly one entry per escrow, deduping the repeated call.
func TestOnBalanceExhaustedMarksAndDedups(t *testing.T) {
	manager := &Manager{}
	manager.OnBalanceExhausted("1", "test")
	manager.OnBalanceExhausted("1", "test")
	manager.OnBalanceExhausted("2", "test")

	if len(manager.depleted.reasons) != 2 || manager.depleted.reasons["1"] != "test" || manager.depleted.reasons["2"] != "test" {
		t.Fatalf("depletedMarks = %v, want {1,2} deduped", manager.depleted.reasons)
	}
}

// Test flow:
//  1. Mark "escrow-1" twice and "escrow-2" once on a `markSet`.
//  2. Assert the first mark of "escrow-1" is new, the repeat is not, and "escrow-2" is new.
//  3. Drain the set and assert both escrows come out.
//  4. Assert "escrow-1" can be marked again after the drain.
func TestExhaustionIsAnnouncedOncePerTick(t *testing.T) {
	t.Parallel()
	var marks markSet

	first := marks.mark("escrow-1")
	second := marks.mark("escrow-1")
	other := marks.mark("escrow-2")

	if !first || second {
		t.Fatalf("mark returned %v then %v, want the first to be new and the second not", first, second)
	}
	if !other {
		t.Fatal("a different escrow was treated as already marked")
	}
	if drained := marks.drain(); len(drained) != 2 {
		t.Fatalf("drain returned %d escrows, want both", len(drained))
	}
	if marks.mark("escrow-1") != true {
		t.Fatal("a drained escrow was not announceable again on the next tick")
	}
}

type fakeExhaustionProbe map[string]scheduler.ExhaustionReason

func (probe fakeExhaustionProbe) Exhaustion(escrowID string) scheduler.ExhaustionReason {
	return probe[escrowID]
}

// Test flow:
//  1. List a serving regular below its floor, a reserve past its nonce cap, a serving escrow above its floor, and an inactive one below it.
//  2. Run `markSpent` over them.
//  3. Assert only the serving rows routing would retire are marked, each with the probe's reason: a parked row is already out.
func TestMarkSpentMarksOnlyServingEscrowsRoutingWouldRetire(t *testing.T) {
	parked := activeRecord("parked", "model-a")
	parked.Active = false
	reserve := activeRecord("reserve", "model-a")
	reserve.RotationRole = RoleReserve
	manager := &Manager{exhaustion: fakeExhaustionProbe{
		"spent":   scheduler.ExhaustionBalanceFloor,
		"reserve": scheduler.ExhaustionNonceCap,
		"parked":  scheduler.ExhaustionBalanceFloor,
	}}

	manager.markSpent([]store.DevshardRecord{activeRecord("spent", "model-a"), reserve, activeRecord("funded", "model-a"), parked})

	want := map[string]scheduler.ExhaustionReason{"spent": scheduler.ExhaustionBalanceFloor, "reserve": scheduler.ExhaustionNonceCap}
	if !maps.Equal(manager.depleted.reasons, want) {
		t.Fatalf("depleted = %v, want %v", manager.depleted.reasons, want)
	}
}

// Test flow:
//  1. Build a manager with no exhaustion probe wired.
//  2. Run `markSpent` over a serving escrow.
//  3. Assert nothing is marked and nothing panics.
func TestMarkSpentWithoutAProbeMarksNothing(t *testing.T) {
	manager := &Manager{}

	manager.markSpent([]store.DevshardRecord{activeRecord("spent", "model-a")})

	if len(manager.depleted.reasons) != 0 {
		t.Fatalf("depleted = %v, want nothing without a probe", manager.depleted.reasons)
	}
}
