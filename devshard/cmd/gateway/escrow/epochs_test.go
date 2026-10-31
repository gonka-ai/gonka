package escrow

import (
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/store"
)

// Test flow:
//  1. Table-driven: a snapshot before PoC, inside PoC, after the switch, and one whose effective epoch is unknown.
//  2. Assert effectiveEpoch reads the snapshot's effective epoch, or the latest when it is unknown, and bridgeLabel is one past it.
func TestTheBridgeLabelIsTheEpochAfterTheEffectiveOne(t *testing.T) {
	testCases := []struct {
		name          string
		snapshot      chain.PhaseSnapshot
		wantEffective uint64
	}{
		{name: "before PoC", snapshot: chain.PhaseSnapshot{EpochIndex: 7, EffectiveEpochIndex: 7}, wantEffective: 7},
		{name: "inside PoC", snapshot: chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 7}, wantEffective: 7},
		{name: "after the switch", snapshot: chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8}, wantEffective: 8},
		{name: "unknown reads the latest", snapshot: chain.PhaseSnapshot{EpochIndex: 8}, wantEffective: 8},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := effectiveEpoch(testCase.snapshot); got != testCase.wantEffective {
				t.Fatalf("effectiveEpoch(%s) = %d, want %d", testCase.name, got, testCase.wantEffective)
			}
			if got := bridgeLabel(testCase.snapshot); got != testCase.wantEffective+1 {
				t.Fatalf("bridgeLabel(%s) = %d, want %d", testCase.name, got, testCase.wantEffective+1)
			}
		})
	}
}

// Test flow:
//  1. Table-driven: rows with a resolved chain epoch, rows resolved only by their label, and a row with neither, at effective epochs on both sides of the prune deadline.
//  2. Assert prunableOnChain reads an unresolved row's label as its latest possible chain epoch: past that epoch's deadline it is prunable, one epoch earlier it is not.
func TestPrunableOnChainReadsTheLabelOfAnUnresolvedRow(t *testing.T) {
	testCases := []struct {
		name          string
		chainEpoch    uint64
		rotationEpoch int64
		effective     uint64
		wantPrunable  bool
	}{
		{name: "resolved, past the deadline", chainEpoch: 50, effective: 52, wantPrunable: true},
		{name: "resolved, before the deadline", chainEpoch: 50, effective: 51, wantPrunable: false},
		{name: "label only, past the deadline", rotationEpoch: 50, effective: 52, wantPrunable: true},
		{name: "label only, past its lower bound's deadline alone", rotationEpoch: 50, effective: 51, wantPrunable: false},
		{name: "neither", effective: 90, wantPrunable: false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			manager := &Manager{snapshots: &fakeSnapshotSource{snapshot: chain.PhaseSnapshot{EpochIndex: testCase.effective, EffectiveEpochIndex: testCase.effective, BlockHeight: 1_000}}}
			record := store.DevshardRecord{EscrowID: "1", ChainEpoch: testCase.chainEpoch, RotationEpoch: testCase.rotationEpoch}

			if got := manager.prunableOnChain(record); got != testCase.wantPrunable {
				t.Fatalf("prunableOnChain(%s) = %v, want %v", testCase.name, got, testCase.wantPrunable)
			}
		})
	}
}
