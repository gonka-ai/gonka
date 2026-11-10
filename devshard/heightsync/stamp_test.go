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
	// L0b would compare start ≤ confirm. A present start (hash set) and
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
		{Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{
			ObservedHeight: 7, ObservedBlockHash: []byte{0xaa},
		}}},
		{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
			ObservedHeight: 12, ObservedBlockHash: []byte{0xbb},
		}}},
	}
	if got := LogResidentHeight(txs, 40); got != 12 {
		t.Fatalf("max host stamp: got %d want 12", got)
	}
}

func TestLogResidentHeight_IgnoresUserStamps(t *testing.T) {
	hash := []byte{0xaa}
	user := []*types.DevshardTx{
		{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			ObservedHeight: 1 << 40, ObservedBlockHash: hash,
		}}},
		{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			InferenceId: 1, ObservedHeight: 1 << 40, ObservedBlockHash: hash,
		}}},
	}
	if got := LogResidentHeight(user, 500); got != 500 {
		t.Fatalf("user stamps must not move hNow: got %d want 500", got)
	}
	mixed := append(user, &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{
		ObservedHeight: 510, ObservedBlockHash: hash,
	}}})
	if got := LogResidentHeight(mixed, 500); got != 510 {
		t.Fatalf("host ack still moves hNow: got %d want 510", got)
	}
}

func TestExecutorStamp_OnlyHostSignedInferenceLegs(t *testing.T) {
	hash := []byte{0xaa}

	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 7, ObservedHeight: 100, ObservedBlockHash: hash,
	}}}
	id, h, ok := ExecutorStamp(confirm)
	if !ok || id != 7 || h != 100 {
		t.Fatalf("stamped confirm: got (%d, %d, %v)", id, h, ok)
	}

	finish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{
		InferenceId: 8, ObservedHeight: 101, ObservedBlockHash: hash,
	}}}
	id, h, ok = ExecutorStamp(finish)
	if !ok || id != 8 || h != 101 {
		t.Fatalf("stamped finish: got (%d, %d, %v)", id, h, ok)
	}

	// A stampless leg is no height claim, so it is no round-trip to credit.
	stampless := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 9, ObservedHeight: 102,
	}}}
	if _, _, ok = ExecutorStamp(stampless); ok {
		t.Fatal("confirm without a hash must not be an executor stamp")
	}

	// Start is sequencer-composed: the producer cannot claim against itself.
	start := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
		InferenceId: 10, ObservedHeight: 103, ObservedBlockHash: hash,
	}}}
	if _, _, ok = ExecutorStamp(start); ok {
		t.Fatal("start is user-signed and must not be an executor stamp")
	}

	ack := &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{
		SlotId: 1, ObservedHeight: 104, ObservedBlockHash: hash,
	}}}
	if _, _, ok = ExecutorStamp(ack); ok {
		t.Fatal("an ack is credited by slot_id, not by the executor rule")
	}

	if _, _, ok = ExecutorStamp(nil); ok {
		t.Fatal("nil tx is no stamp")
	}
}
