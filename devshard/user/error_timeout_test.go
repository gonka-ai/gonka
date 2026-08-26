package user

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"common/completionapi"

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

func startConfirmForErrorMiss(t *testing.T, session *Session, hosts []*signing.Secp256k1Signer, params InferenceParams) (nonce uint64, execIdx int) {
	t.Helper()
	ctx := context.Background()
	prepared, err := session.PrepareInference(params)
	require.NoError(t, err)
	nonce = prepared.diff.Nonce
	execIdx = int(nonce % uint64(len(session.clients)))

	execSig := testutil.SignExecutorReceipt(t, hosts[execIdx], "escrow-1", nonce, testutil.TestPromptHash[:], "llama", 100, 50, 1000, 1000)
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: nonce, ExecutorSig: execSig, ConfirmedAt: 1000,
	}}}
	session.mu.Lock()
	session.addPendingTx(confirm)
	session.mu.Unlock()
	require.NoError(t, session.SendPendingDiff(ctx))
	require.Equal(t, types.StatusStarted, session.StateMachine().SnapshotState().Inferences[nonce].Status)

	session.mu.Lock()
	session.nonceStates[nonce].confirmedAt = 1000
	session.mu.Unlock()
	return nonce, execIdx
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

func TestHandleTimeout_Error_SameDiffFinishAndMiss(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
	result, err := session.HandleTimeout(context.Background(), nonce, time.Now(), nil, TimeoutOpts{
		Reason:          types.TimeoutReason_TIMEOUT_REASON_ERROR,
		FinishTx:        finishBytes,
		ResponsePayload: payload,
	})
	elapsed := time.Since(start)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInferenceMissed)
	require.Contains(t, err.Error(), "timed out")
	require.Equal(t, "error", result.Reason)
	require.True(t, result.Accepted)
	require.Greater(t, result.Votes, 0)
	require.NotEmpty(t, result.ResponseHash)
	require.Less(t, elapsed, time.Second, "ERROR timeout must not wait on ExecutionTimeout")
	require.Greater(t, session.StateMachine().SnapshotState().Config.ExecutionTimeout, int64(60))

	st := session.StateMachine().SnapshotState()
	rec := st.Inferences[nonce]
	require.Equal(t, types.StatusTimedOut, rec.Status)
	require.Equal(t, uint32(1), st.HostStats[uint32(execIdx)].Missed)
	require.False(t, session.IsNonceFinished(nonce), "local finished view must match chain after ERROR miss")

	diffs := session.Diffs()
	require.Equal(t, before+1, len(diffs))
	txs := diffs[len(diffs)-1].Txs
	require.GreaterOrEqual(t, len(txs), 2)
	require.NotNil(t, txs[0].GetFinishInference(), "same-diff composition requires Finish first")
	require.NotNil(t, txs[1].GetTimeoutInference(), "same-diff composition requires Timeout after Finish")
	require.Equal(t, types.TimeoutReason_TIMEOUT_REASON_ERROR, txs[1].GetTimeoutInference().Reason)
}

func TestPinPendingFinish_ConcurrentComposeLeavesFinish(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}
	nonce, execIdx := startConfirmForErrorMiss(t, session, hosts, params)
	payload := errorMissPayload(t)
	finishTx, _ := signedErrorFinishTx(t, hosts, nonce, execIdx, payload)
	session.mu.Lock()
	session.addPendingTx(finishTx)
	session.addPendingTx(timeoutInferenceTx(nonce, types.TimeoutReason_TIMEOUT_REASON_ERROR, nil))
	session.mu.Unlock()

	session.pinPendingFinish(nonce)
	defer session.unpinPendingFinish(nonce)

	require.NoError(t, session.SendPendingDiff(context.Background()))
	require.NotNil(t, findRecoveryFinish(session.PendingTxs(), nonce), "pinned Finish must survive an unrelated SendPendingDiff")
	require.NotNil(t, findPendingTimeout(session.PendingTxs(), nonce), "pinned Timeout must not be stolen without Finish")

	next := params
	next.Prompt = bytes.ReplaceAll(testutil.TestPrompt, []byte("xxxxxxxxxxxxxxxxxxxxxxxxx"), []byte("other concurrent prompt xx"))
	_, err := session.PrepareInference(next)
	require.NoError(t, err)
	require.NotNil(t, findRecoveryFinish(session.PendingTxs(), nonce))
	require.NotNil(t, findPendingTimeout(session.PendingTxs(), nonce), "pinned Timeout must survive PrepareInference")
}

func TestHandleTimeout_Error_PinnedFinishSurvivesConcurrentCompose(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}
	nonce, execIdx := startConfirmForErrorMiss(t, session, hosts, params)
	payload := errorMissPayload(t)
	finishTx, finishBytes := signedErrorFinishTx(t, hosts, nonce, execIdx, payload)
	session.mu.Lock()
	session.addPendingTx(finishTx)
	session.nonceStates[nonce].finished = true
	session.mu.Unlock()

	started := make(chan struct{})
	var once sync.Once
	for i, c := range session.clients {
		session.clients[i] = &timeoutVoteClient{
			HostClient: c,
			mockTimeoutVerifier: &mockTimeoutVerifier{
				accept:   true,
				signer:   hosts[i],
				group:    session.group,
				slotIdx:  i,
				delay:    80 * time.Millisecond,
				onVerify: func() { once.Do(func() { close(started) }) },
			},
		}
	}

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-started
		for {
			select {
			case <-done:
				return
			default:
				_ = session.SendPendingDiff(context.Background())
			}
		}
	}()

	_, err := session.HandleTimeout(context.Background(), nonce, time.Now(), nil, TimeoutOpts{
		Reason:          types.TimeoutReason_TIMEOUT_REASON_ERROR,
		FinishTx:        finishBytes,
		ResponsePayload: payload,
	})
	close(done)
	wg.Wait()
	require.ErrorIs(t, err, ErrInferenceMissed)
	require.Equal(t, types.StatusTimedOut, session.StateMachine().SnapshotState().Inferences[nonce].Status)

	var sawFinishWithTimeout bool
	for _, diff := range session.Diffs() {
		var hasFinish, hasTimeout bool
		for _, tx := range diff.Txs {
			if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == nonce {
				hasFinish = true
			}
			if to := tx.GetTimeoutInference(); to != nil && to.InferenceId == nonce {
				hasTimeout = true
			}
		}
		if hasFinish && hasTimeout {
			sawFinishWithTimeout = true
		}
		if hasFinish && !hasTimeout {
			t.Fatalf("Finish for nonce %d published without Timeout in the same diff", nonce)
		}
	}
	require.True(t, sawFinishWithTimeout, "Finish and ERROR Timeout must share a diff after concurrent compose")
}

func TestHandleTimeout_Error_InsufficientVotesKeepsToday(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
	result, err := session.HandleTimeout(context.Background(), nonce, time.Now(), nil, TimeoutOpts{
		Reason:          types.TimeoutReason_TIMEOUT_REASON_ERROR,
		FinishTx:        finishBytes,
		ResponsePayload: payload,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient votes")
	require.False(t, errors.Is(err, ErrInferenceMissed))
	require.Equal(t, "error", result.Reason)
	require.False(t, result.Accepted)
	require.Len(t, session.Diffs(), before, "insufficient votes must not emit a timeout diff")
	require.Equal(t, types.StatusStarted, session.StateMachine().SnapshotState().Inferences[nonce].Status)
	require.NotNil(t, findRecoveryFinish(session.PendingTxs(), nonce), "Finish stays pending for today's publish path")
}

func TestHandleTimeout_Error_DoesNotEarlyReturnOnPendingFinish(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
	_, err := session.HandleTimeout(context.Background(), nonce, time.Now(), nil, TimeoutOpts{
		Reason:          types.TimeoutReason_TIMEOUT_REASON_ERROR,
		FinishTx:        finishBytes,
		ResponsePayload: payload,
	})
	require.ErrorIs(t, err, ErrInferenceMissed)
	require.Equal(t, types.StatusTimedOut, session.StateMachine().SnapshotState().Inferences[nonce].Status)
}

func TestHandleTimeout_Error_NotAppliedIfFinishMissing(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
	result, err := session.HandleTimeout(context.Background(), nonce, time.Now(), nil, TimeoutOpts{
		Reason:          types.TimeoutReason_TIMEOUT_REASON_ERROR,
		FinishTx:        finishBytes,
		ResponsePayload: payload,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not applied")
	require.False(t, errors.Is(err, ErrInferenceMissed))
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}
	nonce, execIdx := startConfirmForErrorMiss(t, session, hosts, params)
	payload := errorMissPayload(t)
	finishTx, finishBytes := signedErrorFinishTx(t, hosts, nonce, execIdx, payload)
	session.mu.Lock()
	session.addPendingTx(finishTx)
	session.nonceStates[nonce].finished = true
	session.mu.Unlock()
	wrapAcceptingTimeoutVerifiers(session, hosts)
	_, err := session.HandleTimeout(context.Background(), nonce, time.Now(), nil, TimeoutOpts{
		Reason:          types.TimeoutReason_TIMEOUT_REASON_ERROR,
		FinishTx:        finishBytes,
		ResponsePayload: payload,
	})
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
	require.Equal(t, uint64(7), decoded.GetFinishInference().InferenceId)
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
	for i := 0; i < n; i++ {
		session.mu.Lock()
		session.addPendingTx(&types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: uint64(200 + i)}}})
		session.mu.Unlock()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = session.FinishTxFor(uint64(200 + i%n))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
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
	require.Equal(t, "error", timeoutReasonLogLabel(types.TimeoutReason_TIMEOUT_REASON_ERROR))
	require.Equal(t, "unknown", timeoutReasonLogLabel(types.TimeoutReason(99)))
}
