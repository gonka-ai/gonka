package registry

import (
	"context"
	"reflect"
	"testing"

	"devshard/state"
	"devshard/types"
)

// Test flow:
//  1. Build a settlement payload signed with a bogus signature for a group of three validators.
//  2. Check `settlementUnverifiable` against that payload and the snapshot.
//  3. Assert it reports the settlement as unverifiable.
func TestASettlementThatWouldNotVerifyIsReportedNotEnforced(t *testing.T) {
	t.Parallel()
	group := []types.SlotAssignment{
		{SlotID: 0, ValidatorAddress: "gonka1aaa"},
		{SlotID: 1, ValidatorAddress: "gonka1bbb"},
		{SlotID: 2, ValidatorAddress: "gonka1ccc"},
	}
	snapshot := types.EscrowState{
		EscrowID:                    "5",
		StateRootAndProtocolVersion: "v2",
		Group:                       group,
		HostStats:                   map[uint32]*types.HostStats{0: {Cost: 10}},
	}
	payload, err := state.BuildSettlement("5", snapshot, map[uint32][]byte{0: []byte("not-a-signature")}, 9)
	if err != nil {
		t.Fatalf("BuildSettlement: %v", err)
	}

	unverifiable := settlementUnverifiable(*payload, snapshot)
	if unverifiable == nil {
		t.Fatal("a settlement signed by nobody the group knows must be reported")
	}
}

// Test flow:
//  1. Build a settlement payload for a snapshot with no group.
//  2. Check `settlementUnverifiable` against that payload and the snapshot.
//  3. Assert it reports no verdict for a snapshot with no group.
func TestAnAbsentGroupIsNotReported(t *testing.T) {
	t.Parallel()
	snapshot := types.EscrowState{EscrowID: "5", StateRootAndProtocolVersion: "v2"}
	payload, err := state.BuildSettlement("5", snapshot, nil, 9)
	if err != nil {
		t.Fatalf("BuildSettlement: %v", err)
	}

	if unverifiable := settlementUnverifiable(*payload, snapshot); unverifiable != nil {
		t.Errorf("verdict = %v, want none for a snapshot with no group", unverifiable)
	}
}

// Test flow:
//  1. Build a registry around a settleable session at nonce 9 with a three-validator group.
//  2. Call `registry.BuildSettlement`.
//  3. Assert it succeeds with no error and returns the payload with nonce 9: an unverifiable settlement is reported, not refused.
func TestBuildSettlementStillAnswersWhenTheSignaturesDoNotVerify(t *testing.T) {
	t.Parallel()
	session := settleableSession(9)
	session.escrowState.Group = []types.SlotAssignment{
		{SlotID: 0, ValidatorAddress: "gonka1aaa"},
		{SlotID: 1, ValidatorAddress: "gonka1bbb"},
		{SlotID: 2, ValidatorAddress: "gonka1ccc"},
	}
	registry := New(Deps{
		ReadOnlySessions: newSessions(map[string]*fakeSession{"5": session}).open,
		Now:              fixedClock(),
	})

	input, err := registry.BuildSettlement(context.Background(), "5")

	if err != nil {
		t.Fatalf("BuildSettlement = %v, want nil: an unverifiable settlement is reported, not refused", err)
	}
	if input.Nonce != 9 {
		t.Errorf("nonce = %d, want the payload to be built anyway", input.Nonce)
	}
}

// Test flow:
//  1. Build a registry around a settleable session at nonce 9 with a three-validator group, wired to a recording narrator.
//  2. Call `registry.BuildSettlement`.
//  3. Assert it succeeds with no error.
//  4. Assert the narrator recorded "unverifiable 5 nonce 9".
func TestBuildSettlementReportsASettlementThatWouldNotVerify(t *testing.T) {
	t.Parallel()
	session := settleableSession(9)
	session.escrowState.Group = []types.SlotAssignment{
		{SlotID: 0, ValidatorAddress: "gonka1aaa"},
		{SlotID: 1, ValidatorAddress: "gonka1bbb"},
		{SlotID: 2, ValidatorAddress: "gonka1ccc"},
	}
	narrator := &recordingEscrowNarrator{}
	registry := New(Deps{
		ReadOnlySessions: newSessions(map[string]*fakeSession{"5": session}).open,
		Narrator:         narrator,
		Now:              fixedClock(),
	})

	if _, err := registry.BuildSettlement(context.Background(), "5"); err != nil {
		t.Fatalf("BuildSettlement = %v, want nil", err)
	}

	if got := narrator.recorded(); !reflect.DeepEqual(got, []string{"unverifiable 5 nonce 9"}) {
		t.Errorf("the payload left without being checked: %v", got)
	}
}
