package observability

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Payload capture policy (T4a). All capture is off unless explicitly enabled.

const (
	PayloadLevelOff      = "off"
	PayloadLevelHash     = "hash"
	PayloadLevelRedacted = "redacted"
	PayloadLevelFull     = "full"

	DefaultPayloadMaxBytes = 16384

	EventPayloadCaptured = "payload.captured"

	AttrPayloadSHA256 = attribute.Key("devshard.prompt.sha256")
)

// PayloadPolicy is the parsed DEVSHARD_LOG_PAYLOADS* configuration.
type PayloadPolicy struct {
	Level      string
	MLNode     bool
	Quarantine bool
	Validation bool // T4b stub; unused by T4a emitters
	MaxBytes   int
}

var (
	payloadPolicyOnce sync.Once
	payloadPolicy     PayloadPolicy
)

// LoadPayloadPolicy parses env knobs (cached for process lifetime).
func LoadPayloadPolicy() PayloadPolicy {
	payloadPolicyOnce.Do(func() {
		payloadPolicy = ParsePayloadPolicy(
			os.Getenv("DEVSHARD_LOG_PAYLOADS"),
			os.Getenv("DEVSHARD_LOG_PAYLOADS_MLNODE"),
			os.Getenv("DEVSHARD_LOG_PAYLOADS_QUARANTINE"),
			os.Getenv("DEVSHARD_LOG_PAYLOADS_VALIDATION"),
			os.Getenv("DEVSHARD_LOG_PAYLOADS_MAX_BYTES"),
		)
	})
	return payloadPolicy
}

// ResetPayloadPolicyForTest clears the cached policy (tests only).
func ResetPayloadPolicyForTest() {
	payloadPolicyOnce = sync.Once{}
	payloadPolicy = PayloadPolicy{}
}

// ParsePayloadPolicy builds a policy from raw env strings.
func ParsePayloadPolicy(level, mlnode, quarantine, validation, maxBytes string) PayloadPolicy {
	p := PayloadPolicy{
		Level:    PayloadLevelOff,
		MaxBytes: DefaultPayloadMaxBytes,
	}
	switch strings.ToLower(strings.TrimSpace(level)) {
	case PayloadLevelHash, PayloadLevelRedacted, PayloadLevelFull:
		p.Level = strings.ToLower(strings.TrimSpace(level))
	case "", PayloadLevelOff:
		p.Level = PayloadLevelOff
	default:
		p.Level = PayloadLevelOff
	}
	p.MLNode = parseEnvBool(mlnode)
	p.Quarantine = parseEnvBool(quarantine)
	p.Validation = parseEnvBool(validation)
	if raw := strings.TrimSpace(maxBytes); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			p.MaxBytes = n
		}
	}
	return p
}

func parseEnvBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// MLNodeCaptureEnabled reports whether ML-node failure payload lines should emit.
func (p PayloadPolicy) MLNodeCaptureEnabled() bool {
	return p.MLNode && p.Level != PayloadLevelOff
}

// QuarantineCaptureEnabled reports whether quarantine size lines should emit.
// Level-independent per plan §6.2.
func (p PayloadPolicy) QuarantineCaptureEnabled() bool {
	return p.Quarantine
}

// PromptSHA256 returns the hex SHA-256 of the request body.
func PromptSHA256(prompt []byte) string {
	sum := sha256.Sum256(prompt)
	return hex.EncodeToString(sum[:])
}

// BodySHA256 is an alias for hashing response samples.
func BodySHA256(body []byte) string {
	return PromptSHA256(body)
}

const (
	redactHeadRunes = 32
	redactTailRunes = 32
	// redactMaskWindow is the extra raw context masked around each kept slice.
	// Masking runs on the windows rather than the whole body, so a PII token
	// straddling a cut still has to be fully inside a masked region to be
	// caught; 256 bytes comfortably exceeds any pattern below.
	redactMaskWindow = 256
)

var piiPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`),
	regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
	regexp.MustCompile(`(?i)\b(sk|pk|api[_-]?key)[-_]?[A-Za-z0-9]{8,}\b`),
}

// FormatPayloadBodies returns slog fields for request/response bodies at the
// given level. Hash is always included when body is non-empty. Cap applies
// after redaction.
func FormatPayloadBodies(level string, maxBytes int, request, response []byte) []any {
	return FormatPayloadBodiesWithPromptHash(level, maxBytes, request, response, "")
}

// FormatPayloadBodiesWithPromptHash is FormatPayloadBodies for callers that
// already hashed the prompt for a span attribute; pass "" to hash here.
func FormatPayloadBodiesWithPromptHash(level string, maxBytes int, request, response []byte, promptHash string) []any {
	if maxBytes <= 0 {
		maxBytes = DefaultPayloadMaxBytes
	}
	fields := make([]any, 0, 12)
	fields = append(fields, "request_bytes", len(request))
	if len(request) > 0 {
		if promptHash == "" {
			promptHash = PromptSHA256(request)
		}
		fields = append(fields, "devshard.prompt.sha256", promptHash)
	}
	fields = append(fields, "response_bytes", len(response))
	if len(response) > 0 {
		fields = append(fields, "response_sha256", BodySHA256(response))
	}

	switch level {
	case PayloadLevelHash:
		// fingerprint + sizes only
	case PayloadLevelRedacted:
		if len(request) > 0 {
			body, trunc := redactBody(request, maxBytes)
			fields = append(fields, "request", body, "request_truncated", trunc)
		}
		if len(response) > 0 {
			body, trunc := redactBody(response, maxBytes)
			fields = append(fields, "response", body, "response_truncated", trunc)
		}
	case PayloadLevelFull:
		if len(request) > 0 {
			body, trunc := truncateBytes(string(request), maxBytes)
			fields = append(fields, "request", body, "request_truncated", trunc)
		}
		if len(response) > 0 {
			body, trunc := truncateBytes(string(response), maxBytes)
			fields = append(fields, "response", body, "response_truncated", trunc)
		}
	}
	return fields
}

// redactBody keeps a rune-bounded head and tail and drops everything between.
// It slices before masking: the output is ~64 runes regardless of input, so
// running the PII patterns over the whole body would cost three passes and a
// full copy of megabytes to produce a line of text.
func redactBody(raw []byte, maxBytes int) (string, bool) {
	headEnd := headRuneEnd(raw, redactHeadRunes)
	tailStart := tailRuneStart(raw, redactTailRunes)
	if headEnd >= tailStart {
		// Head and tail already cover the body; nothing to drop.
		return truncateBytes(maskPII(string(raw)), maxBytes)
	}
	head := []byte(maskPII(string(raw[:min(headEnd+redactMaskWindow, len(raw))])))
	tail := []byte(maskPII(string(raw[max(tailStart-redactMaskWindow, 0):])))
	out := string(head[:headRuneEnd(head, redactHeadRunes)]) +
		"…[redacted]…" +
		string(tail[tailRuneStart(tail, redactTailRunes):])
	out, _ = truncateBytes(out, maxBytes)
	// Dropping the middle loses far more than the byte cap ever does, so the
	// flag must report it; otherwise a 256 KiB body reads as untruncated.
	return out, true
}

func maskPII(s string) string {
	for _, re := range piiPatterns {
		s = re.ReplaceAllString(s, "[redacted]")
	}
	return s
}

// headRuneEnd returns the byte offset just past the first n runes of b, or
// len(b) when b holds fewer.
func headRuneEnd(b []byte, n int) int {
	off := 0
	for i := 0; i < n && off < len(b); i++ {
		_, size := utf8.DecodeRune(b[off:])
		off += size
	}
	return off
}

// tailRuneStart returns the byte offset where the last n runes of b begin, or
// 0 when b holds fewer.
func tailRuneStart(b []byte, n int) int {
	off := len(b)
	for i := 0; i < n && off > 0; i++ {
		_, size := utf8.DecodeLastRune(b[:off])
		off -= size
	}
	return off
}

func truncateBytes(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}
	// Avoid splitting a multi-byte rune at the cut. A rune is at most four
	// bytes, so this backs up at most three times; validating the whole prefix
	// instead would be quadratic in maxBytes.
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut <= 0 {
		return "", true
	}
	return s[:cut], true
}

// CountChatMessages returns the number of messages in an OpenAI-style body.
func CountChatMessages(prompt []byte) int {
	var parsed struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(prompt, &parsed); err != nil {
		return 0
	}
	return len(parsed.Messages)
}

// AddPayloadCaptured records a payload.captured span event with the prompt hash.
func AddPayloadCaptured(span trace.Span, hash string) {
	if span == nil || !span.IsRecording() || hash == "" {
		return
	}
	span.AddEvent(EventPayloadCaptured, trace.WithAttributes(AttrPayloadSHA256.String(hash)))
}

// ResponseHeaderAllowlist is the set of response headers retained for empty-body fallback.
var ResponseHeaderAllowlist = map[string]struct{}{
	"Content-Type":                  {},
	"Content-Length":                {},
	"X-Request-Id":                  {},
	"X-Request-ID":                  {},
	"Server":                        {},
	"X-Devshard-Error":              {},
	"Openai-Processing-Ms":          {},
	"X-Envoy-Upstream-Service-Time": {},
}

// FilterResponseHeaders keeps only allowlisted header keys (canonical MIME keys).
func FilterResponseHeaders(h map[string][]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string)
	for k, vals := range h {
		if _, ok := ResponseHeaderAllowlist[k]; !ok {
			// Also match case-insensitive common forms.
			canon := httpCanonicalHeader(k)
			if _, ok := ResponseHeaderAllowlist[canon]; !ok {
				continue
			}
			k = canon
		}
		if len(vals) == 0 {
			continue
		}
		out[k] = vals[0]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func httpCanonicalHeader(k string) string {
	// Minimal canonicalization without importing net/textproto for every call site.
	parts := strings.Split(k, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
	}
	return strings.Join(parts, "-")
}
