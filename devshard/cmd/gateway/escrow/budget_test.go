package escrow

import (
	"context"
	"testing"
	"time"

	"devshard/cmd/gateway/store"
)

// Test flow:
//  1. Table-driven: an active row, an inactive row the chain still holds, an inactive row gone from chain, and an active row still carrying the gone mark.
//  2. Assert goneFromChain is true only for the inactive row gone from chain.
func TestOnlyAnInactiveRowMarkedGoneIsGoneFromChain(t *testing.T) {
	testCases := []struct {
		name   string
		record store.DevshardRecord
		want   bool
	}{
		{name: "serving", record: store.DevshardRecord{Active: true}, want: false},
		{name: "deactivated by the operator", record: store.DevshardRecord{}, want: false},
		{name: "gone from chain", record: store.DevshardRecord{GoneFromChain: true}, want: true},
		{name: "reactivated after a mistaken not-found", record: store.DevshardRecord{Active: true, GoneFromChain: true}, want: false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := goneFromChain(testCase.record); got != testCase.want {
				t.Fatalf("goneFromChain(%s) = %v, want %v", testCase.name, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build the planning manager's one serving qwen escrow, then add an operator-deactivated row and a row gone from chain of the same model.
//  2. Read the model as the planner does.
//  3. Assert counted is 2: the serving row and the deactivated one; the gone row has nothing left to settle.
func TestThePlannerDoesNotCountARowGoneFromChain(t *testing.T) {
	manager, testStore, _, _ := planningManager(t)
	testStore.devshards["6"] = store.DevshardRecord{EscrowID: "6", Model: "qwen", RotationRole: roleRegular, RotationEpoch: 7}
	testStore.devshards["7"] = store.DevshardRecord{EscrowID: "7", Model: "qwen", RotationRole: roleRegular, RotationEpoch: 7, GoneFromChain: true}
	rows, err := testStore.ListDevshards(context.Background())
	if err != nil {
		t.Fatalf("ListDevshards() = %v, want nil", err)
	}
	configuration := manager.config.Load()
	models, err := parseModels(configuration.Rotation.ModelsJSON)
	if err != nil {
		t.Fatalf("parseModels() = %v, want nil", err)
	}

	reading := manager.readModel(models[0], rows, nil, planningSnapshot(), configuration, time.Now())

	if reading.state.Counted != 2 {
		t.Fatalf("readModel().state.Counted = %d, want 2: the serving row and the deactivated one", reading.state.Counted)
	}
}

// Test flow:
//  1. Build the planning manager's one serving qwen escrow, add a row gone from chain that still carries settlement_pending and a settle hash.
//  2. Read the model as the planner does.
//  3. Assert the gone row is neither parking nor counted, and is reported inactive.
func TestARowGoneFromChainIsNotParkingEvenWithSettlementPending(t *testing.T) {
	manager, testStore, _, _ := planningManager(t)
	testStore.devshards["7"] = store.DevshardRecord{EscrowID: "7", Model: "qwen", SettlementPending: true, SettleTxHash: "AB", GoneFromChain: true}
	rows, _ := testStore.ListDevshards(context.Background())
	configuration := manager.config.Load()
	models, _ := rotationModels(configuration.Rotation)

	reading := manager.readModel(models[0], rows, nil, planningSnapshot(), configuration, manager.now())

	if reading.state.Parking != 0 || reading.state.Counted != 1 || reading.report.Counts[CountParked] != 0 || reading.report.Counts[CountInactive] != 1 {
		t.Fatalf("readModel() parking = %d counted = %d counts = %v, want 0, 1 and the gone row inactive", reading.state.Parking, reading.state.Counted, reading.report.Counts)
	}
}

// Test flow:
//  1. Store, for model-a, a serving row, a parked row, an operator-deactivated row and a row gone from chain, plus one row of model-b and one commitment of model-a.
//  2. Assert unsettledCount for model-a is 4: everything the chain still holds, plus the commitment.
func TestTheUnsettledCountIsEverythingTheChainStillHolds(t *testing.T) {
	devshards := []store.DevshardRecord{
		{EscrowID: "1", Model: "model-a", Active: true},
		{EscrowID: "2", Model: "model-a", SettlementPending: true},
		{EscrowID: "3", Model: "model-a"},
		{EscrowID: "4", Model: "model-a", GoneFromChain: true},
		{EscrowID: "5", Model: "model-b", Active: true},
	}
	commitments := []store.Commitment{{TxHash: "TX", Model: "model-a"}}

	if got := unsettledCount("model-a", devshards, commitments); got != 4 {
		t.Fatalf("unsettledCount(model-a) = %d, want 4", got)
	}
}
