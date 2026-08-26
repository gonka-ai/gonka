package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/completionapi"

	"devshard/host"
	"devshard/types"
	"devshard/user"
)

const auditErrLine = `data: {"error":{"code":500,"message":"boom","type":"InternalServerError"},"id":"devshard-1-1"}`

func TestShouldRunHandleTimeout_ErrorStreamRunsWhenFinished(t *testing.T) {
	fake := &staticFinishedSession{finished: true}
	inf := &inflight{nonce: 7, errorSource: "error.InternalServerError", errorTerminal: true}

	require.True(t, errorMissEnabledFor(inf))
	require.True(t, shouldRunHandleTimeoutOn(inf, fake, true), "terminal error miss runs HandleTimeout even if the nonce finished")
	require.False(t, shouldRunHandleTimeoutOn(inf, fake, false), "non-error-miss path still skips a finished nonce")

	happy := &inflight{nonce: 7}
	require.False(t, errorMissEnabledFor(happy))
	require.False(t, shouldRunHandleTimeoutOn(happy, fake, false), "non-error finished nonce still skips")
}

type staticFinishedSession struct {
	finished bool
}

func (s *staticFinishedSession) IsNonceFinished(uint64) bool { return s.finished }

func TestShouldRunHandleTimeout_NilGuards(t *testing.T) {
	require.False(t, shouldRunHandleTimeout(nil, nil))
	require.False(t, shouldRunHandleTimeoutOn(&inflight{probe: true, errorSource: "error.x"}, &staticFinishedSession{}, true))
}

func newErrorRaceWriter(t *testing.T, rg *raceGroup, nonce uint64) (*raceWriter, *inflight) {
	t.Helper()
	inf := &inflight{
		hostID: "error-host", escrowID: "escrow-x", nonce: nonce,
		done: make(chan struct{}), receiptCh: make(chan struct{}), firstTokenCh: make(chan struct{}),
	}
	inf.setReceiptAt(time.Now())
	return &raceWriter{group: rg, nonce: nonce, inf: inf}, inf
}

func TestRaceWriter_RetainsErrorStreamLines(t *testing.T) {
	ctx := context.Background()
	var sink bytes.Buffer
	rw, inf := newErrorRaceWriter(t, newRaceGroup(ctx, ctx, "escrow-x", &sink), 1)

	role := []byte(`data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n")
	errEvt := []byte(auditErrLine + "\n\n")
	done := []byte("data: [DONE]\n\n")
	_, err := rw.Write(role)
	require.NoError(t, err)

	_, err = rw.Write(errEvt)
	require.NoError(t, err)
	require.NotEmpty(t, inf.errorStreamLines)
	_, err = rw.Write(done)
	require.NoError(t, err)
	require.True(t, inf.errorStreamComplete)
	require.Equal(t, []string{
		`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
		auditErrLine,
		`data: [DONE]`,
	}, inf.errorStreamLines)

	_, payload := errorMissArtifacts(inf, nil)
	var decoded completionapi.SerializedStreamedResponse
	require.NoError(t, json.Unmarshal(payload, &decoded))
	require.Equal(t, inf.errorStreamLines, decoded.Events)
}

func TestRaceWriter_HappyPathContentClearsRetention(t *testing.T) {
	ctx := context.Background()
	var sink bytes.Buffer
	rw, inf := newErrorRaceWriter(t, newRaceGroup(ctx, ctx, "escrow-x", &sink), 1)

	_, err := rw.Write([]byte(`data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n"))
	require.NoError(t, err)
	_, err = rw.Write([]byte(`data: {"choices":[{"delta":{"content":"ok"}}]}` + "\n\n"))
	require.NoError(t, err)
	_, err = rw.Write([]byte("data: [DONE]\n\n"))
	require.NoError(t, err)
	rw.flushClassify()
	require.Empty(t, inf.errorStreamLines, "a stream that never errored must drop retention at flush")
	require.False(t, inf.errorStreamTruncated)
}

func TestRaceWriter_FragmentedDeliveryAfterRetention(t *testing.T) {
	ctx := context.Background()
	var sink bytes.Buffer
	rw, inf := newErrorRaceWriter(t, newRaceGroup(ctx, ctx, "escrow-x", &sink), 1)

	first := `data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n" + auditErrLine + "\n\n"
	_, err := rw.Write([]byte(first))
	require.NoError(t, err)
	_, err = rw.Write([]byte("data: [DO"))
	require.NoError(t, err)
	_, err = rw.Write([]byte("NE]\n\n"))
	require.NoError(t, err)
	rw.flushClassify()

	require.True(t, inf.errorStreamComplete)
	require.Equal(t, "drift", errorMissRejectLabel(inf), "fully-read fragmented stream must not look cancelled")
	require.Equal(t, []string{
		`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
		auditErrLine,
		`data: [DONE]`,
	}, inf.errorStreamLines)
}

func TestRaceWriter_NewlineLessTailNotDuplicated(t *testing.T) {
	ctx := context.Background()
	var sink bytes.Buffer
	rw, inf := newErrorRaceWriter(t, newRaceGroup(ctx, ctx, "escrow-x", &sink), 1)

	_, err := rw.Write([]byte(auditErrLine + "\n\n"))
	require.NoError(t, err)
	_, err = rw.Write([]byte("data: [DONE]"))
	require.NoError(t, err)
	rw.flushClassify()

	require.Equal(t, []string{auditErrLine, `data: [DONE]`}, inf.errorStreamLines)
	require.True(t, inf.errorStreamComplete)
}

func TestRaceWriter_LoserPendingBufDiscardedKeepsPrefix(t *testing.T) {
	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)
	rwLoser, loser := newErrorRaceWriter(t, rg, 1)
	rwWinner, _ := newErrorRaceWriter(t, rg, 2)

	_, err := rwLoser.Write([]byte(`data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n"))
	require.NoError(t, err)
	_, err = rwWinner.Write([]byte(`data: {"choices":[{"delta":{"content":"ok"}}]}` + "\n\n"))
	require.NoError(t, err)
	_, err = rwLoser.Write([]byte("data: {\"choices\":[{\"delta\":{}}]}\n\n"))
	require.NoError(t, err)
	_, err = rwLoser.Write([]byte(auditErrLine + "\n\n" + "data: [DONE]\n\n"))
	require.NoError(t, err)
	rwLoser.flushClassify()

	require.Contains(t, loser.errorStreamLines, `data: {"choices":[{"delta":{"role":"assistant"}}]}`)
	require.Contains(t, loser.errorStreamLines, auditErrLine)
	require.True(t, loser.errorStreamComplete)
}

func TestRaceWriter_ErrorStreamCappedAndTruncated(t *testing.T) {
	old := maxErrorStreamBytes
	maxErrorStreamBytes = 150
	t.Cleanup(func() { maxErrorStreamBytes = old })

	ctx := context.Background()
	var sink bytes.Buffer
	rw, inf := newErrorRaceWriter(t, newRaceGroup(ctx, ctx, "escrow-x", &sink), 1)

	_, err := rw.Write([]byte(auditErrLine + "\n\n"))
	require.NoError(t, err)
	for i := 0; i < 20; i++ {
		_, err = rw.Write([]byte(`data: {"choices":[{"delta":{}}]}` + "\n\n"))
		require.NoError(t, err)
	}
	require.True(t, inf.errorStreamTruncated)
	require.Equal(t, "truncated", errorMissRejectLabel(inf))
	require.Less(t, inf.errorStreamBytes, 150+len(auditErrLine))
}

func TestRaceWriter_CancelledErrorAttemptIsPrefixNotDrift(t *testing.T) {
	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)

	rwLoser, loser := newErrorRaceWriter(t, rg, 1)
	rwWinner, _ := newErrorRaceWriter(t, rg, 2)

	errEvt := []byte(`data: {"error":{"code":500,"message":"boom","type":"InternalServerError"}}` + "\n\n")
	_, err := rwLoser.Write(errEvt)
	require.NoError(t, err)
	require.False(t, loser.errorStreamComplete)

	_, err = rwWinner.Write([]byte(`data: {"choices":[{"delta":{"content":"ok"}}]}` + "\n\n"))
	require.NoError(t, err)
	require.Equal(t, uint64(2), rg.winnerNonce())

	require.False(t, loser.errorStreamComplete)
	require.Equal(t, "cancelled", errorMissRejectLabel(loser))
	require.Equal(t, []string{
		`data: {"error":{"code":500,"message":"boom","type":"InternalServerError"}}`,
	}, loser.errorStreamLines)

	full := []string{
		`data: {"error":{"code":500,"message":"boom","type":"InternalServerError"}}`,
		`data: [DONE]`,
	}
	fullPayload, err := json.Marshal(completionapi.SerializedStreamedResponse{Events: full})
	require.NoError(t, err)
	_, prefixPayload := errorMissArtifacts(loser, nil)
	require.NotEqual(t, sha256.Sum256(fullPayload), sha256.Sum256(prefixPayload),
		"prefix payload must not hash to the executor's full body")
}

func TestErrorMissRejectLabel_CompleteIsDrift(t *testing.T) {
	require.Equal(t, "cancelled", errorMissRejectLabel(&inflight{}))
	require.Equal(t, "drift", errorMissRejectLabel(&inflight{errorStreamComplete: true}))
	require.Equal(t, "truncated", errorMissRejectLabel(&inflight{errorStreamTruncated: true, errorStreamComplete: true}))
}

func TestSsePayloadDataLines_SkipsProtocolEnvelopes(t *testing.T) {
	raw := []byte("data: {\"devshard_receipt\":{}}\n\n" +
		"data: {\"error\":{\"code\":500,\"message\":\"x\",\"type\":\"InternalServerError\"}}\n\n" +
		"data: {\"devshard_meta\":{}}\n\n" +
		"data: [DONE]\n\n")
	got := ssePayloadDataLines(raw)
	require.Equal(t, []string{
		`data: {"error":{"code":500,"message":"x","type":"InternalServerError"}}`,
		`data: [DONE]`,
	}, got)
}

func TestTimeoutKindForInflight_Error(t *testing.T) {
	inf := &inflight{errorSource: "error.InternalServerError", errorTerminal: true}
	inf.setReceiptAt(time.Now())
	require.Equal(t, "error", timeoutKindForInflight(inf, true))
	require.Equal(t, "execution", timeoutKindForInflight(inf, false), "non-error-miss path keeps today's timeout kind")
}

func TestSkipEmptyStreamTimeout_ErrorMissNotSkipped(t *testing.T) {
	finished := &staticFinishedSession{finished: true}

	errInf := &inflight{nonce: 7, errorSource: "error.InternalServerError", errorTerminal: true}
	errInf.setReceiptAt(time.Now())
	_, skip := emptyStreamWithoutWinnerTimeoutSkipReason(errInf, finished)
	require.False(t, skip, "error streams are not empty streams")
	_, skip = skipEmptyStreamTimeout(errInf, finished, true)
	require.False(t, skip, "error miss must not hide behind the empty-stream guard")

	empty := &inflight{nonce: 7}
	empty.setReceiptAt(time.Now())
	reason, skip := emptyStreamWithoutWinnerTimeoutSkipReason(empty, finished)
	require.True(t, skip)
	require.Equal(t, "empty_stream_without_non_empty_winner", reason)
	reason, skip = skipEmptyStreamTimeout(empty, finished, false)
	require.True(t, skip, "non-error-miss empty streams still skip")
	require.Equal(t, "empty_stream_without_non_empty_winner", reason)
	_, skip = skipEmptyStreamTimeout(empty, finished, true)
	require.False(t, skip)
}

func TestErrorMissArtifacts_FinishFromMempool(t *testing.T) {
	inf := &inflight{
		nonce:            3,
		errorStreamLines: []string{`data: {"error":{"code":500,"message":"x","type":"InternalServerError"}}`, `data: [DONE]`},
		resp: &host.HostResponse{
			Mempool: []*types.DevshardTx{
				{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 3}}},
			},
		},
	}
	finishTx, payload := errorMissArtifacts(inf, nil)
	require.NotEmpty(t, finishTx)
	require.NotEmpty(t, payload)
}

func TestErrorMissEnabledFor_RetriableCapabilityErrorDoesNotEmit(t *testing.T) {
	ctx := context.Background()

	t.Run("context_length", func(t *testing.T) {
		var sink bytes.Buffer
		rw, inf := newErrorRaceWriter(t, newRaceGroup(ctx, ctx, "escrow-x", &sink), 1)
		line := `data: {"error":{"code":400,"message":"This model's maximum context length is 131072 tokens. However, you requested 150000 tokens.","type":"BadRequestError"}}` + "\n\n"
		_, err := rw.Write([]byte(line))
		require.NoError(t, err)
		require.NotEmpty(t, inf.errorSource, "retriable envelopes still set errorSource")
		require.False(t, inf.errorTerminal)
		require.False(t, errorMissEnabledFor(inf))
	})

	t.Run("tool_choice", func(t *testing.T) {
		var sink bytes.Buffer
		rw, inf := newErrorRaceWriter(t, newRaceGroup(ctx, ctx, "escrow-x", &sink), 1)
		line := `data: {"error":{"code":400,"message":"` + toolChoiceUnsupportedMessage + `","type":"BadRequestError"}}` + "\n\n"
		_, err := rw.Write([]byte(line))
		require.NoError(t, err)
		require.NotEmpty(t, inf.errorSource)
		require.False(t, inf.errorTerminal)
		require.False(t, errorMissEnabledFor(inf))
	})

	t.Run("internal_error_still_emits", func(t *testing.T) {
		var sink bytes.Buffer
		rw, inf := newErrorRaceWriter(t, newRaceGroup(ctx, ctx, "escrow-x", &sink), 1)
		_, err := rw.Write([]byte(auditErrLine + "\n\n"))
		require.NoError(t, err)
		require.True(t, inf.errorTerminal)
		require.True(t, errorMissEnabledFor(inf))
	})

	t.Run("content_then_error_emits", func(t *testing.T) {
		var sink bytes.Buffer
		rw, inf := newErrorRaceWriter(t, newRaceGroup(ctx, ctx, "escrow-x", &sink), 1)
		_, err := rw.Write([]byte(`data: {"id":"devshard-1-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"}}]}` + "\n\n"))
		require.NoError(t, err)
		_, err = rw.Write([]byte(auditErrLine + "\n\n"))
		require.NoError(t, err)
		require.NotEmpty(t, inf.contentSource)
		require.True(t, inf.errorTerminal)
		require.True(t, errorMissEnabledFor(inf), "error on a signed Finish is a miss even after content")
		require.Equal(t, []string{
			`data: {"id":"devshard-1-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
			auditErrLine,
		}, inf.errorStreamLines, "content prefix must be kept so the reconstructed payload matches Finish")
	})
}

func TestRaceWriter_EmptyStreamClearsRetentionOnFlush(t *testing.T) {
	ctx := context.Background()
	var sink bytes.Buffer
	rw, inf := newErrorRaceWriter(t, newRaceGroup(ctx, ctx, "escrow-x", &sink), 1)

	_, err := rw.Write([]byte(`data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n"))
	require.NoError(t, err)
	require.NotEmpty(t, inf.errorStreamLines, "prefix is kept until the stream ends or content arrives")
	_, err = rw.Write([]byte("data: [DONE]\n\n"))
	require.NoError(t, err)
	rw.flushClassify()
	require.Empty(t, inf.errorStreamLines, "a stream that never errored must drop retention at flush")
}

func TestTimeoutActionForHandleResult(t *testing.T) {
	complete := &inflight{errorStreamComplete: true}
	require.Equal(t, "completed", mustTimeoutAction(t, user.ErrInferenceMissed, true, complete))
	action, reason := timeoutActionForHandleResult(user.ErrInferenceMissed, true, complete)
	require.Equal(t, "completed", action)
	require.Equal(t, "none", reason)

	action, reason = timeoutActionForHandleResult(errors.New("insufficient votes"), true, complete)
	require.Equal(t, "failed", action)
	require.Equal(t, "timeout_collection_error", reason, "vote/network failure must not look like reconstruction drift")

	truncated := &inflight{errorStreamTruncated: true}
	action, reason = timeoutActionForHandleResult(errors.New("insufficient votes"), true, truncated)
	require.Equal(t, "timeout_collection_error", reason)

	action, reason = timeoutActionForHandleResult(errors.New("collect timeout votes"), false, complete)
	require.Equal(t, "failed", action)
	require.Equal(t, "timeout_collection_error", reason)

	action, reason = timeoutActionForHandleResult(nil, false, complete)
	require.Equal(t, "completed", action)
	require.Equal(t, "none", reason)
}

func mustTimeoutAction(t *testing.T, err error, errorMiss bool, inf *inflight) string {
	t.Helper()
	action, _ := timeoutActionForHandleResult(err, errorMiss, inf)
	return action
}
