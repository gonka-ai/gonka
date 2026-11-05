package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestRetainedErrorStreamMatchesWireGolden(t *testing.T) {
	wire, err := os.ReadFile("../../../gatewayproto/testdata/error.sse")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../../gatewayproto/testdata/error.json")
	if err != nil {
		t.Fatal(err)
	}
	retainer := newErrorStreamRetainer(64 * 1024)
	retainer.retain(wire)
	proof, held := retainer.proof()
	if !held || !proof.Complete || proof.Truncated {
		t.Fatalf("unexpected proof: %+v", &proof)
	}
	if string(proof.ResponsePayload) != string(want) {
		t.Fatalf("payload = %s; want %s", proof.ResponsePayload, want)
	}
}

func TestRetainedStreamPreservesResponseKinds(t *testing.T) {
	data, err := os.ReadFile("../../../gatewayproto/testdata/stream-retention.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name       string          `json:"name"`
		Wire       string          `json:"wire"`
		Payload    json.RawMessage `json:"payload"`
		Complete   bool            `json:"complete"`
		ErrorProof bool            `json:"error_proof"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			retainer := newErrorStreamRetainer(64 * 1024)
			retainer.retain([]byte(tc.Wire))
			got := retainer.retained.Payload()
			complete := retainer.retained.Complete()
			_, hasProof := retainer.proof()
			if hasProof != tc.ErrorProof {
				t.Fatalf("error proof = %v, want %v", hasProof, tc.ErrorProof)
			}
			var want bytes.Buffer
			if err := json.Compact(&want, tc.Payload); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want.Bytes()) {
				t.Fatalf("retained payload = %s, want %s", got, want.Bytes())
			}
			if complete != tc.Complete {
				t.Fatalf("complete = %v, want %v", complete, tc.Complete)
			}
		})
	}
}
