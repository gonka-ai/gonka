package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"devshard/transport"
)

// Test flow:
//  1. Build a chat prompt sized to fit the host request budget alone but overflow it once the escrow's catch-up reserve is added.
//  2. Send the chat completion request through the harness.
//  3. Assert the fixture prompt does not already exceed the host budget by itself.
//  4. Assert the response is refused with 413.
//  5. Assert no limiter slot was acquired, since the refusal must happen before admission.
func TestChatLeavesRoomForTheEscrowsCatchUp(t *testing.T) {
	live := newHarness(t)
	promptBytes := transport.MaxHostRequestBytes - hostCatchUpReserveBytes/2
	oversized := fmt.Sprintf(`{"model":"qwen","messages":[{"role":"user","content":%q}]}`, strings.Repeat("x", promptBytes))

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", oversized, nil)

	if transport.PromptWireBytes(promptBytes) > transport.MaxHostRequestBytes {
		t.Fatalf("the fixture is past the host budget on its own, so it proves nothing about the reserve")
	}
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d (%s), want 413", recorder.Code, recorder.Body.String())
	}
	if got := live.limiter.acquires.Load(); got != 0 {
		t.Fatalf("limiter slots taken: got %d, want 0 — the refusal must precede admission", got)
	}
}
