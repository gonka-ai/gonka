package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func restoreAggregateCaps(t *testing.T) {
	t.Helper()
	prevMem, prevResp := currentAggregateByteLimits()
	prevConc := currentAggregateMaxConcurrentSpools()
	prevDegraded := currentAggregateMaxDegradedRAMBytes()
	prevDir := currentAggregateSpoolDir()
	prevSpoolMax, prevSpoolCur := aggregateSpoolSem.snapshot()
	prevDegMax, prevDegCur := aggregateDegradedSem.snapshot()
	t.Cleanup(func() {
		setAggregateByteLimits(prevMem, prevResp, prevConc, prevDegraded)
		setAggregateSpoolDir(prevDir)
		aggregateSpoolSem.restore(prevSpoolMax, prevSpoolCur)
		aggregateDegradedSem.restore(prevDegMax, prevDegCur)
	})
}

func TestAggregateResponseBuffer_MemoryOnlyCap(t *testing.T) {
	restoreAggregateCaps(t)
	setAggregateSpoolDir("")
	setAggregateByteLimitsForTest(64, 16<<20)

	buf := newAggregateResponseBuffer()
	defer func() { _ = buf.Close() }()
	require.Equal(t, int64(64), buf.maxBytes(), "without spool, ceiling is memory limit")

	_, err := buf.Write(bytes.Repeat([]byte("a"), 64))
	require.NoError(t, err)
	_, err = buf.Write([]byte("x"))
	require.ErrorIs(t, err, ErrAggregateResponseTooLarge)
	require.False(t, buf.Spilled())
}

func TestAggregateResponseBuffer_SpillsToDisk(t *testing.T) {
	restoreAggregateCaps(t)
	dir := t.TempDir()
	setAggregateSpoolDir(dir)
	resetAggregateSpoolSlots(8)
	setAggregateByteLimitsForTest(32, 256)

	buf := newAggregateResponseBuffer()
	defer func() { _ = buf.Close() }()

	_, err := buf.Write([]byte("hello "))
	require.NoError(t, err)
	require.False(t, buf.Spilled())

	_, err = buf.Write(bytes.Repeat([]byte("b"), 40))
	require.NoError(t, err)
	require.True(t, buf.Spilled(), "crossing mem limit must spill")

	// Spool files are unlinked at create time so a crash cannot leave plaintext.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries, "spool inode is anonymous (unlinked) while open")

	got, err := buf.Bytes()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(got), "hello "))
	require.Equal(t, 6+40, len(got))
	require.Equal(t, int64(46), buf.Len())

	require.NoError(t, buf.Close())
}

// Spooled writes go through a bufio.Writer, so the tail of a chunked stream can
// still be sitting in the write buffer when Bytes() / OpenReader runs.
func TestAggregateResponseBuffer_SpilledChunkedWritesRoundTrip(t *testing.T) {
	restoreAggregateCaps(t)
	setAggregateSpoolDir(t.TempDir())
	resetAggregateSpoolSlots(8)
	setAggregateByteLimitsForTest(32, 1<<20)

	buf := newAggregateResponseBuffer()
	defer func() { _ = buf.Close() }()

	var want bytes.Buffer
	for i := 0; i < 500; i++ {
		chunk := []byte(fmt.Sprintf("data: {\"i\":%d}\n\n", i))
		want.Write(chunk)
		_, err := buf.Write(chunk)
		require.NoError(t, err)
	}
	require.True(t, buf.Spilled())

	got, err := buf.Bytes()
	require.NoError(t, err)
	require.Equal(t, want.String(), string(got))
	require.Equal(t, int64(want.Len()), buf.Len())

	r, err := buf.OpenReader()
	require.NoError(t, err)
	fromReader, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, want.String(), string(fromReader))
}

func TestAggregateResponseBuffer_DiskCap(t *testing.T) {
	restoreAggregateCaps(t)
	setAggregateSpoolDir(t.TempDir())
	resetAggregateSpoolSlots(8)
	setAggregateByteLimitsForTest(16, 48)

	buf := newAggregateResponseBuffer()
	defer func() { _ = buf.Close() }()

	_, err := buf.Write(bytes.Repeat([]byte("c"), 48))
	require.NoError(t, err)
	require.True(t, buf.Spilled())

	_, err = buf.Write([]byte("!"))
	require.ErrorIs(t, err, ErrAggregateResponseTooLarge)
}

func TestAggregateResponseBuffer_SpillFailureDegradesToRAM(t *testing.T) {
	restoreAggregateCaps(t)
	// Point spool at a non-writable path so CreateTemp fails.
	bad := filepath.Join(t.TempDir(), "missing", "nested")
	setAggregateSpoolDir(bad)
	resetAggregateSpoolSlots(8)
	setAggregateByteLimitsForTest(16, 1024)

	before := aggregateSpillDegradeTotal.Load()
	buf := newAggregateResponseBuffer()
	defer func() { _ = buf.Close() }()
	require.Equal(t, int64(1024), buf.maxBytes())

	// Crossing mem limit degrades to RAM up to maxBytes instead of hard-failing.
	_, err := buf.Write(bytes.Repeat([]byte("d"), 20))
	require.NoError(t, err)
	require.False(t, buf.Spilled())
	require.True(t, buf.spillDisabled())
	require.Equal(t, before+1, aggregateSpillDegradeTotal.Load())

	_, err = buf.Write(bytes.Repeat([]byte("e"), 1000))
	require.NoError(t, err)
	require.Equal(t, int64(1020), buf.Len())

	_, err = buf.Write(bytes.Repeat([]byte("!"), 5))
	require.ErrorIs(t, err, ErrAggregateResponseTooLarge)
}

func TestAggregateResponseBuffer_SpoolConcurrencyCapDegrades(t *testing.T) {
	restoreAggregateCaps(t)
	setAggregateSpoolDir(t.TempDir())
	resetAggregateSpoolSlots(1)
	setAggregateByteLimitsForTest(16, 256)

	first := newAggregateResponseBuffer()
	defer func() { _ = first.Close() }()
	_, err := first.Write(bytes.Repeat([]byte("a"), 32))
	require.NoError(t, err)
	require.True(t, first.Spilled())

	before := aggregateSpoolCapDegradeTotal.Load()
	second := newAggregateResponseBuffer()
	defer func() { _ = second.Close() }()
	_, err = second.Write(bytes.Repeat([]byte("b"), 32))
	require.NoError(t, err)
	require.False(t, second.Spilled(), "at concurrency cap, degrade to RAM")
	require.True(t, second.spillDisabled())
	require.Equal(t, before+1, aggregateSpoolCapDegradeTotal.Load())

	got, err := second.Bytes()
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte("b"), 32), got)
}

// Spool exhaustion must not turn every further request into a maxBytes RAM
// allocation: past the degraded-RAM budget, buffers keep the memory ceiling.
func TestAggregateResponseBuffer_DegradedRAMBudgetIsBounded(t *testing.T) {
	restoreAggregateCaps(t)
	setAggregateSpoolDir(filepath.Join(t.TempDir(), "missing", "nested"))
	resetAggregateSpoolSlots(8)
	setAggregateByteLimitsForTest(16, 1024)
	resetAggregateDegradedSlots(1024, 1024) // exactly one degraded request

	first := newAggregateResponseBuffer()
	defer func() { _ = first.Close() }()
	_, err := first.Write(bytes.Repeat([]byte("a"), 512))
	require.NoError(t, err)
	require.True(t, first.spillDisabled())
	require.True(t, first.holdsDegradedSlot(), "first degrade claims the budget")
	require.Equal(t, int64(1024), first.maxBytes())

	before := aggregateDegradedRefusedTotal.Load()
	second := newAggregateResponseBuffer()
	defer func() { _ = second.Close() }()
	_, err = second.Write(bytes.Repeat([]byte("b"), 512))
	require.ErrorIs(t, err, ErrAggregateResponseTooLarge,
		"budget is spent, so this request keeps the 16 byte memory ceiling")
	require.False(t, second.holdsDegradedSlot())
	require.Equal(t, int64(16), second.maxBytes())
	require.Equal(t, before+1, aggregateDegradedRefusedTotal.Load())
}

func TestAggregateResponseBuffer_DegradedSlotReleasedOnClose(t *testing.T) {
	restoreAggregateCaps(t)
	setAggregateSpoolDir(filepath.Join(t.TempDir(), "missing", "nested"))
	resetAggregateSpoolSlots(8)
	setAggregateByteLimitsForTest(16, 1024)
	resetAggregateDegradedSlots(1024, 1024)

	first := newAggregateResponseBuffer()
	_, err := first.Write(bytes.Repeat([]byte("a"), 512))
	require.NoError(t, err)
	require.True(t, first.holdsDegradedSlot())
	require.NoError(t, first.Close())

	second := newAggregateResponseBuffer()
	defer func() { _ = second.Close() }()
	_, err = second.Write(bytes.Repeat([]byte("b"), 512))
	require.NoError(t, err, "closing the first buffer must return its share")
	require.True(t, second.holdsDegradedSlot())
}

// A buffer that rejected a write holds a prefix of the response, so it must
// never hand that prefix to the fold as if it were the whole answer.
func TestAggregateResponseBuffer_RejectedWriteIsSticky(t *testing.T) {
	restoreAggregateCaps(t)
	setAggregateSpoolDir("")
	setAggregateByteLimitsForTest(32, 32)

	buf := newAggregateResponseBuffer()
	defer func() { _ = buf.Close() }()

	_, err := buf.Write(bytes.Repeat([]byte("a"), 24))
	require.NoError(t, err)
	_, err = buf.Write(bytes.Repeat([]byte("b"), 24))
	require.ErrorIs(t, err, ErrAggregateResponseTooLarge)

	_, err = buf.Write([]byte("c"))
	require.ErrorIs(t, err, ErrAggregateResponseTooLarge, "later writes stay failed")

	_, err = buf.Bytes()
	require.ErrorIs(t, err, ErrAggregateResponseTooLarge, "must not return a truncated body")
	_, err = buf.OpenReader()
	require.ErrorIs(t, err, ErrAggregateResponseTooLarge, "must not fold a truncated body")
}

func TestAggregateResponseBuffer_WriteAfterClose(t *testing.T) {
	restoreAggregateCaps(t)
	setAggregateSpoolDir("")
	setAggregateByteLimitsForTest(64, 64)
	buf := newAggregateResponseBuffer()
	require.NoError(t, buf.Close())
	_, err := buf.Write([]byte("x"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed")
}

// Concurrent Write vs Close exercises the buffer mutex the way a late winner
// write can race the handleAggregated defer Close (paired with detachClient).
// Run under -race.
func TestAggregateResponseBuffer_ConcurrentWriteClose(t *testing.T) {
	restoreAggregateCaps(t)
	setAggregateSpoolDir(t.TempDir())
	resetAggregateSpoolSlots(8)
	setAggregateByteLimitsForTest(64, 1<<20)

	buf := newAggregateResponseBuffer()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chunk := []byte(fmt.Sprintf("data: {\"i\":%d,\"pad\":\"%s\"}\n\n", i, strings.Repeat("x", 40)))
			_, _ = buf.Write(chunk)
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = buf.Close()
	}()
	wg.Wait()

	// After Close, further writes must fail closed (not panic / corrupt).
	_, err := buf.Write([]byte("late"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed")
}

// Mirrors handleAggregated's post-RunInference path: spill the winner SSE body,
// OpenReader, fold with aggregateSSEStreamReader, write JSON to the client.
func TestHandleAggregatedPath_SpilledBodyFoldsViaOpenReader(t *testing.T) {
	restoreAggregateCaps(t)
	setAggregateSpoolDir(t.TempDir())
	resetAggregateSpoolSlots(8)
	setAggregateByteLimitsForTest(64, 1<<20)

	buf := newAggregateResponseBuffer()
	defer func() { _ = buf.Close() }()

	var wantBody bytes.Buffer
	// Enough small chunks to cross the mem ceiling and force a spill.
	chunks := []string{
		`{"id":"cmpl-spill","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
	}
	for i := 0; i < 40; i++ {
		chunks = append(chunks, fmt.Sprintf(
			`{"id":"cmpl-spill","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"w%d"},"finish_reason":null}]}`, i))
	}
	chunks = append(chunks,
		`{"id":"cmpl-spill","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
	)
	raw := sseData(chunks...)
	_, err := buf.Write(raw)
	require.NoError(t, err)
	require.True(t, buf.Spilled(), "body must spill so OpenReader hits the spool fd")
	wantBody.Write(raw)

	// Buffer already snapshotted the tiny mem ceiling; restore fold budgets so
	// aggregation is not rejected for an artificial spill-forcing limit.
	setAggregateByteLimitsForTest(2<<20, 1<<20)

	src, err := buf.OpenReader()
	require.NoError(t, err)
	assembled := aggregateSSEStreamReader(src, clientResponseIntent{})
	require.JSONEq(t, string(aggregateSSEStream(wantBody.Bytes(), clientResponseIntent{})), string(assembled))

	rec := httptest.NewRecorder()
	writeJSONPayload(rec, 200, assembled)
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "chat.completion", resp["object"])
	require.Equal(t, "cmpl-spill", resp["id"])
	msg := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	require.Equal(t, "stop", resp["choices"].([]any)[0].(map[string]any)["finish_reason"])
	require.True(t, strings.HasPrefix(msg["content"].(string), "w0"))
	require.Contains(t, msg["content"].(string), "w39")
}

func TestConfigureAggregateResponseFromEnv_SpoolDirMode0700(t *testing.T) {
	restoreAggregateCaps(t)
	base := t.TempDir()
	t.Setenv("GATEWAY_AGGREGATE_SPOOL_DIR", "")
	configureAggregateResponseFromEnv(base)
	dir := currentAggregateSpoolDir()
	require.NotEmpty(t, dir)
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestConfigureAggregateResponseFromEnv(t *testing.T) {
	restoreAggregateCaps(t)
	base := t.TempDir()
	t.Setenv("GATEWAY_AGGREGATE_MAX_RESPONSE_BYTES", "1048576")
	t.Setenv("GATEWAY_AGGREGATE_MAX_MEMORY_BYTES", "65536")
	t.Setenv("GATEWAY_AGGREGATE_MAX_CONCURRENT_SPOOLS", "4")
	t.Setenv("GATEWAY_AGGREGATE_SPOOL_DIR", "")

	configureAggregateResponseFromEnv(base)
	mem, resp := currentAggregateByteLimits()
	require.Equal(t, int64(1048576), resp)
	require.Equal(t, int64(65536), mem)
	require.Equal(t, 4, currentAggregateMaxConcurrentSpools())
	require.Equal(t, filepath.Join(base, aggregateSpoolDirName), currentAggregateSpoolDir())
	require.Equal(t, 4, aggregateSpoolSlotCapacity())

	// Zero / negative → defaults.
	t.Setenv("GATEWAY_AGGREGATE_MAX_RESPONSE_BYTES", "0")
	t.Setenv("GATEWAY_AGGREGATE_MAX_MEMORY_BYTES", "-1")
	t.Setenv("GATEWAY_AGGREGATE_MAX_CONCURRENT_SPOOLS", "0")
	configureAggregateResponseFromEnv(base)
	mem, resp = currentAggregateByteLimits()
	require.Equal(t, defaultAggregateMaxResponseBytes, resp)
	require.Equal(t, defaultAggregateMaxMemoryBytes, mem)
	require.Equal(t, defaultAggregateMaxConcurrentSpools, currentAggregateMaxConcurrentSpools())
}

func TestConfigureAggregateResponseFromEnv_ExplicitSpool(t *testing.T) {
	restoreAggregateCaps(t)
	spool := t.TempDir()
	// Leave a stale file; configure must clear agg-* leftovers.
	stale := filepath.Join(spool, "agg-stale.sse")
	require.NoError(t, os.WriteFile(stale, []byte("old"), 0o600))
	keep := filepath.Join(spool, "keep-me.txt")
	require.NoError(t, os.WriteFile(keep, []byte("x"), 0o600))

	t.Setenv("GATEWAY_AGGREGATE_SPOOL_DIR", spool)
	configureAggregateResponseFromEnv(t.TempDir())
	require.Equal(t, spool, currentAggregateSpoolDir())
	_, err := os.Stat(stale)
	require.True(t, errors.Is(err, os.ErrNotExist))
	_, err = os.Stat(keep)
	require.NoError(t, err)
}

func TestAggregateSpoolSlots_ResetKeepsInFlight(t *testing.T) {
	restoreAggregateCaps(t)
	resetAggregateSpoolSlots(1)
	require.True(t, tryAcquireAggregateSpoolSlot())
	resetAggregateSpoolSlots(1) // while held — must not free a second acquire
	require.False(t, tryAcquireAggregateSpoolSlot(), "in-flight holder still counts against max")
	releaseAggregateSpoolSlot()
	require.True(t, tryAcquireAggregateSpoolSlot())
	releaseAggregateSpoolSlot()
}

func TestAggregateDegradedSlots_ResetKeepsInFlight(t *testing.T) {
	restoreAggregateCaps(t)
	resetAggregateDegradedSlots(1024, 1024) // one slot
	require.True(t, tryAcquireAggregateDegradedSlot())
	resetAggregateDegradedSlots(1024, 1024)
	require.False(t, tryAcquireAggregateDegradedSlot(), "in-flight holder still counts against max")
	releaseAggregateDegradedSlot()
	require.True(t, tryAcquireAggregateDegradedSlot())
	releaseAggregateDegradedSlot()
}

func TestGatewayAttemptFailureReason_AggregateTooLarge(t *testing.T) {
	require.Equal(t, "aggregate_response_too_large",
		gatewayAttemptFailureReason(&inflight{err: ErrAggregateResponseTooLarge}, nil, ""))
	require.Equal(t, "aggregate_response_too_large",
		gatewayAttemptFailureReason(&inflight{err: fmt.Errorf("write: %w", ErrAggregateResponseTooLarge)}, nil, ""))
	require.Equal(t, "aggregate_fold_too_large",
		gatewayAttemptFailureReason(&inflight{err: ErrAggregateFoldTooLarge}, nil, ""))
}
