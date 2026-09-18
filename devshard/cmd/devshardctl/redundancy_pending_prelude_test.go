package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
)

const roleOnlyEvent = `data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n"

const contentEvent = `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"

func newPendingRaceWriter(t *testing.T, rg *raceGroup, nonce uint64) (*raceWriter, *inflight) {
	t.Helper()
	rw, inf := newErrorRaceWriter(t, rg, nonce)
	inf.participantPendingBytes = &atomic.Int64{}
	inf.participantAnswerBytes = &atomic.Int64{}
	t.Cleanup(func() { inf.releasePendingBuf() })
	return rw, inf
}

func assertPendingCountersDrained(t *testing.T) {
	t.Helper()
	beforePrelude := pendingPreludeBytes.Load()
	beforeAnswer := pendingAnswerBytes.Load()
	t.Cleanup(func() {
		require.Equal(t, beforePrelude, pendingPreludeBytes.Load(),
			"global prelude counter must return to its starting value")
		require.Equal(t, beforeAnswer, pendingAnswerBytes.Load(),
			"global answer counter must return to its starting value")
	})
}

func withPendingPreludeCap(t *testing.T, cap int) {
	t.Helper()
	old := maxPendingPreludeBytes
	maxPendingPreludeBytes = cap
	t.Cleanup(func() { maxPendingPreludeBytes = old })
}

func TestRaceWriter_PendingPreludeOverflowAbortsAttempt(t *testing.T) {
	assertPendingCountersDrained(t)
	withPendingPreludeCap(t, 4*len(roleOnlyEvent))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sink bytes.Buffer
	rw, inf := newPendingRaceWriter(t, newRaceGroup(ctx, ctx, "escrow-x", &sink), 1)

	attemptCtx, attemptCancel := context.WithCancel(context.Background())
	inf.cancel = attemptCancel
	defer attemptCancel()

	globalBefore := pendingPreludeBytes.Load()

	for i := 0; i < 50; i++ {
		n, err := rw.Write([]byte(roleOnlyEvent))
		require.NoError(t, err)
		require.Equal(t, len(roleOnlyEvent), n)
	}

	require.True(t, inf.preludeOverflow.Load(), "overflow must be flagged")
	require.Nil(t, inf.pendingBuf, "buffer must be released on overflow")
	require.Zero(t, inf.pendingReserved)
	require.Equal(t, globalBefore, pendingPreludeBytes.Load(), "global counter must be returned")
	require.Zero(t, inf.participantPendingBytes.Load(), "participant counter must be returned")
	require.Zero(t, rg0WinnerNonce(rw), "a content-free attempt never wins")

	select {
	case <-attemptCtx.Done():
	default:
		t.Fatal("overflow must cancel the attempt context")
	}
}

func rg0WinnerNonce(rw *raceWriter) uint64 { return rw.group.winnerNonce() }

func TestRaceWriter_PendingPreludeUnderCapIsForwardedByteExact(t *testing.T) {
	assertPendingCountersDrained(t)
	withPendingPreludeCap(t, 1<<20)

	ctx := context.Background()
	var sink bytes.Buffer
	rw, inf := newPendingRaceWriter(t, newRaceGroup(ctx, ctx, "escrow-x", &sink), 1)

	_, err := rw.Write([]byte(roleOnlyEvent))
	require.NoError(t, err)
	require.Equal(t, roleOnlyEvent, string(inf.pendingBuf))
	require.Equal(t, len(roleOnlyEvent), inf.pendingReserved)

	_, err = rw.Write([]byte(contentEvent))
	require.NoError(t, err)

	require.False(t, inf.preludeOverflow.Load())
	require.Equal(t, uint64(1), rw.group.winnerNonce())
	require.Equal(t, roleOnlyEvent+contentEvent, sink.String(),
		"the prelude must reach the client in order and byte-exact")

	require.Nil(t, inf.pendingBuf)
	require.Zero(t, inf.pendingReserved)
	require.Zero(t, inf.participantPendingBytes.Load())
}

func TestRaceWriter_PendingPreludeReleasedWhenAnotherAttemptWins(t *testing.T) {
	assertPendingCountersDrained(t)
	withPendingPreludeCap(t, 1<<20)

	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)

	rwLoser, loser := newPendingRaceWriter(t, rg, 1)
	rwWinner, _ := newPendingRaceWriter(t, rg, 2)

	globalBefore := pendingPreludeBytes.Load()

	_, err := rwLoser.Write([]byte(roleOnlyEvent))
	require.NoError(t, err)
	require.NotEmpty(t, loser.pendingBuf)

	_, err = rwWinner.Write([]byte(contentEvent))
	require.NoError(t, err)
	require.Equal(t, uint64(2), rg.winnerNonce())

	_, err = rwLoser.Write([]byte(roleOnlyEvent))
	require.NoError(t, err)

	require.Nil(t, loser.pendingBuf, "a suppressed loser must not retain bytes")
	require.Zero(t, loser.pendingReserved)
	require.Zero(t, loser.participantPendingBytes.Load())
	require.Equal(t, globalBefore, pendingPreludeBytes.Load())
}

func TestRaceWriter_PendingPreludeStopsAtSampleLimitWhenClientDetached(t *testing.T) {
	assertPendingCountersDrained(t)
	withPendingPreludeCap(t, 1<<20)

	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)
	rw, inf := newPendingRaceWriter(t, rg, 1)
	rg.detachClient()

	big := make([]byte, 64*1024)
	for i := range big {
		big[i] = 'a'
	}
	for i := 0; i < 64; i++ {
		_, err := rw.Write(big)
		require.NoError(t, err)
	}

	require.False(t, inf.preludeOverflow.Load(), "a detached client is not a host fault")
	require.LessOrEqual(t, len(inf.pendingBuf), emptyStreamBodySampleLimit)
	sample, _ := bodySampleForLog(inf.pendingBuf, emptyStreamBodySampleLimit)
	require.NotEmpty(t, sample, "the empty-stream diagnostic sample must survive")
}

func TestRaceWriter_PendingPreludeSharesParticipantBudgetAcrossAttempts(t *testing.T) {
	assertPendingCountersDrained(t)
	withPendingPreludeCap(t, 1<<20)

	oldParticipant := maxPendingPreludeParticipant
	maxPendingPreludeParticipant = int64(3 * len(roleOnlyEvent))
	t.Cleanup(func() { maxPendingPreludeParticipant = oldParticipant })

	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)

	shared := &atomic.Int64{}
	rwA, infA := newPendingRaceWriter(t, rg, 1)
	rwB, infB := newPendingRaceWriter(t, rg, 2)
	infA.participantPendingBytes = shared
	infB.participantPendingBytes = shared
	infA.cancel = func() {}
	infB.cancel = func() {}

	for i := 0; i < 2; i++ {
		_, err := rwA.Write([]byte(roleOnlyEvent))
		require.NoError(t, err)
	}
	for i := 0; i < 4; i++ {
		_, err := rwB.Write([]byte(roleOnlyEvent))
		require.NoError(t, err)
	}

	require.True(t, infB.preludeOverflow.Load(),
		"one participant must not exceed its shared budget by spreading across attempts")
	require.LessOrEqual(t, shared.Load(), maxPendingPreludeParticipant)
}

func bigRoleOnlyEvent(size int) []byte {
	filler := bytes.Repeat([]byte("a"), size)
	return []byte(fmt.Sprintf(
		`data: {"choices":[{"delta":{"role":"assistant"}}],"id":"%s"}`+"\n\n", filler))
}

func bigContentEvent(size int) []byte {
	filler := bytes.Repeat([]byte("a"), size)
	return []byte(fmt.Sprintf(
		`data: {"choices":[{"delta":{"content":"%s"}}]}`+"\n\n", filler))
}

func TestRaceWriter_UnboundedRoleOnlyStreamStaysBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("streams over 1 GiB through the race writer")
	}
	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-oom", &sink)
	rw, inf := newPendingRaceWriter(t, rg, 1)

	attemptCtx, attemptCancel := context.WithCancel(context.Background())
	inf.cancel = attemptCancel
	defer attemptCancel()

	event := bigRoleOnlyEvent(256 * 1024)
	const writes = 4096

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	logical := 0
	for i := 0; i < writes; i++ {
		n, err := rw.Write(event)
		require.NoError(t, err)
		require.Equal(t, len(event), n)
		logical += n
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	require.Greater(t, logical, 1<<30, "the attack must have streamed over 1 GiB logically")
	require.True(t, inf.preludeOverflow.Load(), "the stream must be rejected")
	require.Nil(t, inf.pendingBuf, "no pre-content bytes may be retained")
	require.Zero(t, inf.pendingReserved)
	require.Zero(t, inf.participantPendingBytes.Load())
	require.Zero(t, rg.winnerNonce(), "a content-free attempt never wins")

	select {
	case <-attemptCtx.Done():
	default:
		t.Fatal("the attempt must be cancelled so the host stops streaming")
	}

	growth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("logical_stream_bytes=%d retained_pending=%d heap_growth_bytes=%d",
		logical, len(inf.pendingBuf), growth)
	require.Less(t, growth, int64(8<<20),
		"heap growth must stay bounded regardless of how much the host streams")
}

func TestRaceWriter_ConcurrentRoleOnlyAttemptsStayBounded(t *testing.T) {
	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-oom", &sink)

	shared := &atomic.Int64{}
	event := bigRoleOnlyEvent(64 * 1024)

	for a := 0; a < 64; a++ {
		rw, inf := newPendingRaceWriter(t, rg, uint64(a+1))
		inf.participantPendingBytes = shared
		inf.cancel = func() {}
		for i := 0; i < 64; i++ {
			_, err := rw.Write(event)
			require.NoError(t, err)
		}
		require.LessOrEqual(t, len(inf.pendingBuf), maxPendingPreludeBytes)
	}

	require.LessOrEqual(t, shared.Load(), maxPendingPreludeParticipant,
		"one participant must never exceed its budget across concurrent attempts")
	require.LessOrEqual(t, pendingPreludeBytes.Load(), maxPendingPreludeGlobal)
}

func TestRaceWriter_SuspiciousFallbackAnswerSurvivesAttemptExit(t *testing.T) {
	assertPendingCountersDrained(t)
	withPendingPreludeCap(t, 1<<20)

	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)
	rw, inf := newPendingRaceWriter(t, rg, 1)
	inf.suspicious = true

	_, err := rw.Write([]byte(roleOnlyEvent))
	require.NoError(t, err)
	_, err = rw.Write([]byte(contentEvent))
	require.NoError(t, err)

	require.Zero(t, rg.winnerNonce(), "a suspicious attempt defers winner selection")
	require.True(t, inf.pendingBufPromotable(), "its answer must survive for fallback promotion")

	inf.releasePendingBufUnlessPromotable()
	require.NotEmpty(t, inf.pendingBuf, "the answer must not be cleared at attempt exit")

	require.Equal(t, inf, fallbackSuspiciousWinner([]*inflight{inf}))
	require.NoError(t, rg.promoteFallbackWinner(inf))
	require.Equal(t, roleOnlyEvent+contentEvent, sink.String(),
		"the promoted fallback answer must reach the client in full")
}

func TestRaceWriter_OverflowedAttemptCannotWinOrForward(t *testing.T) {
	assertPendingCountersDrained(t)
	withPendingPreludeCap(t, 2*len(roleOnlyEvent))

	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)
	rw, inf := newPendingRaceWriter(t, rg, 1)
	inf.cancel = func() {}

	for i := 0; i < 8; i++ {
		_, err := rw.Write([]byte(roleOnlyEvent))
		require.NoError(t, err)
	}
	require.True(t, inf.preludeOverflow.Load())

	_, err := rw.Write([]byte(contentEvent))
	require.NoError(t, err)
	_, err = rw.Write([]byte("data: [DONE]\n\n"))
	require.NoError(t, err)

	require.Zero(t, rg.winnerNonce(), "an overflowed attempt must never be crowned")
	require.Empty(t, sink.String(), "an overflowed attempt must never forward a truncated stream")
	require.Zero(t, inf.contentChunks.Load(), "writes after overflow must not be classified")
	require.Nil(t, fallbackSuspiciousWinner([]*inflight{inf}), "and must not be fallback-eligible")
}

func TestRaceWriter_SuspiciousLongAnswerIsNotChargedAsPrelude(t *testing.T) {
	assertPendingCountersDrained(t)
	withPendingPreludeCap(t, 64*1024)

	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)
	rw, inf := newPendingRaceWriter(t, rg, 1)
	inf.suspicious = true
	inf.cancel = func() {}

	chunk := bigContentEvent(64 * 1024)
	for i := 0; i < 64; i++ {
		_, err := rw.Write(chunk)
		require.NoError(t, err)
	}

	require.False(t, inf.preludeOverflow.Load(),
		"a complete suspicious answer must not be cancelled by the prelude cap")
	require.EqualValues(t, 64, inf.contentChunks.Load())
	require.Greater(t, len(inf.pendingBuf), maxPendingPreludeBytes)
}

func TestRaceWriter_GlobalPressureIsNotBlamedOnTheHost(t *testing.T) {
	assertPendingCountersDrained(t)
	withPendingPreludeCap(t, 1<<20)

	oldGlobal := maxPendingPreludeGlobal
	maxPendingPreludeGlobal = pendingPreludeBytes.Load() + 1
	t.Cleanup(func() { maxPendingPreludeGlobal = oldGlobal })

	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)
	rw, inf := newPendingRaceWriter(t, rg, 1)
	inf.cancel = func() {}

	_, err := rw.Write([]byte(roleOnlyEvent))
	require.NoError(t, err)

	require.True(t, inf.preludeOverflow.Load(), "retention must stop under global pressure")
	require.True(t, inf.preludePressure.Load(),
		"pool exhaustion must be recorded as gateway pressure, not host misconduct")
}

func TestRaceWriter_ParticipantCapIsStillHostMisconduct(t *testing.T) {
	assertPendingCountersDrained(t)
	withPendingPreludeCap(t, 1<<20)

	oldParticipant := maxPendingPreludeParticipant
	maxPendingPreludeParticipant = int64(len(roleOnlyEvent))
	t.Cleanup(func() { maxPendingPreludeParticipant = oldParticipant })

	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)
	shared := &atomic.Int64{}
	rw, inf := newPendingRaceWriter(t, rg, 1)
	inf.participantPendingBytes = shared
	inf.cancel = func() {}

	for i := 0; i < 4; i++ {
		_, err := rw.Write([]byte(roleOnlyEvent))
		require.NoError(t, err)
	}

	require.True(t, inf.preludeOverflow.Load())
	require.False(t, inf.preludePressure.Load(),
		"a participant exhausting its own budget is misconduct, not gateway pressure")
}

func TestRaceWriter_ZeroCapMeansUnlimitedNotAbortEverything(t *testing.T) {
	assertPendingCountersDrained(t)
	withPendingPreludeCap(t, 0)

	oldP, oldG := maxPendingPreludeParticipant, maxPendingPreludeGlobal
	maxPendingPreludeParticipant, maxPendingPreludeGlobal = 0, 0
	t.Cleanup(func() { maxPendingPreludeParticipant, maxPendingPreludeGlobal = oldP, oldG })

	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)
	rw, inf := newPendingRaceWriter(t, rg, 1)
	inf.cancel = func() { t.Fatal("a zero cap must not cancel attempts") }

	for i := 0; i < 32; i++ {
		_, err := rw.Write([]byte(roleOnlyEvent))
		require.NoError(t, err)
	}

	require.False(t, inf.preludeOverflow.Load(),
		"0 must read as unlimited, matching GATEWAY_MAX_CONCURRENT_REQUESTS=0 in the shipped config")
	require.Equal(t, 32*len(roleOnlyEvent), len(inf.pendingBuf))
}

const errorEvent = `data: {"error":{"code":500,"message":"boom","type":"InternalServerError"}}` + "\n\n"

func TestRaceWriter_ErrorEventDoesNotUnlockUnboundedRetention(t *testing.T) {
	assertPendingCountersDrained(t)
	withPendingPreludeCap(t, 4*len(roleOnlyEvent))

	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)
	rw, inf := newPendingRaceWriter(t, rg, 1)
	inf.cancel = func() {}

	_, err := rw.Write([]byte(errorEvent))
	require.NoError(t, err)
	require.Positive(t, inf.contentChunks.Load(), "an error event bumps contentChunks")
	require.False(t, inf.answerRetention.Load(), "but it is not a fallback answer")

	for i := 0; i < 64; i++ {
		_, err := rw.Write([]byte(roleOnlyEvent))
		require.NoError(t, err)
	}

	require.True(t, inf.preludeOverflow.Load(),
		"an error envelope must not unlock unbounded role-only retention")
	require.Nil(t, inf.pendingBuf)
	require.Zero(t, rg.winnerNonce())
}

func TestRaceWriter_SuspiciousAnswerBytesStayAccounted(t *testing.T) {
	assertPendingCountersDrained(t)
	withPendingPreludeCap(t, 1<<20)

	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)
	rw, inf := newPendingRaceWriter(t, rg, 1)
	inf.suspicious = true
	inf.cancel = func() {}

	chunk := bigContentEvent(64 * 1024)
	for i := 0; i < 32; i++ {
		_, err := rw.Write(chunk)
		require.NoError(t, err)
	}

	require.True(t, inf.answerRetention.Load())
	require.False(t, inf.preludeOverflow.Load())
	require.Equal(t, len(inf.pendingBuf), inf.pendingReserved+inf.answerReserved,
		"every retained answer byte must stay accounted")
	require.Positive(t, inf.answerReserved, "answer bytes belong to the answer pool")
	require.Equal(t, int64(inf.answerReserved), inf.participantAnswerBytes.Load())
}

func TestRaceWriter_AnswerRetentionIsStillBounded(t *testing.T) {
	assertPendingCountersDrained(t)
	withPendingPreludeCap(t, 1<<20)

	oldAnswer := maxPendingAnswerBytes
	maxPendingAnswerBytes = 256 * 1024
	t.Cleanup(func() { maxPendingAnswerBytes = oldAnswer })

	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)
	rw, inf := newPendingRaceWriter(t, rg, 1)
	inf.suspicious = true
	inf.cancel = func() {}

	chunk := bigContentEvent(64 * 1024)
	for i := 0; i < 32; i++ {
		_, err := rw.Write(chunk)
		require.NoError(t, err)
	}

	require.True(t, inf.preludeOverflow.Load(),
		"fallback-answer retention must have its own ceiling, not be unbounded")
}

func TestGatewayAttemptFailureReason_PressureIsNotHostEmptyStream(t *testing.T) {
	inf := &inflight{hostID: "h", escrowID: "e", nonce: 1,
		done: make(chan struct{}), receiptCh: make(chan struct{}), firstTokenCh: make(chan struct{})}
	inf.setReceiptAt(time.Now())
	inf.err = errGatewayPendingPressure
	inf.preludePressure.Store(true)

	require.True(t, isEmptyStreamAttempt(inf), "it would otherwise classify as empty_stream")
	require.Equal(t, "gateway_pending_pressure",
		gatewayAttemptFailureReason(inf, &staticFinishedSession{}, "m"),
		"gateway overload must not be attributed to the host")
}

func TestReleasePendingBufs_DrainsEveryAttemptOnFallbackFailure(t *testing.T) {
	assertPendingCountersDrained(t)
	withPendingPreludeCap(t, 1<<20)

	ctx := context.Background()
	failing := &errWriter{err: io.ErrClosedPipe}
	rg := newRaceGroup(ctx, ctx, "escrow-x", failing)

	rwA, infA := newPendingRaceWriter(t, rg, 1)
	rwB, infB := newPendingRaceWriter(t, rg, 2)
	for _, rw := range []*raceWriter{rwA, rwB} {
		rw.inf.suspicious = true
		_, err := rw.Write([]byte(roleOnlyEvent))
		require.NoError(t, err)
		_, err = rw.Write([]byte(contentEvent))
		require.NoError(t, err)
	}
	require.Positive(t, infA.pendingReserved)
	require.Positive(t, infB.pendingReserved)

	attempts := []*inflight{infA, infB}
	fallback := fallbackSuspiciousWinner(attempts)
	require.NotNil(t, fallback)
	require.Error(t, rg.promoteFallbackWinner(fallback), "the client write must fail")

	releasePendingBufs(attempts)

	require.Zero(t, infA.pendingReserved, "the fallback's reservation is released")
	require.Zero(t, infB.pendingReserved, "and so is every other attempt's")
}

type errWriter struct{ err error }

func (w *errWriter) Write([]byte) (int, error) { return 0, w.err }

func TestRaceWriter_AnswerRetentionDoesNotStarvePreludes(t *testing.T) {
	assertPendingCountersDrained(t)
	withPendingPreludeCap(t, 1<<20)

	const preludeHeadroom = 1024
	oldPG, oldPP := maxPendingPreludeGlobal, maxPendingPreludeParticipant
	maxPendingPreludeGlobal = pendingPreludeBytes.Load() + preludeHeadroom
	maxPendingPreludeParticipant = preludeHeadroom
	t.Cleanup(func() { maxPendingPreludeGlobal, maxPendingPreludeParticipant = oldPG, oldPP })

	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)

	shared := &atomic.Int64{}
	rwAnswer, answer := newPendingRaceWriter(t, rg, 1)
	answer.suspicious = true
	answer.participantPendingBytes = shared
	answer.cancel = func() {}

	_, err := rwAnswer.Write([]byte(contentEvent))
	require.NoError(t, err)
	for i := 0; i < 8; i++ {
		_, err = rwAnswer.Write(bigContentEvent(2048))
		require.NoError(t, err)
	}
	require.True(t, answer.answerRetention.Load())
	require.False(t, answer.preludeOverflow.Load())
	require.Greater(t, int64(answer.answerReserved), int64(preludeHeadroom),
		"the permitted answer must exceed the whole prelude budget")

	rwOther, other := newPendingRaceWriter(t, rg, 2)
	other.participantPendingBytes = shared
	other.cancel = func() { t.Fatal("a permitted answer must not abort an unrelated prelude") }

	_, err = rwOther.Write([]byte(roleOnlyEvent))
	require.NoError(t, err)

	require.False(t, other.preludeOverflow.Load(),
		"answer retention must not consume another attempt's prelude capacity")
	require.Equal(t, len(roleOnlyEvent), other.pendingReserved)
}

func TestOverflowedAttemptIsNeverASuccessfulSettlement(t *testing.T) {
	inf := &inflight{hostID: "h", escrowID: "e", nonce: 1,
		done: make(chan struct{}), receiptCh: make(chan struct{}), firstTokenCh: make(chan struct{})}
	inf.setReceiptAt(time.Now())
	inf.resp = &host.HostResponse{ConfirmedAt: 1}
	inf.contentChunks.Add(3)

	require.False(t, isFailedStreamAttempt(inf), "a content-bearing finished attempt normally succeeds")
	require.True(t, deliveredWholeAnswer(inf))

	inf.preludeOverflow.Store(true)

	require.True(t, isDiscardedOverflowAttempt(inf))
	require.True(t, isFailedStreamAttempt(inf),
		"a discarded overflow must never count as a delivered answer")
	require.False(t, deliveredWholeAnswer(inf),
		"zero bytes reached the client, so settlement must not record success")
}
