package types

import (
	"errors"
	"testing"
)

func startDiff(versions ...string) Diff {
	d := Diff{}
	for i, v := range versions {
		d.Txs = append(d.Txs, &DevshardTx{Tx: &DevshardTx_StartInference{StartInference: &MsgStartInference{InferenceId: uint64(i + 1), ProtocolVersion: v}}})
	}
	return d
}

func TestStartProofVersion(t *testing.T) {
	cases := []struct {
		name  string
		diffs []Diff
		want  string
		err   error
	}{
		{name: "no diffs", err: ErrStartProofMissing},
		{name: "no start", diffs: []Diff{{Txs: []*DevshardTx{nil}}}, err: ErrStartProofMissing},
		{name: "one start", diffs: []Diff{startDiff("v7")}, want: "v7"},
		{name: "unversioned start", diffs: []Diff{startDiff("v7", "")}, err: ErrStartProofMissing},
		{name: "unversioned first", diffs: []Diff{startDiff(""), startDiff("v7")}, err: ErrStartProofMissing},
		{name: "conflict", diffs: []Diff{startDiff("v7"), startDiff("v6")}, err: ErrProtocolVersionMismatch},
		{name: "trimmed", diffs: []Diff{startDiff(" v7 "), startDiff("v7")}, want: "v7"},
	}
	for _, tc := range cases {
		got, err := StartProofVersion(tc.diffs)
		if tc.err != nil {
			if !errors.Is(err, tc.err) {
				t.Fatalf("%s: err %v, want %v", tc.name, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}
