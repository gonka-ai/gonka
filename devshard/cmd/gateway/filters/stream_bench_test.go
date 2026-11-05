package filters

import "testing"

// realChunk is a production-shaped chunk carrying the logprobs, top_logprobs, and token_ids fields the rewriter strips.
const realChunk = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,` +
	`"model":"model-a","choices":[{"index":0,"delta":{"content":" the"},"finish_reason":null,` +
	`"logprobs":{"content":[{"token":" the","logprob":-0.31,"bytes":[32,116,104,101],` +
	`"top_logprobs":[{"token":" the","logprob":-0.31},{"token":" a","logprob":-2.1}]}]},` +
	`"token_ids":[262]}]}` + "\n\n"

func BenchmarkStreamRewriterRealChunk(b *testing.B) {
	chunk := []byte(realChunk)
	b.ReportAllocs()
	b.SetBytes(int64(len(chunk)))
	for range b.N {
		rewriter := NewStreamRewriter(LogprobIntent{}, true)
		if _, err := rewriter.Write(chunk); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRewriteEventRealChunk(b *testing.B) {
	event := []byte(realChunk[:len(realChunk)-2])
	b.ReportAllocs()
	for range b.N {
		_ = rewriteEventOnly(event, LogprobIntent{}, true)
	}
}

// productionStream returns a production-shaped SSE stream whose content chunks carry the fields the rewriter strips.
func productionStream(chunks int) [][]byte {
	events := make([][]byte, 0, chunks+2)
	events = append(events, []byte(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,`+
		`"model":"model-a","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`+"\n\n"))
	for range chunks {
		events = append(events, []byte(realChunk))
	}
	events = append(events, []byte(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,`+
		`"model":"model-a","choices":[],"usage":{"prompt_tokens":17,"completion_tokens":64,"total_tokens":81}}`+"\n\n"))
	return append(events, SSEDoneEvent)
}

func BenchmarkStreamRewriterProductionStream(b *testing.B) {
	events := productionStream(64)
	streamBytes := 0
	for _, event := range events {
		streamBytes += len(event)
	}
	b.ReportAllocs()
	b.SetBytes(int64(streamBytes))
	for b.Loop() {
		rewriter := NewStreamRewriter(LogprobIntent{}, false)
		for _, event := range events {
			if _, err := rewriter.Write(event); err != nil {
				b.Fatal(err)
			}
		}
		if _, err := rewriter.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEventPayload(b *testing.B) {
	event := []byte(realChunk[:len(realChunk)-2])
	b.ReportAllocs()
	for b.Loop() {
		if _, _, held := eventPayload(event); !held {
			b.Fatal("no data line")
		}
	}
}

// BenchmarkRewriteEventForcedUsage times stripping a forced usage field from the stream's final event.
func BenchmarkRewriteEventForcedUsage(b *testing.B) {
	event := []byte(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,` +
		`"model":"model-a","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":17,"completion_tokens":64,"total_tokens":81}}`)
	b.ReportAllocs()
	for b.Loop() {
		if rewriteEventOnly(event, LogprobIntent{}, false) == nil {
			b.Fatal("event dropped")
		}
	}
}
