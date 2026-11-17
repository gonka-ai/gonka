package engine

import "testing"

// benchStreamChunk is the shape a host puts on the wire mid-answer: one rendered delta beside the
// logprobs the validator will replay it from.
var benchStreamChunk = []byte(`data: {"choices":[{"index":0,"delta":{"content":"the "},` +
	`"logprobs":{"content":[{"token":"1820","top_logprobs":[{"token":"279"},{"token":"264"}]},` +
	`{"token":"3230","top_logprobs":[{"token":"1495"},{"token":"4194"}]}]},"finish_reason":null}]}` + "\n\n")

// benchChunkShapes are the events one stream is made of, so a change that speeds the content chunk up at the cost of the others shows here.
var benchChunkShapes = []struct {
	name  string
	chunk []byte
}{
	{"content_with_logprobs", benchStreamChunk},
	{"content_plain", []byte(`data: {"choices":[{"index":0,"delta":{"content":"the "},"finish_reason":null}]}` + "\n\n")},
	{"role_only", []byte(`data: {"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n")},
	{"finish_only", []byte(`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n")},
	{"usage_only", []byte(`data: {"choices":[],"usage":{"completion_tokens":40}}` + "\n\n")},
}

// BenchmarkClassifyChunk is the write path's cost per chunk, one shape per event a stream carries.
func BenchmarkClassifyChunk(b *testing.B) {
	for _, shape := range benchChunkShapes {
		b.Run(shape.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = classifyChunk(shape.chunk, true)
			}
		})
	}
}
