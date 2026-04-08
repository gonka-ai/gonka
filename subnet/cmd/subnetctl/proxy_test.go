package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"subnet/transport"
	"subnet/types"
	"subnet/user"
)

// --- Existing tests ---

func TestStreamReset_WrittenOnReconnect(t *testing.T) {
	rec := httptest.NewRecorder()
	writeStreamReset(rec)

	body := rec.Body.String()
	require.Contains(t, body, `data: {"subnet_stream_reset":true}`)
}

func TestStreamRegistry_ForwardAndReset(t *testing.T) {
	var buf bytes.Buffer
	reg := newStreamRegistry()

	nonce := uint64(42)
	reg.register(nonce, &buf)

	// Forward lines.
	reg.callback(nonce, "data: line1")
	reg.callback(nonce, "data: line2")
	require.Contains(t, buf.String(), "data: line1")
	require.Contains(t, buf.String(), "data: line2")

	// Write stream reset, then replay.
	writeStreamReset(&buf)
	reg.callback(nonce, "data: line1")
	reg.callback(nonce, "data: line2")
	reg.callback(nonce, "data: line3")

	// All lines forwarded (no dedup), reset event present.
	output := buf.String()
	require.Contains(t, output, `{"subnet_stream_reset":true}`)
	// Count "data: line1" occurrences -- should be 2 (original + replay).
	require.Equal(t, 2, bytes.Count([]byte(output), []byte("data: line1\n\n")))
	require.Contains(t, output, "data: line3")

	reg.unregister(nonce)
	// After unregister, callback is a no-op.
	before := buf.String()
	reg.callback(nonce, "data: ignored")
	require.Equal(t, before, buf.String())
}

func TestHasMsgFinish(t *testing.T) {
	require.False(t, hasMsgFinish(nil, 1))

	txs := []*types.SubnetTx{
		{Tx: &types.SubnetTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1}}},
	}
	require.False(t, hasMsgFinish(txs, 1))

	txs = append(txs, &types.SubnetTx{Tx: &types.SubnetTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 1}}})
	require.True(t, hasMsgFinish(txs, 1))
	require.False(t, hasMsgFinish(txs, 2))
}

// --- Test infrastructure for proxy-level tests ---

// killableClient wraps a HostClient. Kill/Revive toggle availability.
// KillFatal simulates a non-retryable HTTP 4xx response from the host
// (for example a 403 "sender not in group" from the auth layer).
type killableClient struct {
	inner  user.HostClient
	killed atomic.Bool
	fatal  atomic.Bool
}

func (c *killableClient) Send(ctx context.Context, req host.HostRequest) (*host.HostResponse, error) {
	if c.fatal.Load() {
		return nil, &transport.HTTPStatusError{
			StatusCode: http.StatusForbidden,
			Path:       "/sessions/escrow-proxy/chat/completions",
			Body:       "sender not in group",
		}
	}
	if c.killed.Load() {
		return nil, fmt.Errorf("host killed")
	}
	return c.inner.Send(ctx, req)
}

func (c *killableClient) Kill()      { c.killed.Store(true) }
func (c *killableClient) Revive()    { c.killed.Store(false) }
func (c *killableClient) KillFatal() { c.fatal.Store(true) }

// verifierClient wraps killableClient and implements user.TimeoutVerifier.
// This allows session.TimeoutVerifiers() to discover it.
type verifierClient struct {
	*killableClient
	accept  bool
	signer  *signing.Secp256k1Signer
	group   []types.SlotAssignment
	slotIdx int
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

func zeroTimeoutBuffer(t *testing.T) {
	t.Helper()
	saved := timeoutBuffer
	timeoutBuffer = 0
	t.Cleanup(func() { timeoutBuffer = saved })
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

	p := &Proxy{
		session:  session,
		sm:       userSM,
		escrowID: "escrow-proxy",
		model:    "llama",
		registry: newStreamRegistry(),
	}

	return &testProxyEnv{
		proxy:     p,
		session:   session,
		sm:        userSM,
		killables: killables,
		group:     group,
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
	zeroTimeoutBuffer(t)
	env := setupTestProxy(t, 3, nil, true)
	ctx := context.Background()

	var buf bytes.Buffer
	err := env.proxy.runInference(ctx, defaultParams(), &buf)
	require.NoError(t, err)

	// Inference 1 exists in state (applied locally by PrepareInference).
	st := env.sm.SnapshotState()
	_, ok := st.Inferences[1]
	require.True(t, ok, "inference 1 should exist")

	// MsgFinishInference is queued as a pending tx (applied on next diff).
	pending := env.session.PendingTxs()
	hasFinish := false
	for _, tx := range pending {
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == 1 {
			hasFinish = true
		}
	}
	require.True(t, hasFinish, "MsgFinishInference should be in pending txs")
}

func TestRunInference_RefusalTimeout(t *testing.T) {
	zeroTimeoutBuffer(t)
	env := setupTestProxy(t, 3, nil, true)
	ctx := context.Background()

	// Nonce 1 routes to host 1%3 = 1. Kill that host.
	env.killables[1].Kill()

	var buf bytes.Buffer
	err := env.proxy.runInference(ctx, defaultParams(), &buf)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timed out")
	require.Contains(t, err.Error(), "REFUSED")

	// The timeout tx should be applied: inference 1 is timed out.
	// Nonce 1 was the inference. SendPendingDiff creates nonce 2 with
	// the MsgTimeoutInference. Check the latest state.
	st := env.sm.SnapshotState()
	rec, ok := st.Inferences[1]
	require.True(t, ok, "inference 1 should exist")
	require.Equal(t, types.StatusTimedOut, rec.Status)
}

func TestRunInference_ExecutionTimeout(t *testing.T) {
	zeroTimeoutBuffer(t)

	// Executor (host 1) uses a failing engine: receipt is signed but
	// execution fails, so no MsgFinishInference enters the mempool.
	engines := make([]subnet.InferenceEngine, 3)
	engines[0] = stub.NewInferenceEngine()
	engines[1] = stub.NewFailingEngine(fmt.Errorf("engine failure"))
	engines[2] = stub.NewInferenceEngine()

	env := setupTestProxy(t, 3, engines, true)
	ctx := context.Background()

	var buf bytes.Buffer
	err := env.proxy.runInference(ctx, defaultParams(), &buf)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timed out")
	require.Contains(t, err.Error(), "EXECUTION")

	st := env.sm.SnapshotState()
	rec, ok := st.Inferences[1]
	require.True(t, ok)
	require.Equal(t, types.StatusTimedOut, rec.Status)
}

func TestRunInference_RecoveryOnRetry(t *testing.T) {
	zeroTimeoutBuffer(t)
	env := setupTestProxy(t, 3, nil, true)
	ctx := context.Background()

	// Kill executor before attempt 1.
	env.killables[1].Kill()

	// Revive after the refusal deadline passes (during the sleep).
	go func() {
		time.Sleep(500 * time.Millisecond)
		env.killables[1].Revive()
	}()

	var buf bytes.Buffer
	err := env.proxy.runInference(ctx, defaultParams(), &buf)
	require.NoError(t, err, "second attempt should succeed after host is revived")

	// MsgFinishInference should be in pending txs (not yet applied to state).
	pending := env.session.PendingTxs()
	hasFinish := false
	for _, tx := range pending {
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == 1 {
			hasFinish = true
		}
	}
	require.True(t, hasFinish, "MsgFinishInference should be in pending txs after recovery")
}

func TestHandleTimeout_InsufficientVotes(t *testing.T) {
	zeroTimeoutBuffer(t)

	// All verifiers reject: vote collection returns no accept votes.
	env := setupTestProxy(t, 3, nil, false)
	ctx := context.Background()

	env.killables[1].Kill()

	var buf bytes.Buffer
	err := env.proxy.runInference(ctx, defaultParams(), &buf)
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient votes")

	// Inference should remain pending (no timeout tx was submitted).
	st := env.sm.SnapshotState()
	rec, ok := st.Inferences[1]
	require.True(t, ok)
	require.Equal(t, types.StatusPending, rec.Status)
}

// TestRunInference_FatalHTTPErrorReturnsImmediately reproduces the behavior
// described in #1019: when the host rejects an inference with a fatal 4xx
// status (for example 403 "sender not in group" from the auth layer), the
// proxy must surface that error immediately instead of blocking on
// RefusalTimeout / ExecutionTimeout and then falling into timeout-vote
// collection.
//
// Before the fix, sendAndProcess swallowed SendOnly errors whenever the
// response was nil, so runInference waited through two deadline cycles and
// eventually returned a misleading "insufficient timeout votes" / "timed
// out" error. After the fix, 4xx statuses are propagated so the caller can
// return 4xx/502 to the OpenAI client without the minute-long wait.
func TestRunInference_FatalHTTPErrorReturnsImmediately(t *testing.T) {
	zeroTimeoutBuffer(t)
	env := setupTestProxy(t, 3, nil, true)
	ctx := context.Background()

	// Nonce 1 routes to host 1 % 3 = 1. Put that host in fatal mode so
	// Send returns *transport.HTTPStatusError{StatusCode: 403}.
	env.killables[1].KillFatal()

	start := time.Now()
	var buf bytes.Buffer
	err := env.proxy.runInference(ctx, defaultParams(), &buf)
	elapsed := time.Since(start)

	// The fatal HTTP error must be surfaced — not converted into a timeout.
	require.Error(t, err)
	require.Contains(t, err.Error(), "host rejected inference",
		"fatal HTTP errors must be propagated with a clear cause")
	require.Contains(t, err.Error(), "403",
		"status code must be preserved in the error chain")
	require.NotContains(t, err.Error(), "timed out",
		"fatal errors must not be reported as timeouts")
	require.NotContains(t, err.Error(), "insufficient votes",
		"fatal errors must not reach the timeout vote path")

	// The cause must also be inspectable via errors.As for future callers
	// that want to map status codes to HTTP responses.
	require.True(t, transport.IsFatalHTTPError(err),
		"error must unwrap to *transport.HTTPStatusError with a fatal status")

	// Duration sanity: with zeroTimeoutBuffer the RefusalTimeout is 1s and
	// ExecutionTimeout is 1s, so the old swallow-then-wait behavior burns
	// at least ~2s before returning. A correct implementation returns in
	// the low-millisecond range.
	require.Less(t, elapsed, 500*time.Millisecond,
		"fatal HTTP errors must fail fast, not block on refusal/execution timeouts")

	// The inference must NOT be marked as timed out — the timeout machinery
	// was correctly bypassed.
	st := env.sm.SnapshotState()
	rec, ok := st.Inferences[1]
	require.True(t, ok, "inference 1 should exist from PrepareInference")
	require.Equal(t, types.StatusPending, rec.Status,
		"inference must remain pending — fatal errors must not trigger timeout state transitions")
}

