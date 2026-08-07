package main

import (
	"strings"
	"testing"
	"time"

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
