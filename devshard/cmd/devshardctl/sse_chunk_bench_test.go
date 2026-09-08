package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// benchChunk is a recorded logprob-bearing chunk, wrapped the way a host puts it on the wire.
func benchChunk(b *testing.B, fixture string) []byte {
	b.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		b.Fatalf("reading %s: %v", fixture, err)
	}
	return []byte("data: " + strings.Join(strings.Fields(string(body)), "") + "\n\n")
}

// BenchmarkScanSSEChunkOnePass is the write path's cost per chunk today: one walk, one decode.
func BenchmarkScanSSEChunkOnePass(b *testing.B) {
	chunk := benchChunk(b, "logprobs_token_ids.json")
	b.ReportAllocs()
	for b.Loop() {
		_ = scanSSEChunk(chunk)
	}
}

// BenchmarkScanSSEChunkTwoPasses reproduces the cost of asking the same chunk the two questions
// separately, which is what the write path did before the scans were merged.
func BenchmarkScanSSEChunkTwoPasses(b *testing.B) {
	chunk := benchChunk(b, "logprobs_token_ids.json")
	b.ReportAllocs()
	for b.Loop() {
		_, _ = sseChunkContentSource(chunk)
		_, _ = sseChunkLogprobsDecoded(chunk)
	}
}
