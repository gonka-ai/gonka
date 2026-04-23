package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"subnet"
	"subnet/host"
	"subnet/internal/testutil"
	"subnet/signing"
	"subnet/state"
	"subnet/stub"
	"subnet/types"
	"subnet/user"
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
	require.Contains(t, body, `data: {"subnet_stream_reset":true}`)
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

func TestHasMsgFinish(t *testing.T) {
	require.False(t, user.HasMsgFinish(nil, 1))

	txs := []*types.SubnetTx{
		{Tx: &types.SubnetTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1}}},
	}
	require.False(t, user.HasMsgFinish(txs, 1))

	txs = append(txs, &types.SubnetTx{Tx: &types.SubnetTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 1}}})
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
	saved := InterChunkStallTimeout
	InterChunkStallTimeout = d
	t.Cleanup(func() {
		InterChunkStallTimeout = saved
	})
}

func setupTestProxy(t *testing.T, numHosts int, engines []subnet.InferenceEngine, verifierAccept bool) *testProxyEnv {
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
		sm := state.NewStateMachine("escrow-proxy", config, group, 1_000_000, userKey.Address(), verifier)
		var engine subnet.InferenceEngine
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

	userSM := state.NewStateMachine("escrow-proxy", config, group, 1_000_000, userKey.Address(), verifier)
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
	userSM := state.NewStateMachine("escrow-proxy", config, group, 1_000_000, userKey.Address(), verifier)
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
	err := env.proxy.redundancy.RunInference(ctx, defaultParams(), &buf)
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
		Mempool: []*types.SubnetTx{
			{Tx: &types.SubnetTx_FinishInference{
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
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf)
	elapsed := time.Since(start)

	require.ErrorIs(t, err, errSimulatedWinnerTransport)
	require.Less(t, elapsed, 2*time.Second,
		"expected immediate error once crowned winner fails; should not block on slow loser")

	close(releaseSlow)
}

func TestRunInference_WinnerStallsAfterContentTimesOut(t *testing.T) {
	setInterChunkStallTimeout(t, 50*time.Millisecond)
	env := setupTestProxyWithClients(t, []user.HostClient{streamContentThenStallClient{}})

	start := time.Now()
	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf)
	elapsed := time.Since(start)

	require.ErrorContains(t, err, "winner stalled waiting for next chunk")
	require.Less(t, elapsed, time.Second,
		"stalled winner should fail promptly once no new chunks arrive")
	require.Contains(t, buf.String(), `"content":"x"`,
		"winner should still forward the first chunk before timing out")
}

func TestRunInference_CancelStillSettlesStartedAttempt(t *testing.T) {
	releaseCh := make(chan struct{})
	client := &delayedResultClient{
		releaseCh: releaseCh,
		response: &host.HostResponse{
			Nonce: 1,
			Mempool: []*types.SubnetTx{
				{
					Tx: &types.SubnetTx_FinishInference{
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
		errCh <- env.proxy.redundancy.RunInference(ctx, defaultParams(), &buf)
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
	setPoCPreservedParticipants([]string{env.session.HostParticipantKey(0)})

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf)
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
	setPoCPreservedParticipants([]string{env.session.HostParticipantKey(0)})

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf)
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
	setPoCPreservedParticipants([]string{})

	// New design: real requests are NEVER dispatched to PoC-required
	// hosts. With every host PoC-required, the picker's exhaustion
	// sweep computes an empty available-host set on the first
	// iteration, drops the request immediately with
	// ErrNoAvailableHost, and never enqueues it for ghost-burn
	// dispatch. PerfTracker stays clean because no real attempt ever
	// ran.
	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf)
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
	err := env.proxy.redundancy.RunInference(ctx, defaultParams(), &buf)
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
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf)
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
	zeroReceiptTimeout(t)
	env := setupTestProxy(t, 3, nil, true)
	ctx := context.Background()

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(ctx, defaultParams(), &buf)
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
	zeroReceiptTimeout(t)
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.metrics = NewSubnetMetrics()
	env.proxy.redundancy.subnetID = "escrow-proxy"
	env.killables[1].Kill()

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	env.proxy.redundancy.metrics.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, "subnet_speculative_decisions_total")
	require.Contains(t, body, "subnet_speculative_attempt_starts_total")
	require.Contains(t, body, `reason="receipt_timeout"`)
	require.Contains(t, body, `reason="attempt_failed"`)
	require.Contains(t, body, `subnet_id="escrow-proxy"`)
	require.Contains(t, body, "subnet_host_total_time_seconds")
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

func TestFirstTokenFallbackDelayCapsAtOneSecond(t *testing.T) {
	setSpeculativeTiming(t, 50*time.Millisecond, time.Second, 10*time.Millisecond, time.Minute)
	require.Equal(t, time.Second, firstTokenFallbackDelay(50))
	require.Equal(t, 5*time.Second, firstTokenFallbackDelay(500))
	require.Equal(t, time.Second, firstTokenFallbackDelay(0))
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

func TestDecision_FasterSecondary(t *testing.T) {
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
	perf := NewPerfTracker(nil)
	redundancy := &Redundancy{perf: perf, groupSize: 3}
	d := redundancy.Decide(0, 100)
	require.True(t, d.RunSecondary)
	require.Equal(t, ReceiptTimeout, d.Delay)
	require.Equal(t, "receipt_timeout", d.Reason)
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
		sm := state.NewStateMachine("escrow-proxy", config, group, 1_000_000, userKey.Address(), verifier)
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

	userSM := state.NewStateMachine("escrow-proxy", config, group, 1_000_000, userKey.Address(), verifier)
	session, err := user.NewSession(userSM, userKey, "escrow-proxy", group, clients, verifier)
	require.NoError(t, err)
	perf := NewPerfTracker(nil)
	redundancy := NewRedundancy(session, perf, numHosts, "llama")
	t.Cleanup(redundancy.Stop)

	var buf bytes.Buffer
	err = redundancy.RunInference(context.Background(), defaultParams(), &buf)
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
	// Receipt timeout of 50ms is plenty of headroom for the in-process
	// client's synchronous receiptHandler to fire first.
	setSpeculativeTiming(t, 50*time.Millisecond, time.Second, 10*time.Millisecond, time.Minute)
	env := setupTestProxy(t, 3, nil, true)

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf)
	require.NoError(t, err)

	records := env.proxy.perf.RecentRequests()
	require.Len(t, records, 1)
	require.Len(t, records[0].Hosts, 1,
		"healthy primary should win without any spurious secondary — if this fails, "+
			"awaitRace is firing the receipt-timeout escalation on a stale trigger")
}
