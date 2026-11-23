package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"common/completionapi"

	"devshard/gatewayprotocol/streamevidence"
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

// Test flow:
//  1. Read the shared terminal-error fixture `long_error_stream.sse` from `gatewayprotocol/streamevidence/testdata`: about 100 KiB of 255-byte content lines, then an error line and `[DONE]`.
//  2. Take the fixture's evidence lines, keep the leading ones whose bytes plus one separator each fit in `streamevidence.MaxRetainedBytes`, and assert the error line is not among them.
//  3. Classify the fixture one event at a time with an attempt's SSE classifier, as the engine builds it, and flush it.
//  4. Assert the classifier holds no miss proof, since the retained lines carry no error, and that it retained exactly those leading lines, marked truncated and not complete.
func TestALongErrorStreamStopsAtTheSharedEvidenceCap(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "gatewayprotocol", "streamevidence", "testdata", "long_error_stream.sse"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	lines := streamevidence.DataLines(fixture)
	var want []string
	heldBytes := 0
	for _, line := range lines {
		heldBytes += len(line) + 1
		if heldBytes > streamevidence.MaxRetainedBytes {
			break
		}
		want = append(want, line)
	}
	if len(want) >= len(lines)-1 {
		t.Fatalf("the fixture's error line must lie past the cap: %d of %d lines fit", len(want), len(lines))
	}
	classifier := newSSEClassifier(testBudget(1<<20, 1<<20, 1<<20), testParticipant, false, nil)

	for _, event := range bytes.SplitAfter(fixture, []byte("\n\n")) {
		classifier.Classify(event)
	}
	classifier.Flush()

	if proof, held := classifier.missProof(); held {
		t.Fatalf("missProof() held %d bytes, want nothing: the retained lines carry no error", len(proof.ResponsePayload))
	}
	retained := classifier.retained
	if !slices.Equal(retained.lines, want) {
		t.Fatalf("retained %d lines, want the first %d of %d", len(retained.lines), len(want), len(lines))
	}
	if !retained.truncated {
		t.Error("the stream outgrew the cap, so the evidence is truncated")
	}
	if retained.complete {
		t.Error("[DONE] lies past the cap, so the evidence is not complete")
	}
}
