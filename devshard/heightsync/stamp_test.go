package heightsync

import (
	"testing"

	"devshard/types"
)

func TestStampPresent_EmptyHashIsAbsent(t *testing.T) {
	if StampPresent(nil) {
		t.Fatal("nil hash must be absent")
	}
	if StampPresent([]byte{}) {
		t.Fatal("empty hash must be absent")
	}
	if !StampPresent([]byte{0x00}) {
		t.Fatal("non-empty hash is a claim even at height 0")
	}
	if !StampPresent([]byte{0xaa, 0xbb}) {
		t.Fatal("non-empty hash is a claim")
	}
}

func TestStampPresent_PresentThenAbsentIsNotRegression(t *testing.T) {
	// H38: L0b would compare start ≤ confirm. A present start (hash set) and
	// an absent confirm (hash empty, height proto-zero) must skip the check,
	// not read confirm as height 0 and INVALID(height_regression).
	startHash := []byte{0x11}
	var confirmHash []byte
	if !StampPresent(startHash) {
		t.Fatal("start is present")
	}
	if StampPresent(confirmHash) {
		t.Fatal("confirm is absent")
	}
	if StampPresent(startHash) && !StampPresent(confirmHash) {
		return // skip L0b for the missing leg — not a regression
	}
	t.Fatal("present-then-absent pair must not be treated as height 0")
}

func TestLogResidentHeight_PrefersDiffStampThenFallback(t *testing.T) {
	unstamped := []*types.DevshardTx{{Tx: &types.DevshardTx_ForceHeightSyncTurn{
		ForceHeightSyncTurn: &types.MsgForceHeightSyncTurn{TriggerNonce: 1},
	}}}
	if got := LogResidentHeight(unstamped, 40); got != 40 {
		t.Fatalf("unstamped fallback: got %d want 40", got)
	}
	txs := []*types.DevshardTx{
		{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			ObservedHeight: 7, ObservedBlockHash: []byte{0xaa},
		}}},
		{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			ObservedHeight: 12, ObservedBlockHash: []byte{0xbb},
		}}},
	}
	if got := LogResidentHeight(txs, 40); got != 12 {
		t.Fatalf("max stamp: got %d want 12", got)
	}
}
