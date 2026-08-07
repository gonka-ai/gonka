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
	if maxBytes <= 0 {
		maxBytes = DefaultPayloadMaxBytes
	}
	fields := make([]any, 0, 12)
	fields = append(fields, "request_bytes", len(request))
	if len(request) > 0 {
		fields = append(fields, "devshard.prompt.sha256", PromptSHA256(request))
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

func redactBody(raw []byte, maxBytes int) (string, bool) {
	s := string(raw)
	s = maskPII(s)
	head, mid, tail := splitHeadTail(s, redactHeadRunes, redactTailRunes)
	out := head
	if mid {
		out += "…[redacted]…"
	}
	out += tail
	return truncateBytes(out, maxBytes)
}

func maskPII(s string) string {
	for _, re := range piiPatterns {
		s = re.ReplaceAllString(s, "[redacted]")
	}
	return s
}

func splitHeadTail(s string, headN, tailN int) (head string, mid bool, tail string) {
	runes := []rune(s)
	if len(runes) <= headN+tailN {
		return s, false, ""
	}
	return string(runes[:headN]), true, string(runes[len(runes)-tailN:])
}

func truncateBytes(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}
	// Avoid splitting a multi-byte rune at the cut.
	cut := maxBytes
	for cut > 0 && !utf8.ValidString(s[:cut]) {
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
	"Content-Type":             {},
	"Content-Length":           {},
	"X-Request-Id":             {},
	"X-Request-ID":             {},
	"Server":                   {},
	"X-Devshard-Error":         {},
	"Openai-Processing-Ms":     {},
	"X-Envoy-Upstream-Service": {},
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
