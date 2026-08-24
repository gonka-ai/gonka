package observability_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"devshard/observability"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestParsePayloadPolicy_Defaults(t *testing.T) {
	p := observability.ParsePayloadPolicy("", "", "", "", "")
	require.Equal(t, observability.PayloadLevelOff, p.Level)
	require.False(t, p.MLNode)
	require.False(t, p.Quarantine)
	require.False(t, p.Validation)
	require.Equal(t, observability.DefaultPayloadMaxBytes, p.MaxBytes)
	require.False(t, p.MLNodeCaptureEnabled())
	require.False(t, p.QuarantineCaptureEnabled())
}

func TestParsePayloadPolicy_Enabled(t *testing.T) {
	p := observability.ParsePayloadPolicy("hash", "true", "1", "yes", "4096")
	require.Equal(t, observability.PayloadLevelHash, p.Level)
	require.True(t, p.MLNodeCaptureEnabled())
	require.True(t, p.QuarantineCaptureEnabled())
	require.True(t, p.Validation)
	require.Equal(t, 4096, p.MaxBytes)
}

func TestParsePayloadPolicy_QuarantineLevelIndependent(t *testing.T) {
	p := observability.ParsePayloadPolicy("off", "false", "true", "", "")
	require.False(t, p.MLNodeCaptureEnabled())
	require.True(t, p.QuarantineCaptureEnabled())
}

func TestPromptSHA256_Stable(t *testing.T) {
	a := observability.PromptSHA256([]byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	b := observability.PromptSHA256([]byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	require.Equal(t, a, b)
	require.Len(t, a, 64)
}

func TestFormatPayloadBodies_Hash(t *testing.T) {
	req := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	resp := []byte(`data: {"error":{"message":"boom"}}`)
	fields := observability.FormatPayloadBodies(observability.PayloadLevelHash, 16384, req, resp)
	m := kvMap(fields)
	require.Equal(t, len(req), m["request_bytes"])
	require.Equal(t, len(resp), m["response_bytes"])
	require.Equal(t, observability.PromptSHA256(req), m["devshard.prompt.sha256"])
	require.Equal(t, observability.BodySHA256(resp), m["response_sha256"])
	_, hasReq := m["request"]
	_, hasResp := m["response"]
	require.False(t, hasReq)
	require.False(t, hasResp)
}

func TestFormatPayloadBodies_FullTruncates(t *testing.T) {
	req := []byte(strings.Repeat("a", 100))
	fields := observability.FormatPayloadBodies(observability.PayloadLevelFull, 20, req, nil)
	m := kvMap(fields)
	require.Equal(t, true, m["request_truncated"])
	require.LessOrEqual(t, len(m["request"].(string)), 20)
}

func TestFormatPayloadBodies_RedactedMasksEmail(t *testing.T) {
	req := []byte(`{"messages":[{"role":"user","content":"contact me at user@example.com please"}]}`)
	fields := observability.FormatPayloadBodies(observability.PayloadLevelRedacted, 16384, req, nil)
	m := kvMap(fields)
	require.Contains(t, m["request"].(string), "[redacted]")
	require.NotContains(t, m["request"].(string), "user@example.com")
}

// Slicing before masking must still catch a PII token that straddles the cut,
// otherwise redaction would emit the first half of an email address.
func TestFormatPayloadBodies_RedactedMasksPIIStraddlingTheCut(t *testing.T) {
	req := []byte("please write to alice.smith@example.com about " + strings.Repeat("x", 4096) + " thanks")
	fields := observability.FormatPayloadBodies(observability.PayloadLevelRedacted, 16384, req, nil)
	body := kvMap(fields)["request"].(string)
	require.Contains(t, body, "[redacted]")
	require.NotContains(t, body, "alice.smith")
	require.NotContains(t, body, "@example.com")
}

func TestFormatPayloadBodies_RedactedKeepsHeadAndTail(t *testing.T) {
	req := []byte("BEGIN-MARKER " + strings.Repeat("x", 8192) + " END-MARKER")
	fields := observability.FormatPayloadBodies(observability.PayloadLevelRedacted, 16384, req, nil)
	body := kvMap(fields)["request"].(string)
	require.True(t, strings.HasPrefix(body, "BEGIN-MARKER"))
	require.True(t, strings.HasSuffix(body, "END-MARKER"))
	require.Contains(t, body, "…[redacted]…")
}

// Multi-byte runes must survive both the head/tail slice and the byte cap.
func TestFormatPayloadBodies_RedactedDoesNotSplitRunes(t *testing.T) {
	req := []byte(strings.Repeat("日本語テキスト", 512))
	fields := observability.FormatPayloadBodies(observability.PayloadLevelRedacted, 40, req, nil)
	body := kvMap(fields)["request"].(string)
	require.True(t, utf8.ValidString(body), "redacted output must stay valid UTF-8")
	require.LessOrEqual(t, len(body), 40)
}

func TestFormatPayloadBodies_RedactedReportsDroppedMiddle(t *testing.T) {
	resp := []byte(strings.Repeat("z", 64*1024))
	fields := observability.FormatPayloadBodies(observability.PayloadLevelRedacted, 16384, nil, resp)
	m := kvMap(fields)
	require.Equal(t, true, m["response_truncated"], "dropping the middle is truncation")
	require.Less(t, len(m["response"].(string)), 200)
}

func TestFormatPayloadBodies_RedactedShortBodyNotTruncated(t *testing.T) {
	resp := []byte("short body")
	fields := observability.FormatPayloadBodies(observability.PayloadLevelRedacted, 16384, nil, resp)
	m := kvMap(fields)
	require.Equal(t, false, m["response_truncated"])
	require.Equal(t, "short body", m["response"])
}

func TestCountChatMessages(t *testing.T) {
	require.Equal(t, 2, observability.CountChatMessages([]byte(`{"messages":[{"role":"system"},{"role":"user"}]}`)))
	require.Equal(t, 0, observability.CountChatMessages([]byte(`not-json`)))
}

func TestFilterResponseHeaders(t *testing.T) {
	h := observability.FilterResponseHeaders(map[string][]string{
		"Content-Type":         {"application/json"},
		"X-Devshard-Signature": {"secret"},
		"X-Request-Id":         {"req-1"},
		"Authorization":        {"Bearer x"},
		// Envoy sends this lowercase; the allowlist holds the canonical form.
		"x-envoy-upstream-service-time": {"42"},
	})
	require.Equal(t, "application/json", h["Content-Type"])
	require.Equal(t, "req-1", h["X-Request-Id"])
	require.Equal(t, "42", h["X-Envoy-Upstream-Service-Time"])
	_, hasSig := h["X-Devshard-Signature"]
	require.False(t, hasSig)
}

func TestAddPayloadCaptured(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	ctx, span := tp.Tracer("test").Start(t.Context(), "attempt")
	_ = ctx
	observability.AddPayloadCaptured(span, "abc123")
	span.End()
	spans := sr.Ended()
	require.Len(t, spans, 1)
	evs := spans[0].Events()
	require.Len(t, evs, 1)
	require.Equal(t, observability.EventPayloadCaptured, evs[0].Name)
	found := false
	for _, a := range evs[0].Attributes {
		if a.Key == attribute.Key("devshard.prompt.sha256") && a.Value.AsString() == "abc123" {
			found = true
		}
	}
	require.True(t, found)
}

func kvMap(fields []any) map[string]any {
	out := make(map[string]any)
	for i := 0; i+1 < len(fields); i += 2 {
		k, ok := fields[i].(string)
		if !ok {
			continue
		}
		out[k] = fields[i+1]
	}
	return out
}
