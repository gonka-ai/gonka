package filters

import (
	"encoding/json"
	"testing"
)

// kelvinSign is a multi-byte rune that lowercases to a single-byte "k".
const kelvinSign = "K"

// Test flow:
//  1. Build a message prefixed with kelvinSign, whose lowercased copy is two bytes shorter than the original, followed by a context-length refusal.
//  2. Call CapabilityLimits on the message.
//  3. Assert the parsed context limit is correct despite the byte-offset shift between the lowered copy and the original.
func TestCapabilityLimitsSurvivesALowercasingThatShortensTheMessage(t *testing.T) {
	t.Parallel()
	message := kelvinSign + " Maximum context length is 8192 tokens, however you requested 9000"

	contextLimit, _ := CapabilityLimits(message)

	if contextLimit != 8192 {
		t.Fatalf("context limit = %d, want 8192", contextLimit)
	}
}

// Test flow:
//  1. Call CapabilityLimits on a message whose digits are the last characters.
//  2. Assert the parsed context limit is correct.
func TestCapabilityLimitsReadsDigitsThatEndTheMessage(t *testing.T) {
	t.Parallel()
	contextLimit, _ := CapabilityLimits("This model's maximum context length is 8192")

	if contextLimit != 8192 {
		t.Fatalf("context limit = %d, want 8192", contextLimit)
	}
}

// Test flow:
//  1. Call CapabilityLimits on each context-length refusal vLLM emits, verbatim from the release named in the case.
//  2. Assert the context limit is the host's length and the requested count is the request's whole total, or the least total the message proves.
func TestCapabilityLimitsReadsEveryVLLMWording(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		message          string
		contextLimit     uint64
		contextRequested uint64
	}{
		{
			name:             "v0.9.1 entrypoints/openai/serving_engine.py:562 messages only",
			message:          "This model's maximum context length is 8192 tokens. However, you requested 9000 tokens in the messages, Please reduce the length of the messages.",
			contextLimit:     8192,
			contextRequested: 9000,
		},
		{
			name:             "v0.9.1 entrypoints/openai/serving_engine.py:568 messages and completion",
			message:          "This model's maximum context length is 8192 tokens. However, you requested 9064 tokens (9000 in the messages, 64 in the completion). Please reduce the length of the messages or completion.",
			contextLimit:     8192,
			contextRequested: 9064,
		},
		{
			name:             "v0.11.0 entrypoints/openai/serving_engine.py:676 input over the length",
			message:          "This model's maximum context length is 8192 tokens. However, your request has 9000 input tokens. Please reduce the length of the input messages.",
			contextLimit:     8192,
			contextRequested: 9000,
		},
		{
			name:             "v0.11.0 entrypoints/openai/serving_engine.py:684 max_tokens over the rest",
			message:          "'max_tokens' or 'max_completion_tokens' is too large: 64. This model's maximum context length is 8192 tokens and your request has 8150 input tokens (64 > 8192 - 8150).",
			contextLimit:     8192,
			contextRequested: 8214,
		},
		{
			name:             "v0.25.1 renderers/params.py:442 exact total",
			message:          "This model's maximum context length is 8192 tokens. However, you requested 64 output tokens and your prompt contains 10000 input tokens, for a total of 10064 tokens. Please reduce the length of the input prompt or the number of requested output tokens.",
			contextLimit:     8192,
			contextRequested: 10064,
		},
		{
			name:             "v0.25.1 renderers/params.py:442 truncated total",
			message:          "This model's maximum context length is 8192 tokens. However, you requested 64 output tokens and your prompt contains at least 8129 input tokens, for a total of at least 8193 tokens. Please reduce the length of the input prompt or the number of requested output tokens.",
			contextLimit:     8192,
			contextRequested: 8193,
		},
		{
			name:             "v0.25.1 renderers/params.py:345 characters over the input bound",
			message:          "This model's maximum context length is 8192 tokens. However, you requested 64 output tokens and your prompt contains 100000 characters (more than 81280 characters, which is the upper bound for 8128 input tokens). Please reduce the length of the input prompt or the number of requested output tokens.",
			contextLimit:     8192,
			contextRequested: 8193,
		},
		{
			name:             "main renderers/params.py:499 exact total",
			message:          "This model's maximum context length is 131072 tokens. However, you requested 4096 output tokens and your prompt contains 200000 input tokens, for a total of 204096 tokens. Please reduce the length of the input prompt or the number of requested output tokens.",
			contextLimit:     131072,
			contextRequested: 204096,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			contextLimit, contextRequested := CapabilityLimits(testCase.message)
			if contextLimit != testCase.contextLimit || contextRequested != testCase.contextRequested {
				t.Fatalf("CapabilityLimits(%q) = (%d, %d), want (%d, %d)", testCase.message, contextLimit, contextRequested, testCase.contextLimit, testCase.contextRequested)
			}
		})
	}
}

// Test flow:
//  1. Call CapabilityLimits on a message that mentions the maximum context length without any digits.
//  2. Assert both returned values are zero.
func TestCapabilityLimitsIgnoresAPhraseWithoutDigits(t *testing.T) {
	t.Parallel()
	contextLimit, contextRequested := CapabilityLimits("maximum context length is unknown")

	if contextLimit != 0 || contextRequested != 0 {
		t.Fatalf("limits = (%d, %d), want (0, 0)", contextLimit, contextRequested)
	}
}

// Test flow:
//  1. Build an error body for each case's capability-refusal message via `errorBody`, varying the phrasing between a shortening-lowercase message, a message whose digits end it, and the tool-choice-unsupported message.
//  2. Assert IsCacheableResponse reports the 400 response as not cacheable.
//  3. Assert HasNonCacheableError reports the body as a non-cacheable error.
func TestContextRefusalStaysOutOfTheCacheHoweverItIsSpelled(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		message string
	}{
		{name: "shortening_lowercase", message: kelvinSign + " Maximum context length is 8192 tokens"},
		{name: "digits_end_the_message", message: "This model's maximum context length is 8192"},
		{name: "tool_choice", message: ToolChoiceUnsupportedMessage},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			body := errorBody(t, testCase.message)
			if IsCacheableResponse(400, body) {
				t.Fatalf("cached a capability refusal the race would have retried: %q", testCase.message)
			}
			if !HasNonCacheableError(body) {
				t.Fatalf("capability refusal not reported as non-cacheable: %q", testCase.message)
			}
		})
	}
}

func errorBody(t *testing.T, message string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": "BadRequestError", "code": 400},
	})
	if err != nil {
		t.Fatalf("marshalling error body: %v", err)
	}
	return body
}
