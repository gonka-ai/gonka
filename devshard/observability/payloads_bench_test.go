package observability

import (
	"bytes"
	"testing"
)

// benchStreamBody is a 256 KiB SSE sample, the per-attempt capture cap.
func benchStreamBody() []byte {
	chunk := []byte(`data: {"choices":[{"delta":{"content":"hello world "}}]}` + "\n")
	return bytes.Repeat(chunk, (256*1024)/len(chunk))
}

var benchPrompt = []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

// The redacted level emits ~64 runes whatever the input, so it must not scale
// with body size. It regressed to 125x the cost of full by masking first.
func BenchmarkFormatPayloadBodies(b *testing.B) {
	body := benchStreamBody()
	for _, level := range []string{PayloadLevelHash, PayloadLevelRedacted, PayloadLevelFull} {
		b.Run(level, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = FormatPayloadBodies(level, DefaultPayloadMaxBytes, benchPrompt, body)
			}
		})
	}
}

// Invalid UTF-8 before the cut used to force a rescan of the whole prefix.
func BenchmarkTruncateBytesInvalidUTF8(b *testing.B) {
	body := benchStreamBody()
	for i := DefaultPayloadMaxBytes - 384; i < DefaultPayloadMaxBytes; i++ {
		body[i] = 0x80
	}
	s := string(body)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = truncateBytes(s, DefaultPayloadMaxBytes)
	}
}
