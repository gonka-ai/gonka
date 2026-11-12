package filters

import (
	"encoding/json"
	"testing"

	"common/completionapi"
)

// Test flow:
//  1. Normalize a request body via NormalizeRequest, varying the case across no logprobs ask, a width narrower than the pinned cap, a width wider than the cap, a decline, a non-boolean logprobs value, and a negative width.
//  2. For a case expecting rejection, assert NormalizeRequest returns an error.
//  3. For an accepted case, parse the normalized body and assert the logprobs and top_logprobs fields match the expected values, since the host re-pins the width it validates against rather than the gateway overwriting the client's ask.
func TestTheLogprobsAskIsCarriedRatherThanOverwritten(t *testing.T) {
	testCases := []struct {
		name          string
		body          string
		wantLogprobs  any
		wantTopWidth  any
		wantRejection bool
	}{
		{
			name:         "a client that asked for nothing is forwarded asking for nothing",
			body:         `{"messages":[{"role":"user","content":"hi"}]}`,
			wantLogprobs: nil, wantTopWidth: nil,
		},
		{
			name:         "an ask narrower than the pinned width survives as written",
			body:         `{"messages":[{"role":"user","content":"hi"}],"logprobs":true,"top_logprobs":2}`,
			wantLogprobs: true, wantTopWidth: json.Number("2"),
		},
		{
			name:         "a width wider than the pinned one is capped to it",
			body:         `{"messages":[{"role":"user","content":"hi"}],"logprobs":true,"top_logprobs":40}`,
			wantLogprobs: true, wantTopWidth: json.Number("5"),
		},
		{
			name:         "declining logprobs is carried as the decline it is",
			body:         `{"messages":[{"role":"user","content":"hi"}],"logprobs":false}`,
			wantLogprobs: false, wantTopWidth: nil,
		},
		{
			name:          "a logprobs that is not a boolean is refused",
			body:          `{"messages":[{"role":"user","content":"hi"}],"logprobs":"yes"}`,
			wantRejection: true,
		},
		{
			name:          "a negative width is refused",
			body:          `{"messages":[{"role":"user","content":"hi"}],"logprobs":true,"top_logprobs":-1}`,
			wantRejection: true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := NormalizeRequest([]byte(testCase.body), Options{DefaultMaxTokens: 3072, MaxTokensCap: 3072})
			if testCase.wantRejection {
				if err == nil {
					t.Fatalf("NormalizeRequest(%s) = %s, want a rejection", testCase.body, result.Body)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeRequest() = %v, want acceptance", err)
			}
			document, err := ParseDocument(result.Body)
			if err != nil {
				t.Fatalf("ParseDocument(%s) = %v", result.Body, err)
			}
			logprobs, _ := document.Get("logprobs")
			if logprobs != testCase.wantLogprobs {
				t.Errorf("logprobs = %#v, want %#v (body %s)", logprobs, testCase.wantLogprobs, result.Body)
			}
			topWidth, _ := document.Get("top_logprobs")
			if topWidth != testCase.wantTopWidth {
				t.Errorf("top_logprobs = %#v, want %#v (body %s)", topWidth, testCase.wantTopWidth, result.Body)
			}
		})
	}
}

// Test flow:
//  1. Compare logprobsWidthCap against completionapi.ForcedTopLogprobs.
//  2. Assert the two stay equal so the cap never drifts from what the host pins.
func TestTheCapIsTheWidthTheHostPins(t *testing.T) {
	if logprobsWidthCap != completionapi.ForcedTopLogprobs {
		t.Fatalf("logprobsWidthCap = %d, want the pinned %d", logprobsWidthCap, completionapi.ForcedTopLogprobs)
	}
}
