package main

import (
	"bytes"
	"context"
	"math/rand/v2"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Minimal vLLM-shaped SSE fixtures for raceWriter classification tests.
const sseContentStream = "" +
	`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"Qwen/Qwen3","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"Qwen/Qwen3","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"Qwen/Qwen3","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}]}` + "\n\n" +
	`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"Qwen/Qwen3","choices":[],"usage":{"prompt_tokens":3,"total_tokens":5,"completion_tokens":2}}` + "\n\n" +
	`data: [DONE]` + "\n\n"

const sseToolCallsStream = "" +
	`data: {"id":"chatcmpl-2","object":"chat.completion.chunk","created":2,"model":"Qwen/Qwen3","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"chatcmpl-2","object":"chat.completion.chunk","created":2,"model":"Qwen/Qwen3","choices":[{"index":0,"delta":{"tool_calls":[{"id":"call-1","type":"function","index":0,"function":{"name":"get_weather"}}]},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"chatcmpl-2","object":"chat.completion.chunk","created":2,"model":"Qwen/Qwen3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":\"sf\"}"}}]},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"chatcmpl-2","object":"chat.completion.chunk","created":2,"model":"Qwen/Qwen3","choices":[{"index":0,"delta":{"content":""},"finish_reason":"tool_calls"}]}` + "\n\n" +
	`data: {"id":"chatcmpl-2","object":"chat.completion.chunk","created":2,"model":"Qwen/Qwen3","choices":[],"usage":{"prompt_tokens":20,"total_tokens":40,"completion_tokens":20}}` + "\n\n" +
	`data: [DONE]` + "\n\n"

var sseEmbeddedFixtures = []struct {
	name       string
	body       string
	wantSource string
}{
	{"delta.content", sseContentStream, "delta.content"},
	{"delta.tool_calls", sseToolCallsStream, "delta.tool_calls"},
}

func TestSseBlobClassifierNotEmpty(t *testing.T) {
	for _, fx := range sseEmbeddedFixtures {
		t.Run(fx.name, func(t *testing.T) {
			src, ok := sseChunkContentSource([]byte(fx.body))
			require.True(t, ok, "blob classifier returned EMPTY")
			require.Equal(t, fx.wantSource, src)
		})
	}
}

// Regression: classifier must work for any chunk size, including sub-event.
func TestSseRaceWriterAllChunkSizes(t *testing.T) {
	chunkSizes := []int{1, 64, 256, 1024, 4096, 8192}
	for _, fx := range sseEmbeddedFixtures {
		for _, sz := range chunkSizes {
			t.Run(fx.name+"/chunk="+strconv.Itoa(sz), func(t *testing.T) {
				t.Parallel()
				inf := mkRaceWriterInflight(t)
				rw := mkRaceWriter(t, inf)
				body := []byte(fx.body)
				for i := 0; i < len(body); i += sz {
					end := i + sz
					if end > len(body) {
						end = len(body)
					}
					_, err := rw.Write(body[i:end])
					require.NoError(t, err)
				}
				require.False(t, isEmptyStreamAttempt(inf),
					"classified empty (cc=%d oc=%d)",
					inf.contentChunks.Load(), inf.outputChunks.Load())
				require.Equal(t, fx.wantSource, inf.contentSource)
			})
		}
	}
}

// Per-attempt cap: oversized newline-less stream is dropped, global counter balanced.
func TestSseRaceWriterClassifyCapDrops(t *testing.T) {
	before := classifyPartialBytes.Load()
	inf := mkRaceWriterInflight(t)
	rw := mkRaceWriter(t, inf)
	_, err := rw.Write(bytes.Repeat([]byte("x"), maxClassifyPartial+1))
	require.NoError(t, err)
	require.Equal(t, 0, len(inf.classifyPartial))
	require.Equal(t, before, classifyPartialBytes.Load())
}

// Realistic transport shape: arbitrary chunk boundaries from TLS/proxy flushes.
func TestSseRaceWriterRandomChunking(t *testing.T) {
	for fxIndex, fx := range sseEmbeddedFixtures {
		t.Run(fx.name, func(t *testing.T) {
			t.Parallel()
			// Own rng per subtest: *rand.Rand is not safe for concurrent use.
			rng := rand.New(rand.NewPCG(42, uint64(fxIndex)))
			inf := mkRaceWriterInflight(t)
			rw := mkRaceWriter(t, inf)
			body := []byte(fx.body)
			for i := 0; i < len(body); {
				sz := 1 + rng.IntN(64)
				end := i + sz
				if end > len(body) {
					end = len(body)
				}
				_, err := rw.Write(body[i:end])
				require.NoError(t, err)
				i = end
			}
			require.False(t, isEmptyStreamAttempt(inf),
				"classified empty (cc=%d oc=%d)",
				inf.contentChunks.Load(), inf.outputChunks.Load())
			require.Equal(t, fx.wantSource, inf.contentSource)
		})
	}
}

func mkRaceWriterInflight(t testing.TB) *inflight {
	t.Helper()
	return &inflight{
		hostID:       "fixture-host",
		escrowID:     "fixture-escrow",
		nonce:        1,
		done:         make(chan struct{}),
		receiptCh:    make(chan struct{}),
		firstTokenCh: make(chan struct{}),
		receiptTime:  time.Now(),
	}
}

func mkRaceWriter(t testing.TB, inf *inflight) *raceWriter {
	t.Helper()
	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, inf.escrowID, &sink)
	return &raceWriter{group: rg, nonce: inf.nonce, inf: inf}
}
