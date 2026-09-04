package user

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"common/completionapi"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func errorMissPayload(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(completionapi.SerializedStreamedResponse{Events: []string{
		`data: {"error":{"code":500,"message":"EngineCore encountered an issue. See stack trace (above) for the root cause.","param":null,"type":"InternalServerError"},"id":"devshard-57577-89"}`,
		`data: [DONE]`,
	}})
	require.NoError(t, err)
	return b
}

func startPendingConfirmForErrorMiss(t *testing.T, session *Session, hosts []*signing.Secp256k1Signer, params InferenceParams) (nonce uint64, execIdx int) {
	t.Helper()
	prepared, err := session.PrepareInference(params)
	require.NoError(t, err)
	nonce = prepared.diff.Nonce
	execIdx = int(nonce % uint64(len(session.clients)))

	execSig := testutil.SignExecutorReceipt(t, hosts[execIdx], "escrow-1", nonce, testutil.TestPromptHash[:], "llama", 100, testutil.TestMaxTokens, 1000, 1000)
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: nonce, ExecutorSig: execSig, ConfirmedAt: 1000,
	}}}
	session.mu.Lock()
	session.addPendingTx(confirm)
	session.nonceStates[nonce].confirmedAt = 1000
	session.mu.Unlock()
	require.Equal(t, types.StatusPending, session.StateMachine().SnapshotState().Inferences[nonce].Status)
	return nonce, execIdx
}

func startConfirmForErrorMiss(t *testing.T, session *Session, hosts []*signing.Secp256k1Signer, params InferenceParams) (nonce uint64, execIdx int) {
	t.Helper()
	nonce, execIdx = startPendingConfirmForErrorMiss(t, session, hosts, params)
	require.NoError(t, session.SendPendingDiff(context.Background()))
	require.Equal(t, types.StatusStarted, session.StateMachine().SnapshotState().Inferences[nonce].Status)
	return nonce, execIdx
}

// applyingErrorMissClient applies catch-up diffs then runs the real
// VerifyErrorMiss checks (same sequence as HandleVerifyErrorMiss).
type applyingErrorMissClient struct {
	HostClient
	host    *host.Host
	signer  *signing.Secp256k1Signer
	group   []types.SlotAssignment
	slotIdx int
}

func (c *applyingErrorMissClient) VerifyTimeout(context.Context, uint64, types.TimeoutReason, *host.InferencePayload, []types.Diff, host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	return false, nil, 0, nil, "unused", nil
}

func (c *applyingErrorMissClient) VerifyErrorMiss(_ context.Context, inferenceID uint64, diffs []types.Diff, artifacts host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	if len(diffs) > 0 {
		c.host.ApplyCatchUpDiffs(diffs)
	}
	st := c.host.SnapshotState()
	accept, responseHash, rejectCause, err := host.VerifyErrorMiss(st, inferenceID, artifacts.FinishTx, artifacts.ResponsePayload, c.host.MempoolTxs(), c.host)
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	if !accept {
		return false, nil, 0, host.RecoveryTxsFor(c.host.MempoolTxs(), inferenceID), rejectCause, nil
	}
	content := &types.ErrorMissVoteContent{
		EscrowId:     "escrow-1",
		InferenceId:  inferenceID,
		Accept:       true,
		ResponseHash: responseHash,
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(content)
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	sig, err := c.signer.Sign(data)
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	return true, sig, c.group[c.slotIdx].SlotID, nil, "", nil
}

func wrapApplyingErrorMissVerifiers(session *Session, hosts []*signing.Secp256k1Signer) {
	for i, c := range session.clients {
		session.clients[i] = &applyingErrorMissClient{
			HostClient: c,
			host:       c.(*InProcessClient).Host,
			signer:     hosts[i],
			group:      session.group,
			slotIdx:    i,
		}
	}
}

func signedErrorFinishTx(t *testing.T, hosts []*signing.Secp256k1Signer, nonce uint64, execIdx int, payload []byte) (*types.DevshardTx, []byte) {
	t.Helper()
	sum := sha256.Sum256(payload)
	hash := sum[:]
	msg := &types.MsgFinishInference{
		InferenceId:  nonce,
		ResponseHash: hash,
		InputTokens:  0,
		OutputTokens: 0,
		ExecutorSlot: uint32(execIdx),
		EscrowId:     "escrow-1",
	}
	msg.ProposerSig = testutil.SignProposerTx(t, hosts[execIdx], msg)
	tx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
	return tx, MarshalFinishTx([]*types.DevshardTx{tx}, nonce)
}

func wrapAcceptingTimeoutVerifiers(session *Session, signers []*signing.Secp256k1Signer) {
	for i, c := range session.clients {
		session.clients[i] = &timeoutVoteClient{
			HostClient: c,
			mockTimeoutVerifier: &mockTimeoutVerifier{
				accept:  true,
				signer:  signers[i],
				group:   session.group,
				slotIdx: i,
			},
		}
	}
}

func TestHandleErrorMiss_SameDiffFinishAndMiss(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	nonce, execIdx := startConfirmForErrorMiss(t, session, hosts, params)
	payload := errorMissPayload(t)
	finishTx, finishBytes := signedErrorFinishTx(t, hosts, nonce, execIdx, payload)

	session.mu.Lock()
	session.addPendingTx(finishTx)
	session.nonceStates[nonce].finished = true
	session.mu.Unlock()
	require.True(t, session.IsNonceFinished(nonce))

	wrapAcceptingTimeoutVerifiers(session, hosts)

	before := len(session.Diffs())
	start := time.Now()
	result, err := session.HandleErrorMiss(context.Background(), nonce, finishBytes, payload)
	elapsed := time.Since(start)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInferenceMissed)
	require.Contains(t, err.Error(), "timed out")
	require.Equal(t, "error", result.Reason)
	require.True(t, result.Accepted)
	require.Positive(t, result.Votes)
	require.NotEmpty(t, result.ResponseHash)
	require.Less(t, elapsed, time.Second, "error-miss must not wait on ExecutionTimeout")
	require.Greater(t, session.StateMachine().SnapshotState().Config.ExecutionTimeout, int64(60))

	st := session.StateMachine().SnapshotState()
	rec := st.Inferences[nonce]
	require.Equal(t, types.StatusTimedOut, rec.Status)
	require.Equal(t, uint32(1), st.HostStats[uint32(execIdx)].Missed)
	require.False(t, session.IsNonceFinished(nonce), "local finished view must match chain after error-miss")

	diffs := session.Diffs()
	require.Len(t, diffs, before+1)
	txs := diffs[len(diffs)-1].Txs
	require.GreaterOrEqual(t, len(txs), 2)
	require.NotNil(t, txs[0].GetFinishInference(), "same-diff composition requires Finish first")
	require.NotNil(t, txs[1].GetErrorMiss(), "same-diff composition requires ErrorMiss after Finish")
}

func TestHandleErrorMiss_PublishesConfirmStartBeforeVotes(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	nonce, execIdx := startPendingConfirmForErrorMiss(t, session, hosts, params)
	payload := errorMissPayload(t)
	finishTx, finishBytes := signedErrorFinishTx(t, hosts, nonce, execIdx, payload)

	session.mu.Lock()
	session.addPendingTx(finishTx)
	session.nonceStates[nonce].finished = true
	session.mu.Unlock()

	wrapApplyingErrorMissVerifiers(session, hosts)

	result, err := session.HandleErrorMiss(context.Background(), nonce, finishBytes, payload)
	require.ErrorIs(t, err, ErrInferenceMissed)
	require.True(t, result.Accepted)
	require.Equal(t, types.StatusTimedOut, session.StateMachine().SnapshotState().Inferences[nonce].Status)

	var sawConfirmWithoutFinish, sawFinishWithMiss bool
	for _, diff := range session.Diffs() {
		var hasConfirm, hasFinish, hasMiss bool
		for _, tx := range diff.Txs {
			if cs := tx.GetConfirmStart(); cs != nil && cs.GetInferenceId() == nonce {
				hasConfirm = true
			}
			if fi := tx.GetFinishInference(); fi != nil && fi.GetInferenceId() == nonce {
				hasFinish = true
			}
			if em := tx.GetErrorMiss(); em != nil && em.GetInferenceId() == nonce {
				hasMiss = true
			}
		}
		if hasConfirm {
			require.False(t, hasFinish, "receipt publish must hold the pinned Finish")
			sawConfirmWithoutFinish = true
		}
		if hasFinish && hasMiss {
			sawFinishWithMiss = true
		}
	}
	require.True(t, sawConfirmWithoutFinish, "ConfirmStart must be committed before error-miss votes")
	require.True(t, sawFinishWithMiss, "Finish and ErrorMiss must still share a later diff")
}

func TestPinPendingFinish_ConcurrentComposeLeavesFinish(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	nonce, execIdx := startConfirmForErrorMiss(t, session, hosts, params)
	payload := errorMissPayload(t)
	finishTx, _ := signedErrorFinishTx(t, hosts, nonce, execIdx, payload)
	session.mu.Lock()
	session.addPendingTx(finishTx)
	session.mu.Unlock()

	session.pinPendingFinish(nonce)
	defer session.unpinPendingFinish(nonce)

	require.NoError(t, session.SendPendingDiff(context.Background()))
	require.NotNil(t, findRecoveryFinish(session.PendingTxs(), nonce), "pinned Finish must survive an unrelated SendPendingDiff")

	next := params
	next.Prompt = bytes.ReplaceAll(testutil.TestPrompt, []byte("xxxxxxxxxxxxxxxxxxxxxxxxx"), []byte("other concurrent prompt xx"))
	_, err := session.PrepareInference(next)
	require.NoError(t, err)
	require.NotNil(t, findRecoveryFinish(session.PendingTxs(), nonce), "pinned Finish must survive PrepareInference")
}

func TestPinnedNonce_HoldsTimeoutUntilIncluded(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	nonce, execIdx := startConfirmForErrorMiss(t, session, hosts, params)
	payload := errorMissPayload(t)
	finishTx, _ := signedErrorFinishTx(t, hosts, nonce, execIdx, payload)
	session.mu.Lock()
	session.addPendingTx(finishTx)
	session.addPendingTx(errorMissTx(nonce, nil))
	session.mu.Unlock()

	session.pinPendingFinish(nonce)
	defer session.unpinPendingFinish(nonce)

	require.NoError(t, session.SendPendingDiff(context.Background()))
	require.NotNil(t, findRecoveryFinish(session.PendingTxs(), nonce), "pinned Finish must survive an unrelated SendPendingDiff")
	require.NotNil(t, findPendingErrorMiss(session.PendingTxs(), nonce), "pinned ErrorMiss must not be stolen without Finish")

	next := params
	next.Prompt = bytes.ReplaceAll(testutil.TestPrompt, []byte("xxxxxxxxxxxxxxxxxxxxxxxxx"), []byte("other concurrent prompt xx"))
	_, err := session.PrepareInference(next)
	require.NoError(t, err)
	require.NotNil(t, findRecoveryFinish(session.PendingTxs(), nonce))
	require.NotNil(t, findPendingErrorMiss(session.PendingTxs(), nonce), "pinned ErrorMiss must survive PrepareInference")
}

func TestHandleErrorMiss_PinnedFinishSurvivesConcurrentCompose(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	nonce, execIdx := startConfirmForErrorMiss(t, session, hosts, params)
	payload := errorMissPayload(t)
	finishTx, finishBytes := signedErrorFinishTx(t, hosts, nonce, execIdx, payload)
	session.mu.Lock()
	session.addPendingTx(finishTx)
	session.nonceStates[nonce].finished = true
	session.mu.Unlock()

	started := make(chan struct{})
	composed := make(chan struct{})
	var once sync.Once
	for i, c := range session.clients {
		session.clients[i] = &timeoutVoteClient{
			HostClient: c,
			mockTimeoutVerifier: &mockTimeoutVerifier{
				accept:  true,
				signer:  hosts[i],
				group:   session.group,
				slotIdx: i,
				onVerify: func() {
					once.Do(func() {
						close(started)
						<-composed
					})
				},
			},
		}
	}

	composeErr := make(chan error, 1)
	go func() {
		<-started
		composeErr <- session.SendPendingDiff(context.Background())
		close(composed)
	}()

	_, err := session.HandleErrorMiss(context.Background(), nonce, finishBytes, payload)
	require.NoError(t, <-composeErr)
	require.ErrorIs(t, err, ErrInferenceMissed)
	require.Equal(t, types.StatusTimedOut, session.StateMachine().SnapshotState().Inferences[nonce].Status)

	var sawFinishWithErrorMiss bool
	for _, diff := range session.Diffs() {
		var hasFinish, hasErrorMiss bool
		for _, tx := range diff.Txs {
			if fi := tx.GetFinishInference(); fi != nil && fi.GetInferenceId() == nonce {
				hasFinish = true
			}
			if em := tx.GetErrorMiss(); em != nil && em.GetInferenceId() == nonce {
				hasErrorMiss = true
			}
		}
		if hasFinish && hasErrorMiss {
			sawFinishWithErrorMiss = true
		}
		if hasFinish && !hasErrorMiss {
			t.Fatalf("Finish for nonce %d published without ErrorMiss in the same diff", nonce)
		}
	}
	require.True(t, sawFinishWithErrorMiss, "Finish and ErrorMiss must share a diff after concurrent compose")
}

func TestHandleErrorMiss_InsufficientVotesKeepsToday(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	nonce, execIdx := startConfirmForErrorMiss(t, session, hosts, params)
	payload := errorMissPayload(t)
	finishTx, finishBytes := signedErrorFinishTx(t, hosts, nonce, execIdx, payload)
	session.mu.Lock()
	session.addPendingTx(finishTx)
	session.nonceStates[nonce].finished = true
	session.mu.Unlock()

	for i, c := range session.clients {
		session.clients[i] = &timeoutVoteClient{
			HostClient:          c,
			mockTimeoutVerifier: &mockTimeoutVerifier{accept: false, slotIdx: i, group: session.group},
		}
	}

	before := len(session.Diffs())
	result, err := session.HandleErrorMiss(context.Background(), nonce, finishBytes, payload)
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient votes")
	require.NotErrorIs(t, err, ErrInferenceMissed)
	require.Equal(t, "error", result.Reason)
	require.False(t, result.Accepted)
	require.Len(t, session.Diffs(), before, "insufficient votes must not emit an error-miss diff")
	require.Equal(t, types.StatusStarted, session.StateMachine().SnapshotState().Inferences[nonce].Status)
	require.NotNil(t, findRecoveryFinish(session.PendingTxs(), nonce), "Finish stays pending for today's publish path")
}

func TestHandleErrorMiss_DoesNotEarlyReturnOnPendingFinish(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	nonce, execIdx := startConfirmForErrorMiss(t, session, hosts, params)
	payload := errorMissPayload(t)
	finishTx, finishBytes := signedErrorFinishTx(t, hosts, nonce, execIdx, payload)
	session.mu.Lock()
	session.addPendingTx(finishTx)
	session.nonceStates[nonce].confirmedAt = 1000
	session.mu.Unlock()
	require.NotNil(t, findRecoveryFinish(session.PendingTxs(), nonce))

	wrapAcceptingTimeoutVerifiers(session, hosts)
	_, err := session.HandleErrorMiss(context.Background(), nonce, finishBytes, payload)
	require.ErrorIs(t, err, ErrInferenceMissed)
	require.Equal(t, types.StatusTimedOut, session.StateMachine().SnapshotState().Inferences[nonce].Status)
}

func TestHandleErrorMiss_NotAppliedIfFinishMissing(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	nonce, execIdx := startConfirmForErrorMiss(t, session, hosts, params)
	payload := errorMissPayload(t)
	_, finishBytes := signedErrorFinishTx(t, hosts, nonce, execIdx, payload)

	session.mu.Lock()
	session.nonceStates[nonce].finished = true
	session.mu.Unlock()
	require.True(t, session.IsNonceFinished(nonce))
	require.Nil(t, findRecoveryFinish(session.PendingTxs(), nonce))

	wrapAcceptingTimeoutVerifiers(session, hosts)
	before := len(session.Diffs())
	result, err := session.HandleErrorMiss(context.Background(), nonce, finishBytes, payload)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not applied")
	require.NotErrorIs(t, err, ErrInferenceMissed)
	require.Equal(t, "error", result.Reason)
	require.Equal(t, types.StatusStarted, session.StateMachine().SnapshotState().Inferences[nonce].Status)
	require.True(t, session.IsNonceFinished(nonce), "must not clear finished unless the miss landed")
	require.GreaterOrEqual(t, len(session.Diffs()), before)
	require.False(t, result.Accepted, "votes without an applied miss must not report accepted")
}

func TestProcessResponse_TimedOutNonceDoesNotRefinish(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	nonce, execIdx := startConfirmForErrorMiss(t, session, hosts, params)
	payload := errorMissPayload(t)
	finishTx, finishBytes := signedErrorFinishTx(t, hosts, nonce, execIdx, payload)
	session.mu.Lock()
	session.addPendingTx(finishTx)
	session.nonceStates[nonce].finished = true
	session.mu.Unlock()
	wrapAcceptingTimeoutVerifiers(session, hosts)
	_, err := session.HandleErrorMiss(context.Background(), nonce, finishBytes, payload)
	require.ErrorIs(t, err, ErrInferenceMissed)
	require.Equal(t, types.StatusTimedOut, session.StateMachine().SnapshotState().Inferences[nonce].Status)
	require.False(t, session.IsNonceFinished(nonce))

	require.NoError(t, session.ProcessResponse(execIdx, &host.HostResponse{
		Mempool: []*types.DevshardTx{finishTx},
	}, nonce))
	require.False(t, session.IsNonceFinished(nonce), "Finish still in mempool must not re-mark a timed-out nonce finished")
}

func TestMarshalFinishTx(t *testing.T) {
	require.Nil(t, MarshalFinishTx(nil, 1))
	tx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 7}}}
	got := MarshalFinishTx([]*types.DevshardTx{nil, tx}, 7)
	require.NotEmpty(t, got)
	decoded := &types.DevshardTx{}
	require.NoError(t, proto.Unmarshal(got, decoded))
	require.Equal(t, uint64(7), decoded.GetFinishInference().GetInferenceId())
	require.Nil(t, MarshalFinishTx([]*types.DevshardTx{tx}, 1))
}

func TestFinishTxFor_MarshalsUnderLock(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	tx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 7}}}
	session.mu.Lock()
	session.addPendingTx(tx)
	session.mu.Unlock()
	got := session.FinishTxFor(7)
	require.NotEmpty(t, got)
	require.Nil(t, session.FinishTxFor(1))
}

func TestFinishTxFor_ConcurrentWithSendPendingDiff(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()
	const n = 8
	for i := range n {
		session.mu.Lock()
		session.addPendingTx(&types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: uint64(200 + i)}}})
		session.mu.Unlock()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 200 {
			_ = session.FinishTxFor(uint64(200 + i%n))
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 40 {
			session.mu.Lock()
			session.addPendingTx(&types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: uint64(300 + i)}}})
			session.mu.Unlock()
			_ = session.SendPendingDiff(ctx)
		}
	}()
	wg.Wait()
}

func TestMergeTimeoutCatchUpDiffs(t *testing.T) {
	d1 := types.Diff{Nonce: 1}
	d2 := types.Diff{Nonce: 2}
	d3 := types.Diff{Nonce: 3}
	require.Equal(t, []types.Diff{d2, d3}, mergeTimeoutCatchUpDiffs([]types.Diff{d2, d3}, nil))
	require.Equal(t, []types.Diff{d1, d2, d3}, mergeTimeoutCatchUpDiffs(nil, []types.Diff{d1, d2, d3}))
	got := mergeTimeoutCatchUpDiffs([]types.Diff{d2, d3}, []types.Diff{d1, d2, d3})
	require.Equal(t, []types.Diff{d2, d3, d1}, got, "per-host catch-up first, unpublished extras after")
}

func TestTimeoutReasonLogLabel(t *testing.T) {
	require.Equal(t, "execution", timeoutReasonLogLabel(types.TimeoutReason_TIMEOUT_REASON_EXECUTION))
	require.Equal(t, "refused", timeoutReasonLogLabel(types.TimeoutReason_TIMEOUT_REASON_REFUSED))
	require.Equal(t, "unknown", timeoutReasonLogLabel(types.TimeoutReason(99)))
}
