package main

import (
	"bytes"
	"context"
	"math/rand/v2"
	"strconv"
	"sync/atomic"
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

// Stream whose final content event carries no trailing newline (truncated
// upstream, mid-event proxy close). Write only classifies up to the last '\n',
// so the "ok" event lands in classifyPartial and is recovered by flushClassify.
const sseNewlineLessFinalContent = "" +
	`data: {"id":"chatcmpl-3","object":"chat.completion.chunk","created":3,"model":"Qwen/Qwen3","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"chatcmpl-3","object":"chat.completion.chunk","created":3,"model":"Qwen/Qwen3","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`

// Same shape, but the newline-less final event is a non-retriable error.
const sseNewlineLessFinalError = "" +
	`data: {"error":{"message":"boom","type":"server_error","code":"internal"}}`

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

// replaceClassify drops the trailing fragment when it exceeds the per-attempt cap.
func TestSseRaceWriterReplaceClassifyPerAttemptCapDrops(t *testing.T) {
	before := classifyPartialBytes.Load()
	inf := mkRaceWriterInflight(t)
	rw := mkRaceWriter(t, inf)
	chunk := append([]byte("\n"), bytes.Repeat([]byte("x"), maxClassifyPartial+1)...)
	_, err := rw.Write(chunk)
	require.NoError(t, err)
	require.Equal(t, 0, len(inf.classifyPartial))
	require.Equal(t, before, classifyPartialBytes.Load())
}

// growClassify drops a newline-less chunk when the global cap is already reached.
func TestSseRaceWriterGrowClassifyGlobalCapDrops(t *testing.T) {
	before := classifyPartialBytes.Load()
	defer classifyPartialBytes.Store(before)
	classifyPartialBytes.Store(maxClassifyPartialGlobal)
	inf := mkRaceWriterInflight(t)
	rw := mkRaceWriter(t, inf)
	_, err := rw.Write([]byte("data: partial-no-newline"))
	require.NoError(t, err)
	require.Equal(t, 0, len(inf.classifyPartial))
	require.Equal(t, maxClassifyPartialGlobal, classifyPartialBytes.Load())
}

// replaceClassify drops the trailing fragment when the global cap is reached.
func TestSseRaceWriterReplaceClassifyGlobalCapDrops(t *testing.T) {
	before := classifyPartialBytes.Load()
	defer classifyPartialBytes.Store(before)
	classifyPartialBytes.Store(maxClassifyPartialGlobal)
	inf := mkRaceWriterInflight(t)
	rw := mkRaceWriter(t, inf)
	_, err := rw.Write([]byte("\ntail"))
	require.NoError(t, err)
	require.Equal(t, 0, len(inf.classifyPartial))
	require.Equal(t, maxClassifyPartialGlobal, classifyPartialBytes.Load())
}

// Dropping the buffer must free its backing array, not just shrink len — else
// real memory outlives the gauge and can exceed the global cap.
func TestSseRaceWriterDropReleasesBackingArray(t *testing.T) {
	before := classifyPartialBytes.Load()
	inf := mkRaceWriterInflight(t)
	rw := mkRaceWriter(t, inf)
	_, err := rw.Write(bytes.Repeat([]byte("x"), maxClassifyPartial-10))
	require.NoError(t, err)
	require.Greater(t, cap(inf.classifyPartial), 0)
	_, err = rw.Write(bytes.Repeat([]byte("x"), 20))
	require.NoError(t, err)
	require.Equal(t, 0, cap(inf.classifyPartial), "drop must release the backing array")
	require.Equal(t, before, classifyPartialBytes.Load())
}

// releaseClassifyPartial frees a len-0-but-large-cap buffer (the send-goroutine defer after a drop) and leaves the gauge unchanged when len is already 0.
func TestReleaseClassifyPartialFreesLenZeroBuffer(t *testing.T) {
	before := classifyPartialBytes.Load()
	inf := mkRaceWriterInflight(t)
	inf.classifyPartial = make([]byte, 0, maxClassifyPartial)
	inf.releaseClassifyPartial()
	require.Nil(t, inf.classifyPartial)
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

// Regression: a content event delivered as the final, newline-less write is
// stashed in classifyPartial during Write and would leave the attempt looking
// empty. flushClassify (run once the upstream stream ends) must recover it.
func TestSseRaceWriterFlushRecoversNewlineLessContent(t *testing.T) {
	inf := mkRaceWriterInflight(t)
	rw := mkRaceWriter(t, inf)

	_, err := rw.Write([]byte(sseNewlineLessFinalContent))
	require.NoError(t, err)
	// Before the flush the trailing content event is unclassified.
	require.True(t, isEmptyStreamAttempt(inf),
		"precondition: newline-less final event should be unclassified until flush")

	rw.flushClassify()

	require.False(t, isEmptyStreamAttempt(inf),
		"flush must classify the newline-less final content event")
	require.Equal(t, "delta.content", inf.contentSource)
	require.Equal(t, 0, len(inf.classifyPartial), "flush must release the buffer")
}

// Regression: flushClassifyAndCheckEmpty must flush the buffered final event before reading contentChunks (a prior deferred flush ran after the decision, misclassifying content as empty).
func TestFlushClassifyAndCheckEmptyFlushesBeforeDeciding(t *testing.T) {
	inf := mkRaceWriterInflight(t)
	rw := mkRaceWriter(t, inf)

	_, err := rw.Write([]byte(sseNewlineLessFinalContent))
	require.NoError(t, err)
	require.True(t, isEmptyStreamAttempt(inf),
		"precondition: unflushed newline-less final event looks empty")

	require.False(t, rw.flushClassifyAndCheckEmpty(),
		"must flush the buffered final content before the empty-stream decision")
	require.Equal(t, "delta.content", inf.contentSource)
}

// Regression: a newline-less final error event must resolve to an error stream, not empty, through the same flush-before-decide path.
func TestFlushClassifyAndCheckEmptyFinalErrorIsNotEmpty(t *testing.T) {
	inf := mkRaceWriterInflight(t)
	rw := mkRaceWriter(t, inf)

	_, err := rw.Write([]byte(sseNewlineLessFinalError))
	require.NoError(t, err)

	require.False(t, rw.flushClassifyAndCheckEmpty(),
		"final error event must not be classified as empty_stream")
	require.True(t, isErrorStreamAttempt(inf))
}

// Regression: the same recovery must apply to a newline-less final error event
// so the attempt is classified as an error stream, not an empty one.
func TestSseRaceWriterFlushRecoversNewlineLessError(t *testing.T) {
	inf := mkRaceWriterInflight(t)
	rw := mkRaceWriter(t, inf)

	_, err := rw.Write([]byte(sseNewlineLessFinalError))
	require.NoError(t, err)

	rw.flushClassify()

	require.True(t, isErrorStreamAttempt(inf), "flush must classify the final error event")
	require.False(t, isEmptyStreamAttempt(inf))
	require.Equal(t, "error.server_error", inf.errorSource)
}

// flushClassify is a no-op when nothing was buffered (normal newline-terminated
// stream) and stays balanced against the global counter.
func TestSseRaceWriterFlushNoBufferedTail(t *testing.T) {
	before := classifyPartialBytes.Load()
	inf := mkRaceWriterInflight(t)
	rw := mkRaceWriter(t, inf)

	_, err := rw.Write([]byte(sseContentStream))
	require.NoError(t, err)
	require.False(t, isEmptyStreamAttempt(inf))
	source := inf.contentSource
	contentChunks := inf.contentChunks.Load()

	rw.flushClassify()

	require.Equal(t, source, inf.contentSource, "flush must not change an already-classified attempt")
	require.Equal(t, contentChunks, inf.contentChunks.Load(), "flush must not double-count content")
	require.Equal(t, before, classifyPartialBytes.Load())
}

// A probe attempt never classifies content, so flushClassify must leave it untouched.
func TestSseRaceWriterFlushProbeNoop(t *testing.T) {
	inf := mkRaceWriterInflight(t)
	inf.probe = true
	rw := mkRaceWriter(t, inf)

	_, err := rw.Write([]byte(sseNewlineLessFinalContent))
	require.NoError(t, err)

	rw.flushClassify()

	require.Equal(t, int64(0), inf.contentChunks.Load(), "probe flush must not count content")
	require.Equal(t, "", inf.contentSource)
}

// On a cap drop, the raw write chunk is still classified best-effort rather than
// silently skipped — a complete content line survives even when it can't buffer.
func TestSseRaceWriterGracefulDegradationOnCapDrop(t *testing.T) {
	inf := mkRaceWriterInflight(t)
	rw := mkRaceWriter(t, inf)
	_, err := rw.Write(bytes.Repeat([]byte("x"), maxClassifyPartial-10))
	require.NoError(t, err)
	_, err = rw.Write([]byte(`data: {"choices":[{"delta":{"content":"ok"}}]}`))
	require.NoError(t, err)
	require.False(t, isEmptyStreamAttempt(inf), "capped content chunk must still classify")
	require.Equal(t, "delta.content", inf.contentSource)
}

// A hostile participant that fills its own budget must not starve another
// attempt from a different participant sharing the global pool.
func TestSseRaceWriterParticipantCapIsolatesParticipants(t *testing.T) {
	restore := saveClassifyCaps()
	defer restore()
	maxClassifyPartial = 100
	maxClassifyPartialParticipant = 150
	maxClassifyPartialGlobal = 1 << 30

	hostile := &atomic.Int64{}
	honest := &atomic.Int64{}
	infHostile := mkRaceWriterInflight(t)
	infHostile.participantClassifyBytes = hostile
	infHonest := mkRaceWriterInflight(t)
	infHonest.participantClassifyBytes = honest

	_, err := mkRaceWriter(t, infHostile).Write(bytes.Repeat([]byte("x"), 100))
	require.NoError(t, err)
	require.Equal(t, int64(100), hostile.Load())

	// The honest participant still has its full budget available.
	_, err = mkRaceWriter(t, infHonest).Write(bytes.Repeat([]byte("y"), 100))
	require.NoError(t, err)
	require.Equal(t, 100, len(infHonest.classifyPartial), "honest attempt must not be starved")
	require.Equal(t, int64(100), honest.Load())
}

// The same participant is capped across concurrent attempts sharing its counter.
func TestSseRaceWriterParticipantCapDropsOverBudget(t *testing.T) {
	restore := saveClassifyCaps()
	defer restore()
	maxClassifyPartial = 100
	maxClassifyPartialParticipant = 150
	maxClassifyPartialGlobal = 1 << 30

	shared := &atomic.Int64{}
	first := mkRaceWriterInflight(t)
	first.participantClassifyBytes = shared
	second := mkRaceWriterInflight(t)
	second.participantClassifyBytes = shared

	_, err := mkRaceWriter(t, first).Write(bytes.Repeat([]byte("x"), 100))
	require.NoError(t, err)
	_, err = mkRaceWriter(t, second).Write(bytes.Repeat([]byte("y"), 100))
	require.NoError(t, err)

	require.Equal(t, 0, len(second.classifyPartial), "participant cap must drop the over-budget attempt")
	require.Equal(t, int64(100), shared.Load(), "over-budget bytes rolled back")
}

// A global-cap drop must roll back the participant counter it already charged.
func TestSseRaceWriterGlobalCapRollsBackParticipant(t *testing.T) {
	restore := saveClassifyCaps()
	defer restore()
	maxClassifyPartial = 1000
	maxClassifyPartialParticipant = 1000
	maxClassifyPartialGlobal = 50

	participant := &atomic.Int64{}
	inf := mkRaceWriterInflight(t)
	inf.participantClassifyBytes = participant
	before := classifyPartialBytes.Load()

	_, err := mkRaceWriter(t, inf).Write(bytes.Repeat([]byte("x"), 100))
	require.NoError(t, err)

	require.Equal(t, 0, len(inf.classifyPartial), "global cap drops the attempt")
	require.Equal(t, int64(0), participant.Load(), "participant counter rolled back")
	require.Equal(t, before, classifyPartialBytes.Load())
}

// Releasing the buffer decrements both the global and participant counters.
func TestReleaseClassifyPartialDecrementsParticipant(t *testing.T) {
	restore := saveClassifyCaps()
	defer restore()
	maxClassifyPartial = 1000
	maxClassifyPartialParticipant = 1000
	maxClassifyPartialGlobal = 1 << 30

	participant := &atomic.Int64{}
	inf := mkRaceWriterInflight(t)
	inf.participantClassifyBytes = participant

	_, err := mkRaceWriter(t, inf).Write(bytes.Repeat([]byte("x"), 100))
	require.NoError(t, err)
	require.Equal(t, int64(100), participant.Load())

	inf.releaseClassifyPartial()

	require.Equal(t, int64(0), participant.Load(), "release decrements participant counter")
	require.Nil(t, inf.classifyPartial)
}

func TestConfigureClassifyCapsFromEnv(t *testing.T) {
	restore := saveClassifyCaps()
	defer restore()
	t.Setenv("GATEWAY_CLASSIFY_MAX_ATTEMPT_BYTES", "2048")
	t.Setenv("GATEWAY_CLASSIFY_MAX_PARTICIPANT_BYTES", "4096")
	t.Setenv("GATEWAY_CLASSIFY_MAX_GLOBAL_BYTES", "8192")

	configureClassifyCapsFromEnv()

	require.Equal(t, 2048, maxClassifyPartial)
	require.Equal(t, int64(4096), maxClassifyPartialParticipant)
	require.Equal(t, int64(8192), maxClassifyPartialGlobal)
}

// saveClassifyCaps snapshots the tunable caps and returns a restore func.
func saveClassifyCaps() func() {
	attempt, participant, global := maxClassifyPartial, maxClassifyPartialParticipant, maxClassifyPartialGlobal
	return func() {
		maxClassifyPartial = attempt
		maxClassifyPartialParticipant = participant
		maxClassifyPartialGlobal = global
	}
}

func mkRaceWriterInflight(t testing.TB) *inflight {
	t.Helper()
	inf := &inflight{
		hostID:       "fixture-host",
		escrowID:     "fixture-escrow",
		nonce:        1,
		done:         make(chan struct{}),
		receiptCh:    make(chan struct{}),
		firstTokenCh: make(chan struct{}),
	}
	inf.setReceiptAt(time.Now())
	return inf
}

func mkRaceWriter(t testing.TB, inf *inflight) *raceWriter {
	t.Helper()
	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, inf.escrowID, &sink)
	return &raceWriter{group: rg, nonce: inf.nonce, inf: inf}
}
