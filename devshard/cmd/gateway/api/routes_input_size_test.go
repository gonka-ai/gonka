package api

import (
	"net/http"
	"testing"
)

// Test flow:
//  1. Send a chat completion request through the harness and assert it succeeds.
//  2. Read the recorded race attempt.
//  3. Assert the race was handed a non-zero body byte length.
//  4. Assert the race's token estimate is the byte length divided by four, rounded up.
func TestTheRaceIsHandedTheBodysBytesBesideTheTokenEstimate(t *testing.T) {
	live := newHarness(t)

	if recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil); recorder.Code != http.StatusOK {
		t.Fatalf("chat: got %d (%s)", recorder.Code, recorder.Body.String())
	}

	raced := live.inference.raced.Load()
	if raced == nil {
		t.Fatal("the race was never handed a request")
	}
	if raced.InputBytes == 0 {
		t.Fatal("the race was handed no body length, so the balance floor prices the input side at zero")
	}
	if want := (raced.InputBytes + 3) / 4; raced.InputTokens != want {
		t.Fatalf("InputTokens = %d over %d bytes, want %d: the two must describe the same body", raced.InputTokens, raced.InputBytes, want)
	}
}
