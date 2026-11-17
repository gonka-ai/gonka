package types

import "testing"

func TestStartProtocolVersion(t *testing.T) {
	empty := StartProtocolVersion(nil)
	if empty != "" {
		t.Fatalf("empty diffs: got %q", empty)
	}
	diffs := []Diff{{
		Txs: []*DevshardTx{
			{Tx: &DevshardTx_StartInference{StartInference: &MsgStartInference{InferenceId: 1}}},
			{Tx: &DevshardTx_StartInference{StartInference: &MsgStartInference{InferenceId: 2, ProtocolVersion: " v5 "}}},
		},
	}}
	if got := StartProtocolVersion(diffs); got != "v5" {
		t.Fatalf("got %q, want v5", got)
	}
}
