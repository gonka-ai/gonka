package main

import (
	"context"
	"net/http/httptest"
	"testing"
)

// benchDeltaEvent is one vLLM delta event as the gateway receives it with forced logprobs and token ids.
const benchDeltaEvent = `data: {"id":"chatcmpl-5f2c","object":"chat.completion.chunk","created":1790000000,"model":"moonshotai/Kimi-K2.6","choices":[{"index":0,"delta":{"content":" user"},"logprobs":{"content":[{"token":" user","logprob":-0.0123,"bytes":[32,117,115,101,114],"top_logprobs":[{"token":" user","logprob":-0.0123,"bytes":[32,117,115,101,114]},{"token":" users","logprob":-4.51,"bytes":[32,117,115,101,114,115]},{"token":" client","logprob":-6.2,"bytes":[32,99,108,105,101,110,116]},{"token":" person","logprob":-7.9,"bytes":[32,112,101,114,115,111,110]},{"token":" customer","logprob":-8.3,"bytes":[32,99,117,115,116,111,109,101,114]}]}]},"finish_reason":null,"token_ids":[3100]}]}` + "\n\n"

func BenchmarkRewriteStreamingPayloadDeltaNoLogprobs(b *testing.B) {
	chunk := []byte(benchDeltaEvent)
	b.ReportAllocs()
	for b.Loop() {
		_ = rewriteStreamingPayload(chunk, clientResponseIntent{})
	}
}

func BenchmarkRewriteStreamingPayloadDeltaWithLogprobs(b *testing.B) {
	chunk := []byte(benchDeltaEvent)
	b.ReportAllocs()
	for b.Loop() {
		_ = rewriteStreamingPayload(chunk, clientResponseIntent{keepLogprobs: true, keepTopLogprobs: true, keepUsage: true})
	}
}

// BenchmarkGatewayStreamPath is one 200-token answer through the winner's raceWriter into the client's deferredWriter.
func BenchmarkGatewayStreamPath(b *testing.B) {
	event := []byte(benchDeltaEvent)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		client := newDeferredWriter(ctx, httptest.NewRecorder(), "escrow-bench", nil)
		group := newRaceGroup(ctx, ctx, "escrow-bench", client)
		inf := &inflight{hostID: "host-1", escrowID: "escrow-bench", nonce: 1, done: make(chan struct{}), receiptCh: make(chan struct{}), firstTokenCh: make(chan struct{})}
		writer := &raceWriter{group: group, nonce: 1, inf: inf}
		for range 200 {
			if _, err := writer.Write(event); err != nil {
				b.Fatal(err)
			}
			writer.Flush()
		}
	}
}
