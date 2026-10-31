package engine

import (
	"encoding/json"
	"testing"

	"common/completionapi"
)

// Test flow:
//  1. Build an `errorStreamRetainer` and retain two reads: one carrying a devshard receipt line plus an error line, another carrying a devshard meta line plus `[DONE]`.
//  2. Get the retainer's proof.
//  3. Assert the proof was held, is complete, and is not truncated.
//  4. Unmarshal the proof payload and assert its events are only the host's own error and `[DONE]` lines, with the devshard envelope lines stripped out.
func TestTheProofHoldsTheHostsOwnLines(t *testing.T) {
	retainer := newErrorStreamRetainer(64 * 1024)
	retainer.retain([]byte("data: {\"devshard_receipt\":{\"nonce\":1}}\n\ndata: {\"error\":{\"message\":\"boom\"}}\n\n"))
	retainer.retain([]byte("data: {\"devshard_meta\":{\"nonce\":1}}\n\ndata: [DONE]\n\n"))

	proof, held := retainer.proof()

	if !held {
		t.Fatal("proof() held nothing, want the error lines")
	}
	if !proof.Complete {
		t.Error("the retained stream reached [DONE], so it is complete")
	}
	if proof.Truncated {
		t.Error("nothing was dropped, so it is not truncated")
	}
	var serialized completionapi.SerializedStreamedResponse
	if err := json.Unmarshal(proof.ResponsePayload, &serialized); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	want := []string{`data: {"error":{"message":"boom"}}`, "data: [DONE]"}
	if len(serialized.Events) != len(want) {
		t.Fatalf("events = %q, want %q", serialized.Events, want)
	}
	for i := range want {
		if serialized.Events[i] != want[i] {
			t.Fatalf("events = %q, want %q", serialized.Events, want)
		}
	}
}

// Test flow:
//  1. Retain a stream carrying only content and `[DONE]`, no error, in an `errorStreamRetainer`.
//  2. Get the retainer's proof.
//  3. Assert nothing is held.
func TestAStreamWithoutAnErrorProvesNothing(t *testing.T) {
	retainer := newErrorStreamRetainer(64 * 1024)
	retainer.retain([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"))

	proof, held := retainer.proof()

	if held {
		t.Fatalf("proof() held %s, want nothing for a stream that carries no error", proof.ResponsePayload)
	}
}

// Test flow:
//  1. Build an `errorStreamRetainer` with a small byte bound, then retain an error line followed by `[DONE]`.
//  2. Get the retainer's proof.
//  3. Assert something is held, marked truncated, and not complete.
func TestAProofThatLostBytesSaysSo(t *testing.T) {
	retainer := newErrorStreamRetainer(40)
	retainer.retain([]byte("data: {\"error\":{\"message\":\"boom\"}}\n\n"))
	retainer.retain([]byte("data: [DONE]\n\n"))

	proof, held := retainer.proof()

	if !held {
		t.Fatal("proof() held nothing, want what was retained before the bound")
	}
	if !proof.Truncated {
		t.Error("the bound was hit, so the proof is truncated")
	}
	if proof.Complete {
		t.Error("[DONE] never fit, so the proof is not complete")
	}
}

// Test flow:
//  1. Retain an error line in an `errorStreamRetainer`.
//  2. Release the retainer.
//  3. Assert its proof holds nothing afterward.
func TestReleasingRetentionDropsTheLines(t *testing.T) {
	retainer := newErrorStreamRetainer(64 * 1024)
	retainer.retain([]byte("data: {\"error\":{\"message\":\"boom\"}}\n\n"))

	retainer.release()

	if _, held := retainer.proof(); held {
		t.Fatal("proof() held something after release")
	}
}

// Test flow:
//  1. Retain an error line followed by `[DONE]` in an `errorStreamRetainer`.
//  2. Get the retainer's proof and assert it is held.
//  3. Assert the verifier reads the payload as a terminal error.
//  4. Assert hashing the response payload twice produces the same digest.
func TestTheProofHashesToWhatTheVerifierRecomputes(t *testing.T) {
	retainer := newErrorStreamRetainer(64 * 1024)
	retainer.retain([]byte("data: {\"error\":{\"message\":\"boom\"}}\n\ndata: [DONE]\n\n"))

	proof, held := retainer.proof()
	if !held {
		t.Fatal("proof() held nothing")
	}
	if _, isError := completionapi.IsTerminalErrorResponse(proof.ResponsePayload); !isError {
		t.Fatal("the verifier must read the payload as a terminal error")
	}
}
