package api

import (
	"net/http"
	"testing"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/limits"
)

// Test flow:
//  1. Start a harness and install a log capture.
//  2. Send one non-streaming chat completion.
//  3. Assert the flushed "request finished" info line carries the served outcome's fields.
func TestAServedRequestWritesItsRecord(t *testing.T) {
	logged := logcapture.Install(t)
	live := newHarness(t)

	response := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)
	live.events.Flush()

	logged.RequireLine(t, logcapture.Entry{Level: "info", Msg: "request finished", Fields: []any{
		"request", "request-1", "model", "qwen", "escrow", "7", "stream", false,
		"input_tokens", uint64(0), "output_tokens", int64(0),
		"outcome", "served", "bytes", int64(response.Body.Len()), "terminated", false, "duration_ms", int64(0),
	}})
}

// Test flow:
//  1. Start a harness whose inference outcome is a won race (`benchOutcome`) tied to escrow "7".
//  2. Send one non-streaming chat completion.
//  3. Assert the flushed "request finished" line adds the winner's host, token counts, nonce-finished flag and clock-offset facts.
func TestAServedRequestWithAWinnerWritesTheWinnersFacts(t *testing.T) {
	logged := logcapture.Install(t)
	live := newHarness(t)
	live.inference.outcome = benchOutcome()
	live.inference.outcome.EscrowID = "7"

	response := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)
	live.events.Flush()

	logged.RequireLine(t, logcapture.Entry{Level: "info", Msg: "request finished", Fields: []any{
		"request", "request-1", "model", "qwen", "escrow", "7", "stream", false,
		"input_tokens", uint64(1024), "output_tokens", int64(384), "host", "qqqqqqqq",
		"outcome", "served", "bytes", int64(response.Body.Len()), "terminated", false, "duration_ms", int64(0),
		"nonce_finished", false, "host_clock_offset_ms", int64(400), "host_receipt_ms", int64(200),
	}})
}

// Test flow:
//  1. Start a harness whose inference fails with `ErrAllAttemptsFailed` before producing any reply.
//  2. Send one non-streaming chat completion.
//  3. Assert the flushed "request finished" line is logged at warn with the failed_before_first_byte outcome and the error text.
func TestARaceThatFailedBeforeItsFirstByteWritesItsRecordAtWarn(t *testing.T) {
	logged := logcapture.Install(t)
	live := newHarness(t)
	live.inference.reply = ""
	live.inference.outcome = engine.RaceOutcome{EscrowID: "7"}
	live.inference.err = engine.ErrAllAttemptsFailed

	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)
	live.events.Flush()

	logged.RequireLine(t, logcapture.Entry{Level: "warn", Msg: "request finished", Fields: []any{
		"request", "request-1", "model", "qwen", "escrow", "7", "stream", false,
		"input_tokens", uint64(0), "output_tokens", int64(0),
		"outcome", "failed_before_first_byte", "bytes", int64(0), "terminated", false, "duration_ms", int64(0),
		"error", "every attempt failed",
	}})
}

// Test flow:
//  1. Start a harness whose inference streams one chunk and then fails with `ErrAllAttemptsFailed`.
//  2. Send one streaming chat completion.
//  3. Assert the flushed "request finished" line is logged at warn with the failed_mid_stream outcome, terminated true, and the error text.
func TestAStreamThatFailedMidAnswerWritesItsRecordAtWarn(t *testing.T) {
	logged := logcapture.Install(t)
	live := newHarness(t)
	live.inference.chunks = []string{`data: {"choices":[{"index":0,"delta":{"content":"half"}}]}` + "\n\n"}
	live.inference.outcome = engine.RaceOutcome{EscrowID: "7"}
	live.inference.err = engine.ErrAllAttemptsFailed

	response := live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, nil)
	live.events.Flush()

	logged.RequireLine(t, logcapture.Entry{Level: "warn", Msg: "request finished", Fields: []any{
		"request", "request-1", "model", "qwen", "escrow", "7", "stream", true,
		"input_tokens", uint64(0), "output_tokens", int64(0),
		"outcome", "failed_mid_stream", "bytes", int64(response.Body.Len()), "terminated", true, "duration_ms", int64(0),
		"error", "every attempt failed",
	}})
}

// Test flow:
//  1. Start a harness and send the same chat completion twice from the same caller.
//  2. Assert the second, replayed request's flushed "request finished" line carries the body's byte estimate with its source, the cache_hit outcome and the replayed body's byte count.
func TestACacheHitWritesTheShortRecord(t *testing.T) {
	logged := logcapture.Install(t)
	live := newHarness(t)
	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	replay := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))
	live.events.Flush()

	logged.RequireLine(t, logcapture.Entry{Level: "info", Msg: "request finished", Fields: []any{
		"request", "request-1", "model", "qwen", "escrow", "7", "stream", false,
		"input_tokens", filters.EstimatedPromptTokens(int(live.inference.raced.Load().InputBytes)), "input_tokens_source", "bytes_estimate",
		"outcome", "cache_hit", "bytes", int64(replay.Body.Len()),
	}})
}

// Test flow:
//  1. Start a harness whose limiter refuses with a "too many concurrent requests" reason.
//  2. Send one chat completion.
//  3. Assert the flushed line logs the refusal at warn with the reason mapped to `concurrent_requests`.
func TestALimiterRefusalIsLoggedWithTheCapItHit(t *testing.T) {
	logged := logcapture.Install(t)
	live := newHarness(t)
	live.limiter.err = &limits.RateLimitError{Reason: "too many concurrent requests"}

	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)
	live.events.Flush()

	logged.RequireLine(t, logcapture.Entry{Level: "warn", Msg: "gateway limiter turned a request away", Fields: []any{
		"request", "request-1", "model", "qwen", "reason", "concurrent_requests",
	}})
}

// Test flow:
//  1. Start a harness whose inference streams one reasoning chunk and then stops without an answer.
//  2. Send one streaming chat completion.
//  3. Assert the flushed line warns that a host stopped mid-answer with the request, model and escrow fields.
func TestAHostThatStoppedMidAnswerIsLogged(t *testing.T) {
	logged := logcapture.Install(t)
	live := newHarness(t)
	live.inference.chunks = []string{`data: {"choices":[{"index":0,"delta":{"reasoning":"still working"}}]}` + "\n\n"}

	live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))
	live.events.Flush()

	logged.RequireLine(t, logcapture.Entry{Level: "warn", Msg: "a host stopped mid-answer: reply served, not cached", Fields: []any{
		"request", "request-1", "model", "qwen", "escrow", "7",
	}})
}
