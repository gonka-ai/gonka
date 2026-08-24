package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	commonobs "common/observability"

	"devshard/accounting"
	"devshard/observability"
	"devshard/transport"
	"devshard/user"

	"github.com/stretchr/testify/require"
)

func withPayloadPolicy(t *testing.T, level, mlnode, quarantine string) {
	t.Helper()
	observability.ResetPayloadPolicyForTest()
	t.Setenv("DEVSHARD_LOG_PAYLOADS", level)
	t.Setenv("DEVSHARD_LOG_PAYLOADS_MLNODE", mlnode)
	t.Setenv("DEVSHARD_LOG_PAYLOADS_QUARANTINE", quarantine)
	t.Cleanup(observability.ResetPayloadPolicyForTest)
	_ = observability.LoadPayloadPolicy()
}

func TestMaybeLogMLNodePayload_HostResponseWithBody(t *testing.T) {
	withPayloadPolicy(t, "full", "true", "false")
	buf := withDispositionLogCapture(t)

	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hello payload"}]}`)
	inf := &inflight{
		escrowID:              "e1",
		nonce:                 7,
		sendTime:              time.Now().Add(-150 * time.Millisecond),
		payloadResponseSample: []byte(`data: {"choices":[{"delta":{"content":"hi"}}]}`),
		err:                   errEmptyStream,
	}
	inf.contentChunks.Store(1)
	maybeLogMLNodePayload(inf.attemptCtx(), inf, user.InferenceParams{
		Prompt: prompt, Model: "m", Stream: true, MaxTokens: 16,
	}, accounting.FailureHostResponse, "empty_stream")

	out := buf.String()
	require.Contains(t, out, `"stage":"payload_captured"`)
	require.Contains(t, out, `"devshard.prompt.sha256":"`+observability.PromptSHA256(prompt)+`"`)
	require.Contains(t, out, `"response_ms":`)
	require.Contains(t, out, `"failed_at":`)
	require.Contains(t, out, `"response":`)
	require.Contains(t, out, "choices")
	require.Contains(t, out, `"failure_origin":"host_response"`)
}

func TestMaybeLogMLNodePayload_EmptyBodyFallsBackToStatusHeaders(t *testing.T) {
	withPayloadPolicy(t, "hash", "true", "false")
	buf := withDispositionLogCapture(t)

	prompt := []byte(`{"messages":[{"role":"user","content":"x"}]}`)
	inf := &inflight{
		escrowID: "e1",
		nonce:    8,
		sendTime: time.Now().Add(-20 * time.Millisecond),
		err: &transport.UpstreamStatusError{
			StatusCode: 503,
			Body:       "",
			Headers:    map[string]string{"Content-Type": "application/json"},
		},
	}
	maybeLogMLNodePayload(inf.attemptCtx(), inf, user.InferenceParams{Prompt: prompt, Model: "m"},
		accounting.FailureHostResponse, "http_503")

	out := buf.String()
	require.Contains(t, out, `"stage":"payload_captured"`)
	require.Contains(t, out, `"http_status":503`)
	require.Contains(t, out, `"response_headers":`)
	require.Contains(t, out, "Content-Type")
	require.Contains(t, out, `"response_ms":`)
	require.NotContains(t, out, `"response":"`) // hash level, no body field
}

func TestMaybeLogMLNodePayload_SSETruncated(t *testing.T) {
	withPayloadPolicy(t, "full", "true", "false")
	buf := withDispositionLogCapture(t)

	prompt := []byte(`{"messages":[{"role":"user","content":"partial please"}]}`)
	inf := &inflight{
		escrowID:              "e1",
		nonce:                 11,
		sendTime:              time.Now().Add(-50 * time.Millisecond),
		payloadResponseSample: []byte(`data: {"choices":[{"delta":{"content":"partial-body"}}]}`),
		err:                   transport.ErrSSEStreamTruncated,
	}
	inf.contentChunks.Store(3)
	origin := accounting.FailureOriginFromDetail("sse_truncated")
	require.Equal(t, accounting.FailureHostResponse, origin)
	maybeLogMLNodePayload(inf.attemptCtx(), inf, user.InferenceParams{Prompt: prompt, Model: "m", Stream: true},
		origin, "sse_truncated")

	out := buf.String()
	require.Contains(t, out, `"stage":"payload_captured"`)
	require.Contains(t, out, "partial-body")
	require.Contains(t, out, `"detail_reason":"sse_truncated"`)
	require.Contains(t, out, `"http_status":200`)
}

func TestMaybeLogMLNodePayload_SkipsTransportUnknown(t *testing.T) {
	withPayloadPolicy(t, "full", "true", "false")
	buf := withDispositionLogCapture(t)

	inf := &inflight{escrowID: "e1", nonce: 9, sendTime: time.Now()}
	maybeLogMLNodePayload(inf.attemptCtx(), inf, user.InferenceParams{Prompt: []byte(`{}`)},
		accounting.FailureTransportUnknown, "eof_transport")
	require.NotContains(t, buf.String(), `"stage":"payload_captured"`)
}

func TestMaybeLogMLNodePayload_DisabledByDefault(t *testing.T) {
	withPayloadPolicy(t, "off", "false", "false")
	buf := withDispositionLogCapture(t)
	inf := &inflight{escrowID: "e1", nonce: 1, sendTime: time.Now(), payloadResponseSample: []byte("x")}
	maybeLogMLNodePayload(inf.attemptCtx(), inf, user.InferenceParams{Prompt: []byte(`{}`)},
		accounting.FailureHostResponse, "empty_stream")
	require.NotContains(t, buf.String(), `"stage":"payload_captured"`)
}

func TestMaybeLogQuarantinePayload_SizesOnly(t *testing.T) {
	withPayloadPolicy(t, "off", "false", "true")
	buf := withDispositionLogCapture(t)

	maybeLogQuarantinePayload("p1", "m", "probe", "http_throttle_quarantine", QuarantinePayloadStats{
		RequestBytes:  100,
		ResponseBytes: 12,
		Stream:        true,
		MessageCount:  1,
		MaxTokens:     32,
	})
	out := buf.String()
	require.Contains(t, out, `"stage":"payload_quarantine"`)
	require.Contains(t, out, `"participant_key":"p1"`)
	require.Contains(t, out, `"request_bytes":100`)
	require.Contains(t, out, `"response_bytes":12`)
	require.Contains(t, out, `"quarantine_mode":"probe"`)
	require.NotContains(t, out, `"request":"`)
	require.NotContains(t, out, `"response":"`)
}

// An attempt abandoned by a bounded wait is still streaming when
// finishRaceOutcome reaches it. The terminal path must leave it alone so the
// once-guard survives for the classifier that actually knows how it failed.
func TestMaybeLogMLNodePayloadForTerminal_SkipsAttemptStillStreaming(t *testing.T) {
	withPayloadPolicy(t, "full", "true", "false")
	buf := withDispositionLogCapture(t)

	e := &Redundancy{}
	prompt := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	inf := &inflight{
		escrowID:              "e1",
		nonce:                 21,
		sendTime:              time.Now(),
		capturePayload:        true,
		payloadResponseSample: []byte("data: partial"),
		done:                  make(chan struct{}),
	}
	inf.setReceiptAt(time.Now())

	// not_finished is a host_response origin, so only the done check stops it.
	e.maybeLogMLNodePayloadForTerminal(inf, user.InferenceParams{Prompt: prompt},
		accounting.FailureOriginFromDetail("not_finished"), "not_finished")
	require.NotContains(t, buf.String(), `"stage":"payload_captured"`)
	require.False(t, inf.payloadCaptured.Load(), "once-guard must stay available")

	// The attempt finishes and its own classifier emits the authoritative line.
	inf.err = errEmptyStream
	inf.errorSource = "error.TimeoutError"
	inf.errorMessage = "host gave up"
	close(inf.done)
	e.maybeLogMLNodePayloadForAttempt(inf, user.InferenceParams{Prompt: prompt},
		accounting.FailureHostResponse, "empty_stream")

	out := buf.String()
	require.Contains(t, out, `"stage":"payload_captured"`)
	require.Contains(t, out, `"detail_reason":"empty_stream"`)
	require.Contains(t, out, `"error_message":"host gave up"`)
}

func TestMaybeLogMLNodePayloadForTerminal_CapturesFinishedAttempt(t *testing.T) {
	withPayloadPolicy(t, "full", "true", "false")
	buf := withDispositionLogCapture(t)

	e := &Redundancy{}
	inf := &inflight{
		escrowID:              "e1",
		nonce:                 22,
		sendTime:              time.Now(),
		capturePayload:        true,
		payloadResponseSample: []byte("data: partial"),
		err:                   transport.ErrSSEStreamTruncated,
		done:                  make(chan struct{}),
	}
	close(inf.done)

	e.maybeLogMLNodePayloadForTerminal(inf, user.InferenceParams{Prompt: []byte(`{}`)},
		accounting.FailureHostResponse, "sse_truncated")

	require.Contains(t, buf.String(), `"stage":"payload_captured"`)
	require.Contains(t, buf.String(), `"detail_reason":"sse_truncated"`)
}

func TestMaybeLogMLNodePayload_SampleTruncationIsItsOwnField(t *testing.T) {
	withPayloadPolicy(t, "full", "true", "false")
	buf := withDispositionLogCapture(t)

	inf := &inflight{
		escrowID:                       "e1",
		nonce:                          12,
		sendTime:                       time.Now(),
		payloadResponseSample:          []byte("data: {}"),
		payloadResponseSampleTruncated: true,
		err:                            errEmptyStream,
	}
	maybeLogMLNodePayload(inf.attemptCtx(), inf, user.InferenceParams{Prompt: []byte(`{}`)},
		accounting.FailureHostResponse, "empty_stream")

	out := buf.String()
	require.Contains(t, out, `"sample_truncated":true`)
	require.Equal(t, 1, strings.Count(out, `"response_truncated"`), "the formatter owns response_truncated alone")
}

func TestCollectFailureResponseBody_ErrorSampleIsComplete(t *testing.T) {
	inf := &inflight{errorBodySample: []byte(`data: {"error":{"message":"boom"}}`)}
	body, truncated := collectFailureResponseBody(inf)
	require.Equal(t, `data: {"error":{"message":"boom"}}`, string(body))
	require.False(t, truncated)
}

func TestCapturePayloadResponseChunk_RetainsNothingWhenDisabled(t *testing.T) {
	inf := &inflight{}
	inf.capturePayloadResponseChunk(bytes.Repeat([]byte("x"), 4096))
	require.Empty(t, inf.payloadResponseSample)
	require.False(t, inf.payloadResponseSampleTruncated)

	inf.capturePayload = true
	inf.capturePayloadResponseChunk([]byte("data: {}"))
	require.Equal(t, "data: {}", string(inf.payloadResponseSample))
}

func TestEmptyStreamQuarantineStats_LazyAndCountsStreamBytesOnce(t *testing.T) {
	prompt := []byte(`{"messages":[{"role":"system"},{"role":"user"}]}`)
	inf := &inflight{
		payloadResponseSample:   []byte("data: role-chunk"),
		emptyResponseBodySample: "data: role-chunk",
	}
	inf.outputBytes.Store(16)

	withPayloadPolicy(t, "off", "false", "false")
	require.Equal(t, QuarantinePayloadStats{}, emptyStreamQuarantineStats(inf, user.InferenceParams{Prompt: prompt}))

	withPayloadPolicy(t, "off", "false", "true")
	stats := emptyStreamQuarantineStats(inf, user.InferenceParams{Prompt: prompt, Stream: true, MaxTokens: 8})
	require.Equal(t, 16, stats.ResponseBytes, "overlapping capture buffers must not be summed")
	require.Equal(t, len(prompt), stats.RequestBytes)
	require.Equal(t, 2, stats.MessageCount)
}

// lockProbeWriter fails the test if a quarantine payload line is written while
// the limiter mutex is held — that mutex gates admission for every request.
type lockProbeWriter struct {
	t       *testing.T
	limiter *ParticipantRequestLimiter
	buf     *bytes.Buffer
}

func (w *lockProbeWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"stage":"payload_quarantine"`)) {
		if w.limiter.mu.TryLock() {
			w.limiter.mu.Unlock()
		} else {
			w.t.Error("payload_quarantine emitted while holding the limiter lock")
		}
	}
	return w.buf.Write(p)
}

func TestQuarantinePayloadLogEmittedOffTheLimiterLock(t *testing.T) {
	withPayloadPolicy(t, "off", "false", "true")

	limiter := NewParticipantRequestLimiter(10, 10)
	var buf bytes.Buffer
	observability.InstallLogger("json")
	probe := &lockProbeWriter{t: t, limiter: limiter, buf: &buf}
	prev := slog.Default()
	slog.SetDefault(slog.New(commonobs.NewTraceHandler(
		slog.NewJSONHandler(probe, &slog.HandlerOptions{Level: slog.LevelInfo}))))
	t.Cleanup(func() {
		slog.SetDefault(prev)
		observability.InstallLogger("text")
	})

	limiter.ObserveStalledWinner("stall-host")

	require.Contains(t, buf.String(), `"stage":"payload_quarantine"`)
	require.Contains(t, buf.String(), `"reason":"stalled_winner_quarantine"`)
	require.Zero(t, limiter.pendingQuarantineCount.Load(), "queue must drain on unlock")
}

func TestCollectFailureResponseBody_PrefersPayloadSample(t *testing.T) {
	inf := &inflight{
		payloadResponseSample: []byte("partial-stream-body"),
		pendingBuf:            []byte("pending"),
	}
	body, _ := collectFailureResponseBody(inf)
	require.Equal(t, "partial-stream-body", string(body))
}

func TestCollectFailureResponseBody_UpstreamBody(t *testing.T) {
	inf := &inflight{
		err: &transport.UpstreamStatusError{StatusCode: 502, Body: `{"error":{"message":"bad"}}`},
	}
	body, _ := collectFailureResponseBody(inf)
	require.True(t, strings.Contains(string(body), "bad"))
}
