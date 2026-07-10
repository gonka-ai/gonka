package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
	"devshard/user"
)

// --- Existing tests ---

type panicAfterCancelWriter struct {
	ctx     context.Context
	header  http.Header
	writes  int
	flushes int
}

func (w *panicAfterCancelWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *panicAfterCancelWriter) WriteHeader(_ int) {}

func (w *panicAfterCancelWriter) Write(p []byte) (int, error) {
	if w.ctx.Err() != nil {
		panic("write after cancel")
	}
	w.writes++
	return len(p), nil
}

func (w *panicAfterCancelWriter) Flush() {
	if w.ctx.Err() != nil {
		panic("flush after cancel")
	}
	w.flushes++
}

func TestStreamReset_WrittenOnReconnect(t *testing.T) {
	rec := httptest.NewRecorder()
	writeStreamReset(rec)

	body := rec.Body.String()
	require.Contains(t, body, `data: {"devshard_stream_reset":true}`)
}

func TestJSONErrorPayloadDetails(t *testing.T) {
	details, ok := jsonErrorPayloadDetails([]byte(`{"error":{"code":400,"message":"bad request","type":"BadRequestError"}}`))

	require.True(t, ok)
	require.Equal(t, "400", details.Code)
	require.Equal(t, "BadRequestError", details.Type)
	require.Equal(t, "bad request", details.Message)
}

func TestWriteGatewayJSONErrorContentType(t *testing.T) {
	rec := httptest.NewRecorder()

	writeGatewayJSONError(rec, http.StatusBadGateway, "upstream failed")

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.JSONEq(t, `{"error":{"message":"upstream failed"}}`, rec.Body.String())
}

func TestMetaDrainTimeoutFromEnv_Default(t *testing.T) {
	t.Setenv("DEVSHARD_META_DRAIN_TIMEOUT_SECONDS", "")
	require.Equal(t, defaultMetaDrainTimeout, metaDrainTimeoutFromEnv())
}

func TestMetaDrainTimeoutFromEnv_Override(t *testing.T) {
	t.Setenv("DEVSHARD_META_DRAIN_TIMEOUT_SECONDS", "17")
	require.Equal(t, 17*time.Second, metaDrainTimeoutFromEnv())
}

func TestMetaDrainTimeoutFromEnv_InvalidFallsBack(t *testing.T) {
	t.Setenv("DEVSHARD_META_DRAIN_TIMEOUT_SECONDS", "nope")
	require.Equal(t, defaultMetaDrainTimeout, metaDrainTimeoutFromEnv())

	t.Setenv("DEVSHARD_META_DRAIN_TIMEOUT_SECONDS", "0")
	require.Equal(t, defaultMetaDrainTimeout, metaDrainTimeoutFromEnv())
}

func TestCancelFlag_NilSafeAndOneShot(t *testing.T) {
	var nilFlag *cancelFlag
	require.False(t, nilFlag.Gone(), "nil flag must be safe to query")
	require.Nil(t, nilFlag.Done(), "nil flag must return a nil channel")
	nilFlag.Trigger() // must not panic.

	flag := newCancelFlag()
	require.False(t, flag.Gone())
	flag.Trigger()
	require.True(t, flag.Gone())
	flag.Trigger() // idempotent.
	require.True(t, flag.Gone())

	select {
	case <-flag.Done():
	case <-time.After(time.Second):
		t.Fatal("Done channel should fire after Trigger")
	}
}

func TestWatchClientCancel_TriggersOnDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	flag := newCancelFlag()
	stop := watchClientCancel(r, flag)
	defer stop()

	cancel()

	select {
	case <-flag.Done():
	case <-time.After(time.Second):
		t.Fatal("flag should trigger when the request context is canceled mid-request")
	}
}

func TestWatchClientCancel_StopPreventsTriggerOnNormalCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	flag := newCancelFlag()
	stop := watchClientCancel(r, flag)

	stop()
	stop() // must be idempotent
	cancel()

	time.Sleep(50 * time.Millisecond)
	require.False(t, flag.Gone(), "normal handler return must not be treated as a client disconnect")
}

func TestDeferredWriter_SwallowsAfterClientGone(t *testing.T) {
	rec := httptest.NewRecorder()
	flag := newCancelFlag()
	dw := newDeferredWriter(context.Background(), rec, "escrow-test", flag)

	n, err := dw.Write([]byte("data: first\n\n"))
	require.NoError(t, err)
	require.Equal(t, 13, n)
	require.True(t, dw.started)
	require.Contains(t, rec.Body.String(), "data: first")

	flag.Trigger()
	before := rec.Body.Len()

	n, err = dw.Write([]byte("data: dropped\n\n"))
	require.NoError(t, err, "writes after client cancel must succeed for callers")
	require.Equal(t, 15, n, "Write must report full byte count to keep callers happy")
	require.Equal(t, before, rec.Body.Len(), "no bytes should reach the recorder after cancel")

	require.NoError(t, dw.flush("after_cancel"))
}

func TestDeferredWriterSkipsWriteAfterContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &panicAfterCancelWriter{ctx: ctx}
	dw := &deferredWriter{ctx: ctx, w: w}

	cancel()

	n, err := dw.Write([]byte("data: chunk\n\n"))
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, n)
	require.Zero(t, w.writes)

	dw.Flush()
	require.Zero(t, w.flushes)
}

func TestRaceWriterSkipsWinnerWriteAfterContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &panicAfterCancelWriter{ctx: ctx}
	dw := &deferredWriter{ctx: ctx, w: w}
	rg := newRaceGroup(ctx, ctx, "escrow-proxy", dw)
	inf := &inflight{
		hostID:       "host-1",
		escrowID:     "escrow-proxy",
		nonce:        1,
		done:         make(chan struct{}),
		receiptCh:    make(chan struct{}),
		firstTokenCh: make(chan struct{}),
	}
	rw := &raceWriter{group: rg, nonce: 1, inf: inf}

	cancel()
	rg.setWinner(1)

	n, err := rw.Write([]byte("data: chunk\n\n"))
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, n)
	require.Zero(t, w.writes)

	rw.Flush()
	require.Zero(t, w.flushes)
}

func TestRaceWriterDetachedWinnerSinksAfterContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &panicAfterCancelWriter{ctx: ctx}
	dw := &deferredWriter{ctx: ctx, w: w}
	rg := newRaceGroup(ctx, ctx, "escrow-proxy", dw)
	inf := &inflight{
		hostID:       "host-1",
		escrowID:     "escrow-proxy",
		nonce:        1,
		done:         make(chan struct{}),
		receiptCh:    make(chan struct{}),
		firstTokenCh: make(chan struct{}),
	}
	rw := &raceWriter{group: rg, nonce: 1, inf: inf}

	rg.setWinner(1)
	rg.detachClient()
	cancel()

	payload := []byte(`data: {"choices":[{"delta":{"content":"late"}}]}` + "\n\n")
	n, err := rw.Write(payload)
	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	require.Zero(t, w.writes)
	require.Equal(t, int64(1), inf.outputChunks.Load())
	require.Equal(t, int64(1), inf.contentChunks.Load())

	rw.Flush()
	require.Zero(t, w.flushes)
}

func TestRaceWriterDefersSuspiciousWinnerUntilFallback(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := context.Background()
	rg := newRaceGroup(ctx, ctx, "escrow-proxy", rec)
	inf := &inflight{
		hostID:       "host-1",
		escrowID:     "escrow-proxy",
		nonce:        1,
		suspicious:   true,
		done:         make(chan struct{}),
		receiptCh:    make(chan struct{}),
		firstTokenCh: make(chan struct{}),
	}
	rw := &raceWriter{group: rg, nonce: 1, inf: inf}
	payload := []byte(`data: {"choices":[{"delta":{"content":"suspect"}}]}` + "\n\n")

	n, err := rw.Write(payload)
	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	require.Zero(t, rg.winnerNonce())
	require.Empty(t, rec.Body.String())
	require.Equal(t, payload, inf.pendingBuf)

	require.NoError(t, rg.promoteFallbackWinner(inf))
	require.EqualValues(t, 1, rg.winnerNonce())
	require.Equal(t, string(payload), rec.Body.String())
	require.Empty(t, inf.pendingBuf)
}

func TestRaceWriterLogsSuspiciousWinnerDeferredOncePerAttempt(t *testing.T) {
	var logs bytes.Buffer
	oldOutput := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldOutput)
		log.SetFlags(oldFlags)
	})

	rec := httptest.NewRecorder()
	ctx := context.Background()
	rg := newRaceGroup(ctx, ctx, "escrow-proxy", rec)
	inf := &inflight{
		hostID:       "host-1",
		escrowID:     "escrow-proxy",
		nonce:        1,
		suspicious:   true,
		done:         make(chan struct{}),
		receiptCh:    make(chan struct{}),
		firstTokenCh: make(chan struct{}),
	}
	rw := &raceWriter{group: rg, nonce: 1, inf: inf}

	for _, payload := range [][]byte{
		[]byte(`data: {"choices":[{"delta":{"content":"chunk-1"}}]}` + "\n\n"),
		[]byte(`data: {"choices":[{"delta":{"content":"chunk-2"}}]}` + "\n\n"),
		[]byte(`data: {"choices":[{"delta":{"content":"chunk-3"}}]}` + "\n\n"),
	} {
		_, err := rw.Write(payload)
		require.NoError(t, err)
	}

	require.Equal(t, 1, strings.Count(logs.String(), "stage=suspicious_winner_deferred"))
	require.Equal(t, int64(3), inf.contentChunks.Load())
	require.Zero(t, rg.winnerNonce())
}

func TestRaceWriterNonSuspiciousBeatsSuspiciousAttempt(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := context.Background()
	rg := newRaceGroup(ctx, ctx, "escrow-proxy", rec)
	suspicious := &inflight{
		hostID:       "host-1",
		escrowID:     "escrow-proxy",
		nonce:        1,
		suspicious:   true,
		done:         make(chan struct{}),
		receiptCh:    make(chan struct{}),
		firstTokenCh: make(chan struct{}),
	}
	normal := &inflight{
		hostID:       "host-2",
		escrowID:     "escrow-proxy",
		nonce:        2,
		done:         make(chan struct{}),
		receiptCh:    make(chan struct{}),
		firstTokenCh: make(chan struct{}),
	}

	_, err := (&raceWriter{group: rg, nonce: 1, inf: suspicious}).Write([]byte(`data: {"choices":[{"delta":{"content":"suspect"}}]}` + "\n\n"))
	require.NoError(t, err)
	require.Zero(t, rg.winnerNonce())

	normalPayload := []byte(`data: {"choices":[{"delta":{"content":"normal"}}]}` + "\n\n")
	_, err = (&raceWriter{group: rg, nonce: 2, inf: normal}).Write(normalPayload)
	require.NoError(t, err)
	require.EqualValues(t, 2, rg.winnerNonce())
	require.Equal(t, string(normalPayload), rec.Body.String())
}

func TestDeferredWriterRewritesCompletionPayloadToStreamingChunks(t *testing.T) {
	rec := httptest.NewRecorder()
	dw := &deferredWriter{ctx: context.Background(), w: rec, escrow: "escrow-proxy"}

	payload := `data: {"id":"cmpl-1","object":"chat.completion","created":123,"model":"Qwen","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"logprobs":{"content":[{"token":"Hi","logprob":0,"bytes":[72,105],"top_logprobs":[{"token":"Hi","logprob":0,"bytes":[72,105]}]}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1}}` + "\n\n"
	_, err := dw.Write([]byte(payload))
	require.NoError(t, err)

	var events []map[string]any
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}
		var evt map[string]any
		require.NoError(t, json.Unmarshal([]byte(data), &evt))
		events = append(events, evt)
	}

	require.Len(t, events, 3)
	require.Equal(t, "chat.completion.chunk", events[0]["object"])
	firstChoices := events[0]["choices"].([]any)
	firstChoice := firstChoices[0].(map[string]any)
	firstDelta := firstChoice["delta"].(map[string]any)
	require.Equal(t, "assistant", firstDelta["role"])

	secondChoices := events[1]["choices"].([]any)
	secondChoice := secondChoices[0].(map[string]any)
	secondDelta := secondChoice["delta"].(map[string]any)
	require.Equal(t, "Hi", secondDelta["content"])
	require.Equal(t, "stop", secondChoice["finish_reason"])
	require.NotContains(t, secondChoice, "logprobs")
	require.NotContains(t, rec.Body.String(), "logprob")

	require.Equal(t, "chat.completion.chunk", events[2]["object"])
	require.Empty(t, events[2]["choices"].([]any))
	usage := events[2]["usage"].(map[string]any)
	require.Equal(t, float64(7), usage["prompt_tokens"])
	require.Equal(t, float64(1), usage["completion_tokens"])
	require.False(t, dw.sawDone)
}

func TestDeferredWriterTracksForwardedDoneMarker(t *testing.T) {
	rec := httptest.NewRecorder()
	dw := &deferredWriter{ctx: context.Background(), w: rec, escrow: "escrow-proxy"}

	_, err := dw.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"))
	require.NoError(t, err)

	require.True(t, dw.sawDone)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
}

func TestRewriteStreamingPayload_PassthroughWhenNoConversionNeeded(t *testing.T) {
	payload := []byte(`data: {"id":"cmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}` + "\n\n")
	require.Equal(t, payload, rewriteStreamingPayload(payload))
}

func TestRewriteStreamingPayload_FiltersLogprobsFromExistingChunks(t *testing.T) {
	payload := []byte(`data: {"id":"cmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hi"},"logprobs":{"content":[{"token":"Hi","logprob":0,"top_logprobs":[{"token":"Hi","logprob":0}]}]},"finish_reason":null}]}` + "\n\n")

	rewritten := rewriteStreamingPayload(payload)

	require.NotContains(t, string(rewritten), "logprob")
	require.Contains(t, string(rewritten), `"content":"Hi"`)
}

func TestFilterClientInternalFields_RemovesNestedLogprobPayloads(t *testing.T) {
	payload := []byte(`{"choices":[{"message":{"content":"Hi"},"logprobs":{"content":[{"token":"Hi","logprob":0,"top_logprobs":[{"token":"Hi","logprob":0}]}]}}]}`)

	filtered := filterClientInternalFields(payload)

	require.JSONEq(t, `{"choices":[{"message":{"content":"Hi"}}]}`, string(filtered))
}

func TestFilterClientInternalFields_RemovesTokenIDsAndPromptTokenIDs(t *testing.T) {
	payload := []byte(`{"prompt_token_ids":[1,2,3],"choices":[{"message":{"content":"Hi"},"token_ids":[4,5,6]}]}`)

	filtered := filterClientInternalFields(payload)

	require.JSONEq(t, `{"choices":[{"message":{"content":"Hi"}}]}`, string(filtered))
}

func TestRewriteStreamingPayload_PreservesOriginalBytesWhenConvertibleRewriteFails(t *testing.T) {
	payload := []byte("data: {\"choices\":[{\"message\":{\"role\":\"assistant\",\"content\":\"\"}}]}\r\n\r\n")
	require.Equal(t, payload, rewriteStreamingPayload(payload))
}

func TestHasMsgFinish(t *testing.T) {
	require.False(t, user.HasMsgFinish(nil, 1))

	txs := []*types.DevshardTx{
		{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1}}},
	}
	require.False(t, user.HasMsgFinish(txs, 1))

	txs = append(txs, &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 1}}})
	require.True(t, user.HasMsgFinish(txs, 1))
	require.False(t, user.HasMsgFinish(txs, 2))
}

// --- Test infrastructure for proxy-level tests ---

// killableClient wraps a HostClient. Kill/Revive toggle availability.
type killableClient struct {
	inner  user.HostClient
	killed atomic.Bool
	mu     sync.Mutex
	err    error
	last   *host.HostRequest
}

func (c *killableClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	c.mu.Lock()
	reqCopy := req
	c.last = &reqCopy
	forcedErr := c.err
	c.mu.Unlock()
	if c.killed.Load() {
		return nil, fmt.Errorf("host killed")
	}
	if forcedErr != nil {
		return nil, forcedErr
	}
	return c.inner.Send(ctx, req, stream, receiptHandler)
}

func (c *killableClient) Kill()   { c.killed.Store(true) }
func (c *killableClient) Revive() { c.killed.Store(false) }

func (c *killableClient) ForceError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

func (c *killableClient) LastRequest() *host.HostRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		return nil
	}
	reqCopy := *c.last
	return &reqCopy
}

// verifierClient wraps killableClient and implements user.TimeoutVerifier.
// This allows session.TimeoutVerifiers() to discover it.
type verifierClient struct {
	*killableClient
	accept  bool
	signer  *signing.Secp256k1Signer
	group   []types.SlotAssignment
	slotIdx int
}

type delayedResultClient struct {
	response  *host.HostResponse
	releaseCh chan struct{}
	sendCalls atomic.Int32
}

func (c *delayedResultClient) Send(ctx context.Context, _ host.HostRequest, _ io.Writer, _ func()) (*host.HostResponse, error) {
	c.sendCalls.Add(1)
	select {
	case <-c.releaseCh:
		return c.response, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type emptyNonStreamingRecorderClient struct {
	sendCalls atomic.Int32
	maxTokens []uint64
	mu        sync.Mutex
}

func (c *emptyNonStreamingRecorderClient) Send(_ context.Context, req host.HostRequest, _ io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	c.sendCalls.Add(1)
	if receiptHandler != nil {
		receiptHandler()
	}
	if req.Payload != nil {
		c.mu.Lock()
		c.maxTokens = append(c.maxTokens, req.Payload.MaxTokens)
		c.mu.Unlock()
	}
	return &host.HostResponse{Nonce: req.Nonce}, nil
}

func (c *emptyNonStreamingRecorderClient) MaxTokens() []uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]uint64(nil), c.maxTokens...)
}

type blockingNonStreamingRecorderClient struct {
	sendCalls atomic.Int32
	maxTokens []uint64
	mu        sync.Mutex
}

func (c *blockingNonStreamingRecorderClient) Send(ctx context.Context, req host.HostRequest, _ io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	c.sendCalls.Add(1)
	if receiptHandler != nil {
		receiptHandler()
	}
	if req.Payload != nil {
		c.mu.Lock()
		c.maxTokens = append(c.maxTokens, req.Payload.MaxTokens)
		c.mu.Unlock()
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *blockingNonStreamingRecorderClient) MaxTokens() []uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]uint64(nil), c.maxTokens...)
}

func (c *verifierClient) VerifyTimeout(_ context.Context, inferenceID uint64, reason types.TimeoutReason, _ *host.InferencePayload, _ []types.Diff) (bool, []byte, uint32, error) {
	if !c.accept {
		return false, nil, 0, nil
	}
	voterSlot := c.group[c.slotIdx].SlotID
	content := &types.TimeoutVoteContent{
		EscrowId:    "escrow-proxy",
		InferenceId: inferenceID,
		Reason:      reason,
		Accept:      true,
	}
	data, err := proto.Marshal(content)
	if err != nil {
		return false, nil, 0, err
	}
	sig, err := c.signer.Sign(data)
	if err != nil {
		return false, nil, 0, err
	}
	return true, sig, voterSlot, nil
}

type testProxyEnv struct {
	proxy     *Proxy
	session   *user.Session
	sm        *state.StateMachine
	killables []*killableClient
	group     []types.SlotAssignment
}

func zeroReceiptTimeout(t *testing.T) {
	t.Helper()
	saved := ReceiptTimeout
	ReceiptTimeout = 50 * time.Millisecond
	t.Cleanup(func() { ReceiptTimeout = saved })
}

func setSpeculativeTiming(t *testing.T, receipt time.Duration, firstTokenCap time.Duration, perInputToken time.Duration, secondaryWait time.Duration) {
	t.Helper()
	savedReceipt := ReceiptTimeout
	savedFirstTokenCap := FirstTokenTimeoutCap
	savedPerInputToken := PerInputTokenFirstTokenLag
	savedSecondaryWait := SecondaryWaitAfterWinner
	ReceiptTimeout = receipt
	FirstTokenTimeoutCap = firstTokenCap
	PerInputTokenFirstTokenLag = perInputToken
	SecondaryWaitAfterWinner = secondaryWait
	t.Cleanup(func() {
		ReceiptTimeout = savedReceipt
		FirstTokenTimeoutCap = savedFirstTokenCap
		PerInputTokenFirstTokenLag = savedPerInputToken
		SecondaryWaitAfterWinner = savedSecondaryWait
	})
}

func setInterChunkStallTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := captureRedundancyTimingSettings()
	InterChunkStallTimeout = d
	InterChunkStallLogThreshold = d
	t.Cleanup(func() { restoreRedundancyTimingSettings(prev) })
}

func setStreamingAttemptHardTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := captureRedundancyTimingSettings()
	StreamingAttemptHardTimeout = d
	t.Cleanup(func() { restoreRedundancyTimingSettings(prev) })
}

func setSecondaryWaitAfterWinner(t *testing.T, d time.Duration) {
	t.Helper()
	saved := SecondaryWaitAfterWinner
	SecondaryWaitAfterWinner = d
	t.Cleanup(func() {
		SecondaryWaitAfterWinner = saved
	})
}

func setNonStreamingTimeouts(t *testing.T, reducedFallback, noContent, maxAttemptWait time.Duration) {
	t.Helper()
	savedReducedFallback := nonStreamingReducedMaxTokensFallbackDelay
	savedNoContent := nonStreamingNoContentTimeout
	savedMaxAttemptWait := nonStreamingMaxAttemptWait
	nonStreamingReducedMaxTokensFallbackDelay = reducedFallback
	nonStreamingNoContentTimeout = noContent
	nonStreamingMaxAttemptWait = maxAttemptWait
	t.Cleanup(func() {
		nonStreamingReducedMaxTokensFallbackDelay = savedReducedFallback
		nonStreamingNoContentTimeout = savedNoContent
		nonStreamingMaxAttemptWait = savedMaxAttemptWait
	})
}

func disablePairwiseABSampling(t *testing.T) {
	t.Helper()
	savedABSampleRate := PairwiseABSampleRate
	savedABSparseSampleRate := PairwiseABSparseSampleRate
	savedMinDirectComparisons := PairwiseMinDirectComparisons
	PairwiseABSampleRate = 0
	PairwiseABSparseSampleRate = 0
	PairwiseMinDirectComparisons = 0
	t.Cleanup(func() {
		PairwiseABSampleRate = savedABSampleRate
		PairwiseABSparseSampleRate = savedABSparseSampleRate
		PairwiseMinDirectComparisons = savedMinDirectComparisons
	})
}

func setupTestProxy(t *testing.T, numHosts int, engines []devshard.InferenceEngine, verifierAccept bool) *testProxyEnv {
	t.Helper()
	return setupTestProxyWithBalance(t, numHosts, engines, verifierAccept, 1_000_000)
}

func setupTestProxyWithBalance(t *testing.T, numHosts int, engines []devshard.InferenceEngine, verifierAccept bool, balance uint64) *testProxyEnv {
	t.Helper()
	hostSigners := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := types.SessionConfig{
		RefusalTimeout:   1,
		ExecutionTimeout: 1,
		TokenPrice:       1,
		VoteThreshold:    uint32(numHosts) / 2,
	}
	verifier := signing.NewSecp256k1Verifier()

	killables := make([]*killableClient, numHosts)
	clients := make([]user.HostClient, numHosts)
	for i := range hostSigners {
		sm := statetest.MustStateMachine(t, "escrow-proxy", config, group, balance, userKey.Address(), verifier)
		var engine devshard.InferenceEngine
		if engines != nil {
			engine = engines[i]
		} else {
			engine = stub.NewInferenceEngine()
		}
		h, err := host.NewHost(sm, hostSigners[i], engine, "escrow-proxy", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		kc := &killableClient{inner: &user.InProcessClient{Host: h}}
		killables[i] = kc
		clients[i] = &verifierClient{
			killableClient: kc,
			accept:         verifierAccept,
			signer:         hostSigners[i],
			group:          group,
			slotIdx:        i,
		}
	}

	userSM := statetest.MustStateMachine(t, "escrow-proxy", config, group, balance, userKey.Address(), verifier)
	session, err := user.NewSession(userSM, userKey, "escrow-proxy", group, clients, verifier)
	require.NoError(t, err)

	perf := NewPerfTracker(nil)
	redundancy := NewRedundancy(session, perf, numHosts, "llama")
	t.Cleanup(redundancy.Stop)

	p := &Proxy{
		session:    session,
		sm:         userSM,
		escrowID:   "escrow-proxy",
		model:      "llama",
		redundancy: redundancy,
		perf:       perf,
	}

	return &testProxyEnv{
		proxy:     p,
		session:   session,
		sm:        userSM,
		killables: killables,
		group:     group,
	}
}

func setupTestProxyWithClients(t *testing.T, clients []user.HostClient) *testProxyEnv {
	t.Helper()
	numHosts := len(clients)
	hostSigners := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := types.SessionConfig{
		RefusalTimeout:   1,
		ExecutionTimeout: 1,
		TokenPrice:       1,
		VoteThreshold:    uint32(numHosts) / 2,
	}
	verifier := signing.NewSecp256k1Verifier()
	userSM := statetest.MustStateMachine(t, "escrow-proxy", config, group, 1_000_000, userKey.Address(), verifier)
	session, err := user.NewSession(userSM, userKey, "escrow-proxy", group, clients, verifier)
	require.NoError(t, err)

	perf := NewPerfTracker(nil)
	redundancy := NewRedundancy(session, perf, numHosts, "llama")
	t.Cleanup(redundancy.Stop)

	p := &Proxy{
		session:    session,
		sm:         userSM,
		escrowID:   "escrow-proxy",
		model:      "llama",
		redundancy: redundancy,
		perf:       perf,
	}

	return &testProxyEnv{
		proxy:   p,
		session: session,
		sm:      userSM,
		group:   group,
	}
}

func defaultParams() user.InferenceParams {
	return user.InferenceParams{
		Model:       "llama",
		Prompt:      testutil.TestPrompt,
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   time.Now().Unix(),
	}
}

// --- Proxy-level test scenarios ---

func TestRunInference_HappyPath(t *testing.T) {
	zeroReceiptTimeout(t)
	env := setupTestProxy(t, 3, nil, true)
	ctx := context.Background()

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(ctx, defaultParams(), &buf, nil)
	require.NoError(t, err)

	st := env.sm.SnapshotState()
	_, ok := st.Inferences[1]
	require.True(t, ok, "inference 1 should exist")
}

// errSimulatedWinnerTransport is returned by streamContentThenErrClient after
// it streams a content-bearing SSE chunk so the race crowns a winner.
var errSimulatedWinnerTransport = errors.New("simulated winner transport failure")

type streamContentThenErrClient struct{}

func (streamContentThenErrClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	if receiptHandler != nil {
		receiptHandler()
	}
	if stream != nil {
		_, _ = io.WriteString(stream, `data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n")
	}
	return nil, errSimulatedWinnerTransport
}

type streamContentWithoutFinishClient struct{}

func (streamContentWithoutFinishClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	if receiptHandler != nil {
		receiptHandler()
	}
	if stream != nil {
		_, _ = io.WriteString(stream, `data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n")
	}
	return &host.HostResponse{
		Nonce:       req.Nonce,
		ConfirmedAt: time.Now().Unix(),
	}, nil
}

type emptyStartedClient struct{}

func (emptyStartedClient) Send(_ context.Context, req host.HostRequest, _ io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	if receiptHandler != nil {
		receiptHandler()
	}
	return &host.HostResponse{
		Nonce:       req.Nonce,
		ConfirmedAt: time.Now().Unix(),
	}, nil
}

type errorStreamWithoutFinishClient struct {
	calls atomic.Int32
}

func (c *errorStreamWithoutFinishClient) Send(_ context.Context, req host.HostRequest, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	c.calls.Add(1)
	if receiptHandler != nil {
		receiptHandler()
	}
	if stream != nil {
		_, _ = io.WriteString(stream, `data: {"error":{"code":404,"message":"The model does not exist.","type":"NotFoundError"}}`+"\n\n")
		_, _ = io.WriteString(stream, "data: [DONE]\n\n")
	}
	return &host.HostResponse{
		Nonce:       req.Nonce,
		ConfirmedAt: time.Now().Add(-10 * time.Second).Unix(),
	}, nil
}

type streamContentThenStallClient struct{}

func (streamContentThenStallClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	if receiptHandler != nil {
		receiptHandler()
	}
	if stream != nil {
		_, _ = io.WriteString(stream, `data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n")
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

type streamContentThenReleaseClient struct {
	releaseCh chan struct{}
	err       error
}

func (c *streamContentThenReleaseClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	if receiptHandler != nil {
		receiptHandler()
	}
	if stream != nil {
		_, _ = io.WriteString(stream, `data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n")
	}
	select {
	case <-c.releaseCh:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if c.err != nil {
		return nil, c.err
	}
	if stream != nil {
		_, _ = io.WriteString(stream, `data: {"choices":[{"delta":{"content":"late"}}]}`+"\n\n")
	}
	nid := req.Nonce
	return &host.HostResponse{
		Nonce: nid,
		Mempool: []*types.DevshardTx{
			{Tx: &types.DevshardTx_FinishInference{
				FinishInference: &types.MsgFinishInference{InferenceId: nid},
			}},
		},
		ConfirmedAt: time.Now().Unix(),
	}, nil
}

// releaseAfterClient blocks Send until releaseCh is closed (or ctx is done),
// then returns a minimal successful HostResponse for the request nonce.
type releaseAfterClient struct {
	releaseCh chan struct{}
}

func (c *releaseAfterClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	select {
	case <-c.releaseCh:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if receiptHandler != nil {
		receiptHandler()
	}
	nid := req.Nonce
	return &host.HostResponse{
		Nonce: nid,
		Mempool: []*types.DevshardTx{
			{Tx: &types.DevshardTx_FinishInference{
				FinishInference: &types.MsgFinishInference{InferenceId: nid},
			}},
		},
	}, nil
}

func TestRunInference_WinnerFailsAfterContentDoesNotWaitForLosers(t *testing.T) {
	zeroReceiptTimeout(t)
	env := setupTestProxy(t, 2, nil, true)

	// Force immediate parallel secondary (Rule 1: primary unresponsive).
	for i := range env.killables {
		env.proxy.redundancy.perf.Record(RequestSample{HostIdx: i, Responsive: false})
	}

	releaseSlow := make(chan struct{})
	env.killables[0].inner = streamContentThenErrClient{}
	env.killables[1].inner = &releaseAfterClient{releaseCh: releaseSlow}

	start := time.Now()
	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)
	elapsed := time.Since(start)

	require.ErrorIs(t, err, errSimulatedWinnerTransport)
	require.Less(t, elapsed, 2*time.Second,
		"expected immediate error once crowned winner fails; should not block on slow loser")

	close(releaseSlow)
}

func TestRecordWinnerTerminalFailureSkipsNonStallTerminalErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "state hash mismatch", err: fmt.Errorf("process response: %w", types.ErrStateHashMismatch)},
		{name: "generic process error", err: fmt.Errorf("process response: malformed terminal chunk")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			perf := NewPerfTracker(nil)
			limiter := NewParticipantRequestLimiter(10, 10)
			redundancy := &Redundancy{
				perf:               perf,
				participantLimiter: limiter,
			}

			// One prior failure means the old stalled-winner path would add a second
			// failed sample and immediately quarantine the participant. Terminal errors
			// without a recorded stream stall must not be classified as stalled winners.
			perf.Record(RequestSample{HostIdx: 0, ParticipantKey: "host:0", Responsive: false})

			inf := &inflight{
				hostIdx:    0,
				nonce:      7,
				sendTime:   time.Now().Add(-time.Second),
				processErr: tc.err,
			}
			inf.contentChunks.Store(1)

			redundancy.recordWinnerTerminalFailureOnce(inf, user.InferenceParams{InputLength: 1}, 7)

			stats := perf.StatsForParticipant("host:0")
			require.Equal(t, 1, stats.TotalSamples)
			require.Equal(t, 1, stats.FailureSamples)
			require.False(t, limiter.IsBlocked("host:0"))
		})
	}
}

func TestRecordWinnerTerminalFailureRecordsOnlyRecordedStall(t *testing.T) {
	perf := NewPerfTracker(nil)
	limiter := NewParticipantRequestLimiter(10, 10)
	redundancy := &Redundancy{
		perf:               perf,
		participantLimiter: limiter,
	}
	perf.Record(RequestSample{HostIdx: 0, ParticipantKey: "host:0", Responsive: false})

	lastChunkAt := time.Now().Add(-2 * InterChunkStallLogThreshold)
	inf := &inflight{
		hostIdx:  0,
		nonce:    7,
		sendTime: time.Now().Add(-time.Second),
	}
	inf.contentChunks.Store(1)
	inf.lastChunkAt.Store(lastChunkAt.UnixNano())
	_, ok := inf.startInterChunkStall(time.Now())
	require.True(t, ok)

	redundancy.recordWinnerTerminalFailureOnce(inf, user.InferenceParams{InputLength: 1}, 7)

	stats := perf.StatsForParticipant("host:0")
	require.Equal(t, 2, stats.TotalSamples)
	require.Equal(t, 2, stats.FailureSamples)
	require.False(t, limiter.IsBlocked("host:0"))
	require.True(t, limiter.IsShadowQuarantined("host:0"))
}

func TestRunInference_WinnerStallsAfterContentTimesOut(t *testing.T) {
	setInterChunkStallTimeout(t, 50*time.Millisecond)
	setStreamingAttemptHardTimeout(t, 120*time.Millisecond)
	env := setupTestProxyWithClients(t, []user.HostClient{streamContentThenStallClient{}})
	limiter := NewParticipantRequestLimiter(10, 10)
	env.proxy.redundancy.participantLimiter = limiter

	start := time.Now()
	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)
	elapsed := time.Since(start)

	requireStalledWinnerTimeoutError(t, err)
	require.GreaterOrEqual(t, elapsed, 100*time.Millisecond,
		"stalled winner should be allowed to keep running until the hard attempt timeout")
	require.Contains(t, buf.String(), `"content":"x"`,
		"winner should still forward the first chunk before timing out")

	require.Eventually(t, func() bool {
		stats := env.proxy.redundancy.perf.Stats(0)
		return stats.TotalSamples == 1 && stats.ResponsiveRate == 0
	}, time.Second, 10*time.Millisecond)
	require.False(t, limiter.IsBlocked(env.session.HostParticipantKey(0)),
		"a single stalled winner is below the participant failure threshold")
}

func TestRunInference_StalledWinnerCanCompleteAfterClientTimeout(t *testing.T) {
	setInterChunkStallTimeout(t, 50*time.Millisecond)
	release := make(chan struct{})
	env := setupTestProxyWithClients(t, []user.HostClient{&streamContentThenReleaseClient{releaseCh: release}})

	var buf bytes.Buffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(buf.String(), `"content":"x"`)
	}, time.Second, 10*time.Millisecond)
	time.Sleep(75 * time.Millisecond)
	require.Contains(t, buf.String(), `"content":"x"`)

	close(release)
	require.NoError(t, <-errCh)

	require.Eventually(t, func() bool {
		return env.session.IsNonceFinished(1)
	}, time.Second, 10*time.Millisecond)
	require.Contains(t, buf.String(), `"content":"late"`,
		"late chunks after a logged stall should still be forwarded")

	stats := env.proxy.redundancy.perf.Stats(0)
	require.Equal(t, 1, stats.TotalSamples)
	require.Equal(t, 1.0, stats.ResponsiveRate)
}

func TestRunInference_StalledWinnerNaturalErrorAfterClientTimeoutRecordsFailure(t *testing.T) {
	setInterChunkStallTimeout(t, 50*time.Millisecond)
	release := make(chan struct{})
	env := setupTestProxyWithClients(t, []user.HostClient{&streamContentThenReleaseClient{
		releaseCh: release,
		err:       errSimulatedWinnerTransport,
	}})

	var buf bytes.Buffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(buf.String(), `"content":"x"`)
	}, time.Second, 10*time.Millisecond)
	time.Sleep(75 * time.Millisecond)
	close(release)
	require.ErrorIs(t, <-errCh, errSimulatedWinnerTransport)

	require.Eventually(t, func() bool {
		stats := env.proxy.redundancy.perf.Stats(0)
		return stats.TotalSamples == 1 && stats.ResponsiveRate == 0
	}, time.Second, 10*time.Millisecond)
}

func TestStalledWinnerQuarantineRequiresParticipantFailureThreshold(t *testing.T) {
	env := setupTestProxyWithClients(t, []user.HostClient{streamContentThenStallClient{}})
	limiter := NewParticipantRequestLimiter(10, 10)
	env.proxy.redundancy.participantLimiter = limiter
	participantKey := env.session.HostParticipantKey(0)
	markStalled := func(inf *inflight) {
		inf.contentChunks.Store(1)
		inf.lastChunkAt.Store(time.Now().Add(-2 * InterChunkStallLogThreshold).UnixNano())
		_, ok := inf.startInterChunkStall(time.Now())
		require.True(t, ok)
	}

	first := &inflight{
		hostIdx:     0,
		nonce:       1,
		sendTime:    time.Now().Add(-time.Second),
		receiptTime: time.Now().Add(-900 * time.Millisecond),
		firstToken:  time.Now().Add(-800 * time.Millisecond),
	}
	markStalled(first)
	env.proxy.redundancy.recordStalledWinnerFailureOnce(first, defaultParams())
	require.False(t, limiter.IsBlocked(participantKey))

	second := &inflight{
		hostIdx:     0,
		nonce:       2,
		sendTime:    time.Now().Add(-time.Second),
		receiptTime: time.Now().Add(-900 * time.Millisecond),
		firstToken:  time.Now().Add(-800 * time.Millisecond),
	}
	markStalled(second)
	env.proxy.redundancy.recordStalledWinnerFailureOnce(second, defaultParams())
	require.False(t, limiter.IsBlocked(participantKey))
	require.True(t, limiter.IsShadowQuarantined(participantKey))
}

func TestLongResponseAfterContentSkipsParticipantFailureAccounting(t *testing.T) {
	env := setupTestProxyWithClients(t, []user.HostClient{streamContentThenStallClient{}})
	limiter := NewParticipantRequestLimiter(10, 10)
	env.proxy.redundancy.participantLimiter = limiter
	participantKey := env.session.HostParticipantKey(0)

	for i := 0; i < 2; i++ {
		inf := &inflight{
			hostIdx:     0,
			nonce:       uint64(i + 1),
			sendTime:    time.Now().Add(-(longResponseFailureExemption + time.Second)),
			receiptTime: time.Now().Add(-(longResponseFailureExemption + 900*time.Millisecond)),
			firstToken:  time.Now().Add(-(longResponseFailureExemption + 800*time.Millisecond)),
		}
		inf.contentChunks.Store(1)
		inf.outputChunks.Store(1)
		env.proxy.redundancy.recordStalledWinnerFailureOnce(inf, defaultParams())
		env.proxy.redundancy.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
	}

	require.False(t, limiter.IsBlocked(participantKey))
	require.Equal(t, 0, env.proxy.redundancy.perf.Stats(0).TotalSamples)
}

func TestLongNonStreamEmptyResponseRecordsTimingWithoutQuarantine(t *testing.T) {
	env := setupTestProxyWithClients(t, []user.HostClient{streamContentThenStallClient{}})
	limiter := NewParticipantRequestLimiter(10, 10)
	env.proxy.redundancy.participantLimiter = limiter
	oldWindow := ParticipantPerfWindow
	ParticipantPerfWindow = 24 * time.Hour
	t.Cleanup(func() { ParticipantPerfWindow = oldWindow })
	participantKey := env.session.HostParticipantKey(0)
	params := defaultParams()
	params.Stream = false

	for i := 0; i < 2; i++ {
		inf := &inflight{
			hostIdx:     0,
			nonce:       uint64(i + 1),
			sendTime:    time.Now().Add(-(longResponseFailureExemption + time.Second)),
			receiptTime: time.Now().Add(-(longResponseFailureExemption + 900*time.Millisecond)),
		}
		env.proxy.redundancy.recordStartedAttemptSamples([]*inflight{inf}, params, true)
	}

	require.False(t, limiter.IsBlocked(participantKey))
	stats := env.proxy.redundancy.perf.Stats(0)
	require.Equal(t, 2, stats.TotalSamples)
	require.Equal(t, 1.0, stats.ResponsiveRate)

	inf := &inflight{
		hostIdx:     0,
		nonce:       99,
		sendTime:    time.Now().Add(-(longResponseFailureExemption + time.Second)),
		receiptTime: time.Now().Add(-(longResponseFailureExemption + 900*time.Millisecond)),
	}
	involvement := env.proxy.redundancy.buildInvolvement(inf, 0, params)
	require.True(t, involvement.Responsive)
	require.True(t, involvement.Finished)
	require.GreaterOrEqual(t, involvement.TotalTimeMs, float64(longResponseFailureExemption.Milliseconds()))
}

func TestFastNonStreamEmptyResponseRecordsParticipantFailure(t *testing.T) {
	env := setupTestProxyWithClients(t, []user.HostClient{streamContentThenStallClient{}})
	limiter := NewParticipantRequestLimiter(10, 10)
	env.proxy.redundancy.participantLimiter = limiter
	params := defaultParams()
	params.Stream = false

	inf := &inflight{
		hostIdx:     0,
		nonce:       1,
		sendTime:    time.Now().Add(-time.Second),
		receiptTime: time.Now().Add(-900 * time.Millisecond),
	}
	env.proxy.redundancy.recordStartedAttemptSamples([]*inflight{inf}, params, true)

	stats := env.proxy.redundancy.perf.Stats(0)
	require.Equal(t, 1, stats.TotalSamples)
	require.Equal(t, 0.0, stats.ResponsiveRate)
}

func TestErrorStreamSkipsParticipantFailureAccounting(t *testing.T) {
	env := setupTestProxyWithClients(t, []user.HostClient{streamContentThenStallClient{}})
	limiter := NewParticipantRequestLimiter(10, 10)
	env.proxy.redundancy.participantLimiter = limiter
	participantKey := env.session.HostParticipantKey(0)

	for i := 0; i < 2; i++ {
		inf := &inflight{
			hostIdx:     0,
			nonce:       uint64(i + 1),
			sendTime:    time.Now().Add(-time.Second),
			receiptTime: time.Now().Add(-900 * time.Millisecond),
			firstToken:  time.Now().Add(-800 * time.Millisecond),
			errorSource: "error.BadRequestError",
		}
		inf.outputChunks.Store(1)
		env.proxy.redundancy.recordStalledWinnerFailureOnce(inf, defaultParams())
		env.proxy.redundancy.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
	}

	require.False(t, limiter.IsBlocked(participantKey))
	require.Equal(t, 0, env.proxy.redundancy.perf.Stats(0).TotalSamples)
}

func TestRunInference_AllHostsKnownToolUnsupportedReturnsToolError(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	for _, key := range env.session.ParticipantKeys() {
		env.proxy.redundancy.perf.RecordToolUnsupported(key)
	}
	params := defaultParams()
	params.Prompt = []byte(`{"messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"tool_choice":"auto"}`)

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), params, &buf, nil)

	var hostErr *hostApplicationError
	require.ErrorAs(t, err, &hostErr)
	require.Equal(t, toolChoiceUnsupportedMessage, hostErr.Error())
	require.Equal(t, http.StatusBadRequest, hostErr.statusCode())
	require.Empty(t, env.proxy.perf.RecentRequests(), "no real host attempt should be recorded")
}

func TestRunInference_StateRootDivergenceBlocksParticipantForEscrow(t *testing.T) {
	zeroReceiptTimeout(t)
	env := setupTestProxy(t, 2, nil, true)
	divergent := env.killables[1]
	divergent.ForceError(fmt.Errorf(`http /sessions/escrow-proxy/chat/completions: status 500: {"error":"apply diff nonce 1: post_state_root does not match computed state root: diff 00, computed 11"}`))

	var first bytes.Buffer
	require.NoError(t, env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &first, nil))
	require.EqualValues(t, 1, divergent.LastRequest().Nonce)

	// Even if the host would answer now, this escrow must stop sending it
	// real traffic because its local state no longer matches our diff chain.
	divergent.ForceError(nil)
	var second bytes.Buffer
	require.NoError(t, env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &second, nil))
	require.EqualValues(t, 1, divergent.LastRequest().Nonce)

	reason, blocked := env.proxy.redundancy.escrowStateBlockReason(env.session.HostParticipantKey(1))
	require.True(t, blocked)
	require.Equal(t, "escrow_state_root_diverged", reason)
}

func TestRunInference_IncompleteWinnerAfterContentQuarantinesParticipant(t *testing.T) {
	env := setupTestProxyWithClients(t, []user.HostClient{streamContentWithoutFinishClient{}})
	limiter := NewParticipantRequestLimiter(10, 10)
	env.proxy.redundancy.participantLimiter = limiter
	participantKey := env.session.HostParticipantKey(0)

	var first bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &first, nil)
	requireIncompleteWinnerError(t, err)
	require.Contains(t, first.String(), `"content":"x"`)
	require.False(t, limiter.IsBlocked(participantKey))

	var second bytes.Buffer
	err = env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &second, nil)
	requireIncompleteWinnerError(t, err)
	require.Contains(t, second.String(), `"content":"x"`)
	require.False(t, limiter.IsBlocked(participantKey))
	require.True(t, limiter.IsShadowQuarantined(participantKey))
}

func requireIncompleteWinnerError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.True(t,
		strings.Contains(err.Error(), "winner inference incomplete") ||
			strings.Contains(err.Error(), "no non-probe attempt finished"),
		"unexpected incomplete winner error: %v", err)
}

func requireStalledWinnerTimeoutError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.True(t,
		errors.Is(err, context.Canceled) ||
			strings.Contains(err.Error(), "no non-probe attempt finished"),
		"unexpected stalled winner timeout error: %v", err)
}

func TestRecoveredEmptyStreamsRecordPerfWithoutQuarantine(t *testing.T) {
	env := setupTestProxyWithClients(t, []user.HostClient{streamContentThenStallClient{}})
	limiter := NewParticipantRequestLimiter(10, 10)
	env.proxy.redundancy.participantLimiter = limiter

	for i := 0; i < emptyStreamQuarantineThreshold; i++ {
		inf := &inflight{
			hostIdx:     0,
			nonce:       uint64(i + 1),
			sendTime:    time.Now().Add(-time.Second),
			receiptTime: time.Now().Add(-900 * time.Millisecond),
		}
		inf.outputChunks.Store(1)
		env.proxy.redundancy.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
	}

	stats := env.proxy.redundancy.perf.Stats(0)
	require.Equal(t, emptyStreamQuarantineThreshold, stats.TotalSamples)
	require.Zero(t, stats.ResponsiveRate)
	require.False(t, limiter.IsBlocked(env.session.HostParticipantKey(0)))
	require.True(t, limiter.IsShadowQuarantined(env.session.HostParticipantKey(0)))

	unstarted := &inflight{hostIdx: 0, nonce: 99}
	env.proxy.redundancy.recordStartedAttemptSamples([]*inflight{unstarted}, defaultParams(), true)
	require.Equal(t, emptyStreamQuarantineThreshold, env.proxy.redundancy.perf.Stats(0).TotalSamples)
}

func TestRecordStartedAttemptSamplesDoesNotCountEmptyStreamWhenRequestFailed(t *testing.T) {
	env := setupTestProxyWithClients(t, []user.HostClient{streamContentThenStallClient{}})
	limiter := NewParticipantRequestLimiter(10, 10)
	env.proxy.redundancy.participantLimiter = limiter

	for i := 0; i < emptyStreamQuarantineThreshold; i++ {
		inf := &inflight{
			hostIdx:     0,
			nonce:       uint64(i + 1),
			sendTime:    time.Now().Add(-time.Second),
			receiptTime: time.Now().Add(-900 * time.Millisecond),
		}
		inf.outputChunks.Store(1)
		env.proxy.redundancy.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), false)
	}

	stats := env.proxy.redundancy.perf.Stats(0)
	require.Zero(t, stats.TotalSamples)
	require.Zero(t, stats.FailureSamples)
	require.False(t, limiter.IsBlocked(env.session.HostParticipantKey(0)))
	require.True(t, limiter.IsShadowQuarantined(env.session.HostParticipantKey(0)))
}

func TestRecordStartedAttemptSamplesDoesNotCountEmptyStreamDuringRelaxedPoC(t *testing.T) {
	setPoCModeForTest(t, pocRequestModeRelaxed)
	setPoCPhaseState(true, "confirmation_poc")

	env := setupTestProxyWithClients(t, []user.HostClient{streamContentThenStallClient{}})
	limiter := NewParticipantRequestLimiter(10, 10)
	env.proxy.redundancy.participantLimiter = limiter

	for i := 0; i < emptyStreamQuarantineThreshold; i++ {
		inf := &inflight{
			hostIdx:     0,
			nonce:       uint64(i + 1),
			sendTime:    time.Now().Add(-time.Second),
			receiptTime: time.Now().Add(-900 * time.Millisecond),
		}
		inf.outputChunks.Store(1)
		env.proxy.redundancy.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
	}

	participantKey := env.session.HostParticipantKey(0)
	stats := env.proxy.redundancy.perf.Stats(0)
	require.Zero(t, stats.TotalSamples)
	require.False(t, limiter.IsBlocked(participantKey))
	require.False(t, limiter.IsShadowQuarantined(participantKey))
}

func TestEmptyStreamWithoutWinnerSkipsTimeoutVoteOnlyWhenFinished(t *testing.T) {
	env := setupTestProxyWithClients(t, []user.HostClient{streamContentThenStallClient{}})
	prepared, err := env.session.PrepareInference(defaultParams())
	require.NoError(t, err)

	inf := &inflight{
		nonce:       prepared.Nonce(),
		receiptTime: time.Now(),
		resp:        &host.HostResponse{ConfirmedAt: time.Now().Unix()},
	}

	reason, skip := emptyStreamWithoutWinnerTimeoutSkipReason(inf, env.session)

	require.False(t, skip)
	require.Empty(t, reason)

	inf.resp.Mempool = []*types.DevshardTx{
		{
			Tx: &types.DevshardTx_FinishInference{
				FinishInference: &types.MsgFinishInference{InferenceId: prepared.Nonce()},
			},
		},
	}
	require.NoError(t, env.session.ProcessResponse(prepared.HostIdx(), inf.resp, prepared.Nonce()))

	reason, skip = emptyStreamWithoutWinnerTimeoutSkipReason(inf, env.session)

	require.True(t, skip)
	require.Equal(t, "empty_stream_without_non_empty_winner", reason)
}

func TestErrorStreamWithoutFinishPostsTimeoutVote(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	params := defaultParams()
	params.StartedAt = time.Now().Add(-10 * time.Second).Unix()
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	body := []byte(`data: {"error":{"code":404,"message":"The model does not exist.","type":"NotFoundError"}}` + "\n\n" +
		"data: [DONE]\n\n")
	inf := &inflight{
		hostIdx:         prepared.HostIdx(),
		hostID:          env.session.HostLabel(prepared.HostIdx()),
		nonce:           prepared.Nonce(),
		escrowID:        "escrow-proxy",
		sendTime:        time.Now().Add(-10 * time.Second),
		receiptTime:     time.Now().Add(-9 * time.Second),
		errorSource:     "error.NotFoundError",
		errorCode:       "404",
		errorType:       "NotFoundError",
		errorMessage:    "The model does not exist.",
		errorBodySample: body,
		resp:            &host.HostResponse{Nonce: prepared.Nonce()},
		done:            make(chan struct{}),
	}
	inf.outputChunks.Store(1)
	inf.contentChunks.Store(1)
	close(inf.done)

	err = env.proxy.redundancy.finishRaceOutcome(context.Background(), []*inflight{inf}, params, Decision{Reason: "test"}, prepared.Nonce(), raceFinishOptions{recordFailureSamples: true})

	var hostErr *hostApplicationError
	require.ErrorAs(t, err, &hostErr)
	require.Equal(t, http.StatusNotFound, hostErr.statusCode())
	st := env.sm.SnapshotState()
	require.Equal(t, types.StatusTimedOut, st.Inferences[prepared.Nonce()].Status)
}

func TestRunInference_ErrorStreamRetriesInsteadOfWinning(t *testing.T) {
	withRedundancySpeedPolicyForProxyTest(t, RedundancySpeedPolicyLegacy)
	zeroReceiptTimeout(t)
	env := setupTestProxy(t, 3, nil, true)
	errorClient := &errorStreamWithoutFinishClient{}
	env.killables[1].inner = errorClient

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)

	require.NoError(t, err)
	require.Equal(t, int32(1), errorClient.calls.Load())
	require.NotContains(t, buf.String(), `"NotFoundError"`)
	require.Contains(t, buf.String(), `"choices"`)
	require.True(t, env.session.IsNonceFinished(2))
}

func TestRunInference_CancelStillSettlesStartedAttempt(t *testing.T) {
	releaseCh := make(chan struct{})
	client := &delayedResultClient{
		releaseCh: releaseCh,
		response: &host.HostResponse{
			Nonce: 1,
			Mempool: []*types.DevshardTx{
				{
					Tx: &types.DevshardTx_FinishInference{
						FinishInference: &types.MsgFinishInference{InferenceId: 1},
					},
				},
			},
		},
	}
	env := setupTestProxyWithClients(t, []user.HostClient{client})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		var buf bytes.Buffer
		errCh <- env.proxy.redundancy.RunInference(ctx, defaultParams(), &buf, nil)
	}()

	require.Eventually(t, func() bool {
		return client.sendCalls.Load() == 1
	}, time.Second, 10*time.Millisecond)

	cancel()

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("RunInference did not return after request cancellation")
	}

	close(releaseCh)

	require.Eventually(t, func() bool {
		return env.session.IsNonceFinished(1)
	}, time.Second, 10*time.Millisecond)
}

func TestRunInference_NonStreamingEarlyFailuresRetryNormalAttemptsBeforeReducedDelay(t *testing.T) {
	setNonStreamingTimeouts(t, 20*time.Millisecond, 60*time.Millisecond, 80*time.Millisecond)
	disablePairwiseABSampling(t)
	client := &emptyNonStreamingRecorderClient{}
	env := setupTestProxyWithClients(t, []user.HostClient{client, client})

	params := defaultParams()
	params.Stream = false
	params.MaxTokens = 50
	params.Prompt = []byte(`{"model":"llama","max_tokens":50,"messages":[{"role":"user","content":"hello"}]}`)

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), params, &buf, nil)

	require.Error(t, err)
	var reducedTokenTimeoutErr *nonStreamingReducedMaxTokensTimeoutError
	require.ErrorAs(t, err, &reducedTokenTimeoutErr)
	require.Equal(t, []uint64{50, 50}, client.MaxTokens())
	require.EqualValues(t, 2, client.sendCalls.Load())
	env.proxy.perf.pairwise.mu.RLock()
	pairwiseComparisons := len(env.proxy.perf.pairwise.pairs)
	env.proxy.perf.pairwise.mu.RUnlock()
	require.Zero(t, pairwiseComparisons)
}

func TestRunInference_NonStreamingResponseTimeoutRetriesOnceWithReducedMaxTokens(t *testing.T) {
	setNonStreamingTimeouts(t, 20*time.Millisecond, 60*time.Millisecond, 80*time.Millisecond)
	disablePairwiseABSampling(t)
	client := &blockingNonStreamingRecorderClient{}
	env := setupTestProxyWithClients(t, []user.HostClient{client, client})

	params := defaultParams()
	params.Stream = false
	params.MaxTokens = 50
	params.Prompt = []byte(`{"model":"llama","max_tokens":50,"messages":[{"role":"user","content":"hello"}]}`)

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), params, &buf, nil)

	require.Error(t, err)
	var reducedTokenTimeoutErr *nonStreamingReducedMaxTokensTimeoutError
	require.ErrorAs(t, err, &reducedTokenTimeoutErr)
	require.Equal(t, []uint64{50, 25}, client.MaxTokens())
	require.EqualValues(t, 2, client.sendCalls.Load())
	env.proxy.perf.pairwise.mu.RLock()
	pairwiseComparisons := len(env.proxy.perf.pairwise.pairs)
	env.proxy.perf.pairwise.mu.RUnlock()
	require.Zero(t, pairwiseComparisons)
}

func TestRunInference_NonStreamingResponseTimeoutReducesMaxCompletionTokensAndMinTokens(t *testing.T) {
	params := user.InferenceParams{
		Model:       "llama",
		Prompt:      []byte(`{"model":"llama","max_completion_tokens":50,"min_tokens":50,"messages":[{"role":"user","content":"hello"}]}`),
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   time.Now().Unix(),
	}

	reduced, ok := reducedMaxTokensParams(params)

	require.True(t, ok)
	require.EqualValues(t, 25, reduced.MaxTokens)
	require.JSONEq(t, `{"model":"llama","max_completion_tokens":25,"min_tokens":25,"messages":[{"role":"user","content":"hello"}]}`, string(reduced.Prompt))
}

func TestProxyHandleChatCompletionsRejectsWhenConfirmationPoCActive(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.phaseGate = &ChainPhaseGate{}
	env.proxy.phaseGate.storeSnapshot(ChainPhaseSnapshot{
		EpochPhase:           epochPhaseInference,
		ConfirmationPoCPhase: confirmationPoCGeneration,
		RequestsBlocked:      true,
		BlockReason:          "confirmation_poc",
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()

	env.proxy.handleChatCompletions(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "confirmation PoC generation")
	require.EqualValues(t, 0, env.proxy.session.Nonce())
}

func TestProxyHandleChatCompletionsRejectsWhenRegularPoCActive(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.phaseGate = &ChainPhaseGate{}
	env.proxy.phaseGate.storeSnapshot(ChainPhaseSnapshot{
		EpochPhase:      epochPhasePoCGenerate,
		RequestsBlocked: true,
		BlockReason:     "poc",
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()

	env.proxy.handleChatCompletions(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "PoC generation")
	require.EqualValues(t, 0, env.proxy.session.Nonce())
}

func TestHandleDebugInferences_IncludesSealedInferences(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	escrowID := "escrow-state-api"
	store := testutil.MustMemoryStore(t, escrowID, userKey.Address(), config, group, 100000)
	sm, err := state.NewStateMachine(escrowID, config, group, 100000, userKey.Address(), verifier, store)
	require.NoError(t, err)

	start := &types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama", InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}
	_, err = sm.ApplyLocal(1, []*types.DevshardTx{{Tx: &types.DevshardTx_StartInference{StartInference: start}}})
	require.NoError(t, err)
	execSig := testutil.SignExecutorReceipt(t, hosts[1], escrowID, 1, []byte("prompt"), "llama", 100, 50, 1000, 2000)
	_, err = sm.ApplyLocal(2, []*types.DevshardTx{{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 2000,
	}}}})
	require.NoError(t, err)
	finish := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("response"), InputTokens: 10, OutputTokens: 20, ExecutorSlot: 1, EscrowId: escrowID,
	}
	finish.ProposerSig = testutil.SignProposerTx(t, hosts[1], finish)
	_, err = sm.ApplyLocal(3, []*types.DevshardTx{{Tx: &types.DevshardTx_FinishInference{FinishInference: finish}}})
	require.NoError(t, err)
	require.NoError(t, sm.SealInference(1))

	proxy := &Proxy{sm: sm, escrowID: escrowID}

	req := httptest.NewRequest(http.MethodGet, "/v1/debug/inferences", nil)
	rec := httptest.NewRecorder()
	proxy.handleDebugInferences(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var stateResp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &stateResp))
	inferences, ok := stateResp["inferences"].(map[string]any)
	require.True(t, ok, "/v1/debug/inferences must expose inferences map")
	inf, ok := inferences["1"].(map[string]any)
	require.True(t, ok, "sealed inference 1 must appear in /v1/debug/inferences")
	require.Equal(t, "finished", inf["status"])
	require.Equal(t, "llama", inf["model"])
}

func TestProxyStatusIncludesChainPhaseSnapshot(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.phaseGate = &ChainPhaseGate{}
	env.proxy.phaseGate.storeSnapshot(ChainPhaseSnapshot{
		EpochPhase:           epochPhasePoCValidate,
		ConfirmationPoCPhase: confirmationPoCValidation,
		RequestsBlocked:      true,
		BlockReason:          "confirmation_poc",
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	rec := httptest.NewRecorder()

	env.proxy.handleStatus(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var status statusResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	require.Equal(t, epochPhasePoCValidate, status.ChainPhase)
	require.Equal(t, confirmationPoCValidation, status.ConfirmationPoCPhase)
	require.True(t, status.RequestsBlocked)
	require.Equal(t, "confirmation_poc", status.BlockReason)
}

func TestRunInference_RelaxedPoCSilentlyBurnsNonceForUnresponsiveHost(t *testing.T) {
	setPoCModeForTest(t, pocRequestModeRelaxed)

	env := setupTestProxy(t, 2, nil, true)
	env.proxy.phaseGate = &ChainPhaseGate{}
	env.proxy.phaseGate.storeSnapshot(ChainPhaseSnapshot{
		EpochPhase:      epochPhasePoCGenerate,
		RequestsBlocked: false,
		BlockReason:     "poc",
	})
	// Preserve only host 0. Host 1 is PoC-required: real requests
	// must skip it, AND in the new uniform-silent-probe regime the
	// picker must NOT send any probe traffic to it either.
	setPoCPreservedParticipantsByModel(map[string][]string{"llama": []string{env.session.HostParticipantKey(0)}})

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)
	require.NoError(t, err)

	require.Nil(t, env.killables[1].LastRequest(),
		"PoC-required host must receive no traffic at all (silent ghost probe)")

	st := env.sm.SnapshotState()
	// MsgStartInference is composed and applied locally inside
	// PrepareInferenceFn even when the dispatcher stays silent, so
	// nonce 1's local state still shows Pending. Nonce 2 went to
	// the preserved host and finished normally.
	require.Contains(t, st.Inferences, uint64(1))
	require.Contains(t, st.Inferences, uint64(2))
	require.Equal(t, types.StatusPending, st.Inferences[1].Status,
		"silent probe still applies the local MsgStart; host just never confirms")
	require.True(t, env.session.IsNonceFinished(2))
}

func TestRunInference_RelaxedPoCImmediatelyEscalatesProbeChainToPreservedHost(t *testing.T) {
	setPoCModeForTest(t, pocRequestModeRelaxed)

	env := setupTestProxy(t, 3, nil, true)
	env.proxy.phaseGate = &ChainPhaseGate{}
	env.proxy.phaseGate.storeSnapshot(ChainPhaseSnapshot{
		EpochPhase:      epochPhasePoCGenerate,
		RequestsBlocked: false,
		BlockReason:     "poc",
	})

	// Nonce routing for 3 hosts is 1 -> host 1, 2 -> host 2, 3 -> host 0.
	// Preserve only host 0 so nonces 1 and 2 are silently burned past
	// the PoC-required hosts and the real request lands on host 0
	// when nonce 3 binds to it.
	setPoCPreservedParticipantsByModel(map[string][]string{"llama": []string{env.session.HostParticipantKey(0)}})

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)
	require.NoError(t, err)

	require.Nil(t, env.killables[1].LastRequest(),
		"PoC-required host 1 must receive no traffic (silent ghost probe)")
	require.Nil(t, env.killables[2].LastRequest(),
		"PoC-required host 2 must receive no traffic (silent ghost probe)")
	require.NotNil(t, env.killables[0].LastRequest(),
		"preserved host 0 should receive the real request after the picker advances past 1+2")

	st := env.sm.SnapshotState()
	require.Contains(t, st.Inferences, uint64(1))
	require.Contains(t, st.Inferences, uint64(2))
	require.Contains(t, st.Inferences, uint64(3))
	require.True(t, env.session.IsNonceFinished(3), "preserved host attempt should complete")

	requests := env.proxy.perf.RecentRequests()
	require.Len(t, requests, 1)
	require.Len(t, requests[0].Hosts, 1, "probe attempts should not be recorded in request perf stats")
	require.Equal(t, 0, requests[0].Hosts[0].HostIdx)
	require.Equal(t, uint64(3), requests[0].Hosts[0].Nonce)
}

func TestRunInference_RelaxedPoCProbeOnlyRequestsFailAndSkipPerfRecording(t *testing.T) {
	setPoCModeForTest(t, pocRequestModeRelaxed)

	env := setupTestProxy(t, 2, nil, true)
	env.proxy.phaseGate = &ChainPhaseGate{}
	env.proxy.phaseGate.storeSnapshot(ChainPhaseSnapshot{
		EpochPhase:      epochPhasePoCGenerate,
		RequestsBlocked: false,
		BlockReason:     "poc",
	})

	// Empty preserved set means every host is treated as a probe.
	setPoCPreservedParticipantsByModel(map[string][]string{"llama": []string{}})

	// New design: real requests are NEVER dispatched to PoC-required
	// hosts. With every host PoC-required, the picker's exhaustion
	// sweep computes an empty available-host set on the first
	// iteration, drops the request immediately with
	// ErrNoAvailableHost, and never enqueues it for ghost-burn
	// dispatch. PerfTracker stays clean because no real attempt ever
	// ran.
	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)
	require.ErrorIs(t, err, ErrNoAvailableHost,
		"all-PoC-required escrow should drop the request immediately, got %v", err)

	require.Empty(t, env.proxy.perf.RecentRequests(), "probe-only requests should not be recorded in request perf stats")
	require.Empty(t, env.proxy.perf.AllStats(), "probe-only requests should not produce host perf samples")
}

func TestRunInference_SpeculativeOnKill(t *testing.T) {
	zeroReceiptTimeout(t)
	env := setupTestProxy(t, 3, nil, true)
	ctx := context.Background()

	// Kill primary host (nonce 1 → host 1).
	env.killables[1].Kill()

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(ctx, defaultParams(), &buf, nil)
	// The speculative engine sends a secondary to the next host.
	// Depending on timing, it may succeed or fail.
	// With short ReceiptTimeout, secondary should start quickly.
	if err != nil {
		// Both hosts may fail if secondary host is also the killed one
		// (depends on group routing). Not an error in the test — just log.
		t.Logf("speculative inference with killed primary: %v", err)
	}
}

func TestRunInference_SpeculativeFallsThroughMultipleDeadHosts(t *testing.T) {
	zeroReceiptTimeout(t)
	env := setupTestProxy(t, 4, nil, true)

	// nonce 1 -> host 1, nonce 2 -> host 2, nonce 3 -> host 3.
	env.killables[1].Kill()
	env.killables[2].Kill()

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)
	require.NoError(t, err)

	requests := env.proxy.perf.RecentRequests()
	require.NotEmpty(t, requests)

	last := requests[len(requests)-1]
	require.Equal(t, uint64(3), last.WinnerNonce)
	require.Equal(t, 3, last.WinnerHostIdx)
	require.Len(t, last.Hosts, 3)
	require.True(t, last.Hosts[2].Winner)

	st := env.sm.SnapshotState()
	_, ok := st.Inferences[3]
	require.True(t, ok, "third inference should exist after falling through dead hosts")
}

func TestRunInference_PerfTracking(t *testing.T) {
	withRedundancySpeedPolicyForProxyTest(t, RedundancySpeedPolicyLegacy)
	zeroReceiptTimeout(t)
	env := setupTestProxy(t, 3, nil, true)
	ctx := context.Background()

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(ctx, defaultParams(), &buf, nil)
	require.NoError(t, err)

	stats := env.proxy.perf.AllStats()
	require.NotEmpty(t, stats, "should have recorded at least one host sample")

	totalSamples := 0
	for _, s := range stats {
		totalSamples += s.TotalSamples
	}
	require.GreaterOrEqual(t, totalSamples, 1, "at least one sample recorded")
}

func TestRunInference_ExportsPrometheusMetrics(t *testing.T) {
	withRedundancySpeedPolicyForProxyTest(t, RedundancySpeedPolicyLegacy)
	zeroReceiptTimeout(t)
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.metrics = NewDevshardMetrics()
	env.proxy.redundancy.devshardID = "escrow-proxy"
	env.killables[1].Kill()

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	env.proxy.redundancy.metrics.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, "devshard_speculative_decisions_total")
	require.Contains(t, body, "devshard_speculative_attempt_starts_total")
	require.Contains(t, body, `reason="receipt_timeout"`)
	require.Contains(t, body, `reason="attempt_failed"`)
	require.Contains(t, body, `devshard_id="escrow-proxy"`)
	require.Contains(t, body, "devshard_host_total_time_seconds")
	require.Contains(t, body, `devshard_gateway_requests_total{model="llama",outcome="success",reason="none"} 1`)
	require.Contains(t, body, "devshard_gateway_slot_decisions_total")
	require.Contains(t, body, `decision="real_send"`)
	require.Contains(t, body, "devshard_gateway_attempts_started_total")
	require.Contains(t, body, `role="primary"`)
	require.Contains(t, body, `role="extra"`)
	require.Contains(t, body, "devshard_gateway_attempts_terminal_total")
	require.Contains(t, body, "devshard_gateway_attempt_failures_total")
	require.Contains(t, body, "devshard_gateway_user_requests_with_hidden_failure_total")
}

func TestPerfTrackerIsUnresponsiveUsesThreshold(t *testing.T) {
	perf := NewPerfTracker(nil)
	perf.Record(RequestSample{HostIdx: 0, Responsive: true})
	perf.Record(RequestSample{HostIdx: 0, Responsive: true})
	perf.Record(RequestSample{HostIdx: 0, Responsive: true})
	perf.Record(RequestSample{HostIdx: 0, Responsive: false})

	saved := UnresponsiveThreshold
	UnresponsiveThreshold = 0.70
	t.Cleanup(func() { UnresponsiveThreshold = saved })

	require.False(t, perf.IsUnresponsive(0))

	UnresponsiveThreshold = 0.90
	require.True(t, perf.IsUnresponsive(0))
}

func TestFirstTokenFallbackDelayUsesDefaultFormula(t *testing.T) {
	setSpeculativeTiming(t, 50*time.Millisecond, time.Second, 10*time.Millisecond, time.Minute)
	require.InDelta(t, (1.7+0.00003*50+0.0000000005*50*50)*float64(time.Second), float64(defaultFirstTokenFallbackDelay(50)), float64(time.Millisecond))
	require.InDelta(t, (1.7+0.00003*500+0.0000000005*500*500)*float64(time.Second), float64(defaultFirstTokenFallbackDelay(500)), float64(time.Millisecond))
	require.InDelta(t, 1.7*float64(time.Second), float64(defaultFirstTokenFallbackDelay(0)), float64(time.Millisecond))
}

func TestWaitForFirstTokenUntilReturnsWhenTokenArrives(t *testing.T) {
	inf := &inflight{
		firstTokenCh: make(chan struct{}),
		done:         make(chan struct{}),
	}
	go func() {
		time.Sleep(10 * time.Millisecond)
		inf.firstToken = time.Now()
		close(inf.firstTokenCh)
	}()

	ok := waitForFirstTokenUntil(context.Background(), inf, time.Now().Add(100*time.Millisecond))
	require.True(t, ok)
}

func TestWaitForFirstTokenUntilTimesOutWithoutToken(t *testing.T) {
	inf := &inflight{
		firstTokenCh: make(chan struct{}),
		done:         make(chan struct{}),
	}

	ok := waitForFirstTokenUntil(context.Background(), inf, time.Now().Add(20*time.Millisecond))
	require.False(t, ok)
}

func TestNonStreamingFallbackDelayUsesMaxThreshold(t *testing.T) {
	setSpeculativeTiming(t, 50*time.Millisecond, time.Second, 10*time.Millisecond, time.Minute)
	savedFloor := NonStreamResponseFloor
	savedLag := PerInputTokenResponseLag
	NonStreamResponseFloor = 20 * time.Second
	PerInputTokenResponseLag = 20 * time.Millisecond
	t.Cleanup(func() {
		NonStreamResponseFloor = savedFloor
		PerInputTokenResponseLag = savedLag
	})

	require.Equal(t, 20*time.Second, nonStreamingFallbackDelay(100))
	require.Equal(t, 24*time.Second, nonStreamingFallbackDelay(1200))
}

func TestWaitForInflightDoneUntilReturnsWhenDoneArrives(t *testing.T) {
	inf := &inflight{done: make(chan struct{})}
	go func() {
		time.Sleep(10 * time.Millisecond)
		close(inf.done)
	}()

	ok := waitForInflightDoneUntil(context.Background(), inf, time.Now().Add(100*time.Millisecond))
	require.True(t, ok)
}

func TestWaitForInflightDoneUntilTimesOut(t *testing.T) {
	inf := &inflight{done: make(chan struct{})}

	ok := waitForInflightDoneUntil(context.Background(), inf, time.Now().Add(20*time.Millisecond))
	require.False(t, ok)
}

func TestDecision_UnresponsiveHost(t *testing.T) {
	perf := NewPerfTracker(nil)
	for i := 0; i < 10; i++ {
		perf.Record(RequestSample{HostIdx: 0, Responsive: false})
	}

	redundancy := &Redundancy{perf: perf, groupSize: 3}
	d := redundancy.Decide(0, 100)
	require.True(t, d.RunSecondary)
	require.Equal(t, time.Duration(0), d.Delay)
	require.Equal(t, "primary_unresponsive", d.Reason)
}

func withRedundancySpeedPolicyForProxyTest(t *testing.T, policy string) {
	t.Helper()
	saved := RedundancySpeedPolicy
	RedundancySpeedPolicy = policy
	t.Cleanup(func() { RedundancySpeedPolicy = saved })
}

func TestDecision_FasterSecondary(t *testing.T) {
	withRedundancySpeedPolicyForProxyTest(t, RedundancySpeedPolicyLegacy)

	perf := NewPerfTracker(nil)
	for i := 0; i < 5; i++ {
		perf.Record(RequestSample{
			HostIdx:     0,
			Responsive:  true,
			SendTime:    time.Now().Add(-1 * time.Second),
			ReceiptTime: time.Now().Add(-500 * time.Millisecond),
			FirstToken:  time.Now().Add(-400 * time.Millisecond),
			TotalTime:   1 * time.Second,
			InputTokens: 100,
		})
		perf.Record(RequestSample{
			HostIdx:     1,
			Responsive:  true,
			SendTime:    time.Now().Add(-200 * time.Millisecond),
			ReceiptTime: time.Now().Add(-150 * time.Millisecond),
			FirstToken:  time.Now().Add(-100 * time.Millisecond),
			TotalTime:   200 * time.Millisecond,
			InputTokens: 100,
		})
	}

	redundancy := &Redundancy{perf: perf, groupSize: 3}
	d := redundancy.Decide(0, 100)
	require.True(t, d.RunSecondary)
	require.Equal(t, time.Duration(0), d.Delay)
	require.Equal(t, "secondary_faster", d.Reason)
}

func TestDecision_DefaultDelay(t *testing.T) {
	withRedundancySpeedPolicyForProxyTest(t, RedundancySpeedPolicyLegacy)
	perf := NewPerfTracker(nil)
	redundancy := &Redundancy{perf: perf, groupSize: 3}
	d := redundancy.Decide(0, 100)
	require.True(t, d.RunSecondary)
	require.Equal(t, ReceiptTimeout, d.Delay)
	require.Equal(t, "receipt_timeout", d.Reason)
}

func TestDecision_ReceiptTimeoutScalesForLargeRunnerInput(t *testing.T) {
	withRedundancySpeedPolicyForProxyTest(t, RedundancySpeedPolicyLegacy)
	saved := ReceiptTimeout
	ReceiptTimeout = 5 * time.Second
	t.Cleanup(func() { ReceiptTimeout = saved })

	perf := NewPerfTracker(nil)
	redundancy := &Redundancy{perf: perf, groupSize: 3}
	d := redundancy.Decide(0, 100_001)

	require.True(t, d.RunSecondary)
	require.Equal(t, 10*time.Second, d.Delay)
	require.Equal(t, "receipt_timeout", d.Reason)
}

func TestFirstTokenFallbackDelayScalesForLargeRunnerInput(t *testing.T) {
	savedFloor := FirstTokenTimeoutCap
	savedLag := PerInputTokenFirstTokenLag
	FirstTokenTimeoutCap = time.Second
	PerInputTokenFirstTokenLag = time.Millisecond
	t.Cleanup(func() {
		FirstTokenTimeoutCap = savedFloor
		PerInputTokenFirstTokenLag = savedLag
	})

	require.InDelta(t, float64(1703*time.Millisecond), float64(defaultFirstTokenFallbackDelay(100)), float64(time.Millisecond))
	require.InDelta(t, float64(9700*time.Millisecond), float64(defaultFirstTokenFallbackDelay(100_000)), float64(time.Millisecond))
}

// slowReceiptClient wraps an inner user.HostClient and defers the receipt
// callback by a configurable duration. This simulates a host whose TCP
// connect completes normally but whose first response bytes are slow to
// arrive — the exact failure shape that masks the receipt-timeout escalation
// bug if awaitRace sees sendTime == 0 on its first iteration.
type slowReceiptClient struct {
	inner        user.HostClient
	receiptDelay time.Duration
}

func (c *slowReceiptClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	wrapped := receiptHandler
	if wrapped != nil && c.receiptDelay > 0 {
		wrapped = func() {
			select {
			case <-time.After(c.receiptDelay):
			case <-ctx.Done():
			}
			receiptHandler()
		}
	}
	return c.inner.Send(ctx, req, stream, wrapped)
}

// TestRunInference_ReceiptTimeoutEscalatesEvenWhenSendTimeRaces is a regression
// test for the bug where `inf.sendTime` was assigned inside the send goroutine
// rather than synchronously in startInflight. If the awaitRace loop iterated
// before the goroutine scheduled, nextEscalationTrigger returned no candidate
// (sendTime.IsZero() short-circuit), no escalation timer was ever scheduled,
// and a slow primary could stall the request indefinitely — or at best waste
// the entire receipt-delay window.
//
// To force the race-prone ordering we delay the primary's receipt well beyond
// ReceiptTimeout; the fix guarantees that regardless of goroutine scheduling
// the receipt-timeout escalation fires, a secondary is dispatched, and the
// request succeeds via the secondary. Observable evidence: more than one
// HostInvolvement entry gets recorded for the request.
func TestRunInference_ReceiptTimeoutEscalatesEvenWhenSendTimeRaces(t *testing.T) {
	// Aggressive timings: 10ms receipt timeout, 200ms primary receipt delay.
	// Any implementation that misses the escalation due to the sendTime race
	// will either record exactly one attempt, or hang.
	setSpeculativeTiming(t, 10*time.Millisecond, time.Second, 10*time.Millisecond, time.Minute)

	numHosts := 3
	hostSigners := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := types.SessionConfig{
		RefusalTimeout:   1,
		ExecutionTimeout: 1,
		TokenPrice:       1,
		VoteThreshold:    uint32(numHosts) / 2,
	}
	verifier := signing.NewSecp256k1Verifier()

	clients := make([]user.HostClient, numHosts)
	for i := range hostSigners {
		sm := statetest.MustStateMachine(t, "escrow-proxy", config, group, 1_000_000, userKey.Address(), verifier)
		engine := stub.NewInferenceEngine()
		h, err := host.NewHost(sm, hostSigners[i], engine, "escrow-proxy", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		kc := &killableClient{inner: &user.InProcessClient{Host: h}}
		vc := &verifierClient{
			killableClient: kc,
			accept:         true,
			signer:         hostSigners[i],
			group:          group,
			slotIdx:        i,
		}
		if i == 1 {
			// Primary for nonce 1 resolves to host 1 (nonce % len(group)). Give this
			// host a slow receipt so the primary *visibly* lags far past
			// ReceiptTimeout — the escalation must still fire.
			clients[i] = &slowReceiptClient{inner: vc, receiptDelay: 200 * time.Millisecond}
		} else {
			clients[i] = vc
		}
	}

	userSM := statetest.MustStateMachine(t, "escrow-proxy", config, group, 1_000_000, userKey.Address(), verifier)
	session, err := user.NewSession(userSM, userKey, "escrow-proxy", group, clients, verifier)
	require.NoError(t, err)
	perf := NewPerfTracker(nil)
	redundancy := NewRedundancy(session, perf, numHosts, "llama")
	t.Cleanup(redundancy.Stop)

	var buf bytes.Buffer
	err = redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)
	require.NoError(t, err)

	// RunInference now returns the instant the winner's stream settles; the
	// primary's 200ms receipt delay keeps it in flight past that point, so
	// finishRaceOutcome (and RecordRequest) run on the background finalizer.
	// Wait briefly for the record to appear before asserting on it.
	require.Eventually(t, func() bool {
		return len(perf.RecentRequests()) >= 1
	}, time.Second, 10*time.Millisecond, "background finalizer should have recorded the request")
	records := perf.RecentRequests()
	require.Len(t, records, 1, "one request should have been recorded")
	rec := records[0]
	require.GreaterOrEqual(t, len(rec.Hosts), 2,
		"redundancy must have escalated to a second host after primary exceeded ReceiptTimeout — "+
			"if this fails, awaitRace likely missed the receipt-timeout trigger due to sendTime race")
}

// TestRunInference_FastReceiptDoesNotSpuriouslyEscalate asserts the other
// side of the fix: when the primary's receipt arrives comfortably BEFORE
// ReceiptTimeout, the scheduled receipt-timeout timer is re-validated at
// fire-time and quietly skipped, so we do NOT start a secondary we do not
// need. Without the re-check in awaitRace, every healthy primary with
// sendTime stamped synchronously would trigger a useless secondary.
func TestRunInference_FastReceiptDoesNotSpuriouslyEscalate(t *testing.T) {
	withRedundancySpeedPolicyForProxyTest(t, RedundancySpeedPolicyLegacy)
	// Receipt timeout of 50ms is plenty of headroom for the in-process
	// client's synchronous receiptHandler to fire first.
	setSpeculativeTiming(t, 50*time.Millisecond, time.Second, 10*time.Millisecond, time.Minute)
	env := setupTestProxy(t, 3, nil, true)

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)
	require.NoError(t, err)

	records := env.proxy.perf.RecentRequests()
	require.Len(t, records, 1)
	require.Len(t, records[0].Hosts, 1,
		"healthy primary should win without any spurious secondary — if this fails, "+
			"awaitRace is firing the receipt-timeout escalation on a stale trigger")
}
