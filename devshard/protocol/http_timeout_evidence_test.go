package protocol

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/transport"
	"devshard/types"
)

// httpEvidenceWirings runs a timeout test against both host wirings. Timeout
// evidence must verify the same way whether or not the host carries its own
// signature verifier; devshardd never sets one.
var httpEvidenceWirings = []struct {
	name         string
	hostVerifier bool
}{
	{name: "devshardd wiring", hostVerifier: false},
	{name: "host verifier", hostVerifier: true},
}

// forgingExecutor answers every ChallengeReceipt with a ConfirmStart that
// carries no executor signature, plus a Finish. By default the Finish is
// signed by another group member. signAsExecutor signs it with the executor.
type forgingExecutor struct {
	challenges     atomic.Int32
	signAsExecutor bool
}

func (f *forgingExecutor) serve(t *testing.T, env *httpTestEnv, executorIdx int) *transport.HTTPClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req transport.ChallengeReceiptRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.challenges.Add(1)
		forged := []byte("forged-receipt")
		signerIdx := (executorIdx + 1) % len(env.signers)
		if f.signAsExecutor {
			signerIdx = executorIdx
		}
		finish := &types.MsgFinishInference{
			InferenceId:  req.InferenceID,
			EscrowId:     "escrow-1",
			ExecutorSlot: env.group[executorIdx].SlotID,
			ResponseHash: testutil.TestResponseHash,
			ServedHash:   testutil.TestServedHash,
		}
		finish.ProposerSig = testutil.SignProposerTx(t, env.signers[signerIdx], finish)
		mempool, err := transport.DevshardTxsToBytes([]*types.DevshardTx{
			{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
				InferenceId: req.InferenceID, ExecutorSig: forged, ConfirmedAt: time.Now().Unix(),
			}}},
			{Tx: &types.DevshardTx_FinishInference{FinishInference: finish}},
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(transport.ChallengeReceiptResponse{Receipt: forged, Mempool: mempool})
	}))
	t.Cleanup(srv.Close)
	return httpTestClient(srv.URL, "escrow-1", env.userSigner)
}

// routeExecutorTo points every host's timeout challenge for executorIdx at c.
func routeExecutorTo(env *httpTestEnv, executorIdx int, c *transport.HTTPClient) {
	for _, srv := range env.servers {
		peers := make(map[int]*transport.HTTPClient, len(env.clients))
		for i, pc := range env.clients {
			peers[i] = pc
		}
		peers[executorIdx] = c
		srv.SetPeerClients(transport.HTTPPeerClients(peers))
	}
}

func requireNoEvidenceCopied(t *testing.T, env *httpTestEnv, executorIdx int, inferenceID uint64) {
	t.Helper()
	for i, h := range env.hosts {
		if i == executorIdx {
			continue
		}
		require.Empty(t, host.RecoveryTxsFor(h.MempoolTxs(), inferenceID), "verifier %d copied unverified evidence", i)
	}
}

// requireFinishInVerifierMempools checks that every non-executor host copied
// the same MsgFinishInference into its mempool and left ConfirmStart out.
// An execution challenge copies only the finish the reject reply then returns.
func requireFinishInVerifierMempools(t *testing.T, env *httpTestEnv, executorIdx int, inferenceID uint64) *types.DevshardTx {
	t.Helper()
	var want *types.DevshardTx
	copied := 0
	for i, h := range env.hosts {
		if i == executorIdx {
			continue
		}
		finish := findFinish(h.MempoolTxs(), inferenceID)
		require.NotNil(t, finish, "verifier %d mempool must hold the executor finish", i)
		require.Nil(t, findConfirmStart(h.MempoolTxs(), inferenceID), "verifier %d must not copy ConfirmStart from an execution challenge", i)
		if want == nil {
			want = finish
		} else {
			require.Equal(t, types.TxHash(want), types.TxHash(finish), "verifier %d copied a different finish", i)
		}
		copied++
	}
	require.Positive(t, copied, "at least one verifier must have been asked")
	return want
}

func TestHTTP_RefusedTimeout_HonestReceiptRecovers(t *testing.T) {
	for _, wiring := range httpEvidenceWirings {
		t.Run(wiring.name, func(t *testing.T) {
			env := setupHTTPEnvWiring(t, 5, 1000000, 100, wiring.hostVerifier)
			ctx := context.Background()

			prepared, err := env.session.PrepareInference(defaultParams())
			require.NoError(t, err)
			executorIdx := prepared.HostIdx()
			diffs := env.session.Diffs()
			_, err = env.hosts[executorIdx].HandleRequest(ctx, host.HostRequest{Diffs: diffs, Nonce: diffs[len(diffs)-1].Nonce})
			require.NoError(t, err)

			result, err := env.session.HandleTimeout(ctx, prepared.Nonce(), time.Unix(0, 0), refusedPayload())
			require.NoError(t, err, "an executor that signs a receipt when challenged is not refusing")
			require.Zero(t, result.Votes)
			rec := env.session.StateMachine().SnapshotState().Inferences[prepared.Nonce()]
			require.Contains(t, []types.InferenceStatus{types.StatusStarted, types.StatusFinished}, rec.Status,
				"the verified receipt lands as recovery, and the challenged run may already have finished")
			require.Zero(t, env.session.StateMachine().SnapshotState().HostStats[rec.ExecutorSlot].Missed)
		})
	}
}

func TestHTTP_ExecutionTimeout_HonestFinishRejects(t *testing.T) {
	for _, wiring := range httpEvidenceWirings {
		t.Run(wiring.name, func(t *testing.T) {
			config := testutil.DefaultConfig(3)
			config.ExecutionTimeout = 0
			env := setupHTTPEnvWiring(t, 3, 1000000, 100, wiring.hostVerifier, config)
			ctx := context.Background()

			prepared, err := env.session.PrepareInference(defaultParams())
			require.NoError(t, err)
			executorIdx := prepared.HostIdx()
			receipt, _, err := env.clients[executorIdx].ChallengeReceipt(ctx, prepared.Nonce(), refusedPayload(), env.session.Diffs())
			require.NoError(t, err)
			confirmTx := findConfirmStart(env.hosts[executorIdx].MempoolTxs(), prepared.Nonce())
			require.NotNil(t, confirmTx)
			require.NoError(t, env.session.ProcessResponse(executorIdx, &host.HostResponse{
				Receipt:     receipt,
				ConfirmedAt: confirmTx.GetConfirmStart().ConfirmedAt,
			}, prepared.Nonce()))
			require.NoError(t, env.session.SendPendingDiff(ctx))
			require.Eventually(t, func() bool {
				return findFinish(env.hosts[executorIdx].MempoolTxs(), prepared.Nonce()) != nil
			}, 5*time.Second, 20*time.Millisecond)

			votes, recovery, _, err := env.session.CollectTimeoutVotes(ctx, prepared.Nonce(), types.TimeoutReason_TIMEOUT_REASON_EXECUTION, nil, env.session.TimeoutVerifiers(), env.session.Diffs())
			require.NoError(t, err)
			require.Empty(t, votes, "a finish signed by the executor rejects the execution timeout")

			execFinish := findFinish(env.hosts[executorIdx].MempoolTxs(), prepared.Nonce())
			require.NotNil(t, execFinish)
			copied := requireFinishInVerifierMempools(t, env, executorIdx, prepared.Nonce())
			require.Equal(t, types.TxHash(execFinish), types.TxHash(copied), "verifiers copy the executor's finish into their mempools")
			recovered := findFinish(recovery, prepared.Nonce())
			require.NotNil(t, recovered, "the reject reply is read from those mempools")
			require.Equal(t, types.TxHash(execFinish), types.TxHash(recovered))
			require.Nil(t, findConfirmStart(recovery, prepared.Nonce()))
		})
	}
}

func TestHTTP_RefusedTimeout_ForgedReceiptTimesOut(t *testing.T) {
	for _, wiring := range httpEvidenceWirings {
		t.Run(wiring.name, func(t *testing.T) {
			env := setupHTTPEnvWiring(t, 5, 1000000, 100, wiring.hostVerifier)
			ctx := context.Background()

			prepared, err := env.session.PrepareInference(defaultParams())
			require.NoError(t, err)
			executorIdx := prepared.HostIdx()
			forger := &forgingExecutor{}
			routeExecutorTo(env, executorIdx, forger.serve(t, env, executorIdx))

			result, err := env.session.HandleTimeout(ctx, prepared.Nonce(), time.Unix(0, 0), refusedPayload())
			require.Error(t, err)
			require.True(t, result.Applied, "forged evidence must not block the refusal: %+v", result)
			require.Positive(t, forger.challenges.Load(), "verifiers must have challenged the executor")

			st := env.session.StateMachine().SnapshotState()
			rec := st.Inferences[prepared.Nonce()]
			require.Equal(t, types.StatusTimedOut, rec.Status)
			require.Equal(t, uint32(1), st.HostStats[rec.ExecutorSlot].Missed)
			requireNoEvidenceCopied(t, env, executorIdx, prepared.Nonce())
		})
	}
}

func TestHTTP_ExecutionTimeout_ForgedFinishTimesOut(t *testing.T) {
	for _, wiring := range httpEvidenceWirings {
		t.Run(wiring.name, func(t *testing.T) {
			withHTTPTimeoutBuffer(t, 0)
			config := testutil.DefaultConfig(5)
			config.ExecutionTimeout = 0
			env := setupHTTPEnvWiring(t, 5, 1000000, 100, wiring.hostVerifier, config)
			ctx := context.Background()

			prepared, err := env.session.PrepareInference(defaultParams())
			require.NoError(t, err)
			executorIdx := prepared.HostIdx()
			rec := env.session.StateMachine().SnapshotState().Inferences[prepared.Nonce()]
			confirmedAt := time.Now().Unix()
			receipt := testutil.SignExecutorReceipt(t, env.signers[executorIdx], "escrow-1", prepared.Nonce(),
				rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, rec.StartedAt, confirmedAt)
			require.NoError(t, env.session.ProcessResponse(executorIdx, &host.HostResponse{Receipt: receipt, ConfirmedAt: confirmedAt}, prepared.Nonce()))
			require.NoError(t, env.session.SendPendingDiff(ctx))
			require.Equal(t, types.StatusStarted, env.session.StateMachine().SnapshotState().Inferences[prepared.Nonce()].Status)

			forger := &forgingExecutor{}
			routeExecutorTo(env, executorIdx, forger.serve(t, env, executorIdx))

			result, err := env.session.HandleTimeout(ctx, prepared.Nonce(), time.Unix(0, 0), nil)
			require.Error(t, err)
			require.Equal(t, "execution", result.Reason)
			require.True(t, result.Applied, "a finish the executor did not sign must not block the timeout: %+v", result)
			require.Positive(t, forger.challenges.Load())

			st := env.session.StateMachine().SnapshotState()
			require.Equal(t, types.StatusTimedOut, st.Inferences[prepared.Nonce()].Status)
			require.Equal(t, uint32(1), st.HostStats[rec.ExecutorSlot].Missed)
			requireNoEvidenceCopied(t, env, executorIdx, prepared.Nonce())
		})
	}
}

func TestHTTP_ExecutionTimeout_ExecutorFinishIsSequenced(t *testing.T) {
	for _, wiring := range httpEvidenceWirings {
		t.Run(wiring.name, func(t *testing.T) {
			withHTTPTimeoutBuffer(t, 0)
			config := testutil.DefaultConfig(5)
			config.ExecutionTimeout = 0
			env := setupHTTPEnvWiring(t, 5, 1000000, 100, wiring.hostVerifier, config)
			ctx := context.Background()

			prepared, err := env.session.PrepareInference(defaultParams())
			require.NoError(t, err)
			executorIdx := prepared.HostIdx()
			rec := env.session.StateMachine().SnapshotState().Inferences[prepared.Nonce()]
			confirmedAt := time.Now().Unix()
			receipt := testutil.SignExecutorReceipt(t, env.signers[executorIdx], "escrow-1", prepared.Nonce(),
				rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, rec.StartedAt, confirmedAt)
			require.NoError(t, env.session.ProcessResponse(executorIdx, &host.HostResponse{Receipt: receipt, ConfirmedAt: confirmedAt}, prepared.Nonce()))
			require.NoError(t, env.session.SendPendingDiff(ctx))
			require.Equal(t, types.StatusStarted, env.session.StateMachine().SnapshotState().Inferences[prepared.Nonce()].Status)

			forger := &forgingExecutor{signAsExecutor: true}
			routeExecutorTo(env, executorIdx, forger.serve(t, env, executorIdx))
			require.Nil(t, findFinish(env.hosts[executorIdx].MempoolTxs(), prepared.Nonce()),
				"the finish exists only in the challenge response")

			votes, recovery, _, err := env.session.CollectTimeoutVotes(ctx, prepared.Nonce(), types.TimeoutReason_TIMEOUT_REASON_EXECUTION, nil, env.session.TimeoutVerifiers(), env.session.Diffs())
			require.NoError(t, err)
			require.Empty(t, votes)
			copied := requireFinishInVerifierMempools(t, env, executorIdx, prepared.Nonce())
			require.Nil(t, findFinish(env.hosts[executorIdx].MempoolTxs(), prepared.Nonce()),
				"the executor host is not a voter and does not receive the copy")
			recovered := findFinish(recovery, prepared.Nonce())
			require.NotNil(t, recovered)
			require.Equal(t, types.TxHash(copied), types.TxHash(recovered))

			result, err := env.session.HandleTimeout(ctx, prepared.Nonce(), time.Unix(0, 0), nil)
			require.NoError(t, err, "an executor-signed finish is recovery, not a timeout: %+v", result)
			require.Equal(t, "execution", result.Reason)
			require.Zero(t, result.Votes)
			require.Positive(t, forger.challenges.Load())

			st := env.session.StateMachine().SnapshotState()
			require.Equal(t, types.StatusFinished, st.Inferences[prepared.Nonce()].Status)
			require.Zero(t, st.HostStats[rec.ExecutorSlot].Missed)
			diffs := env.session.Diffs()
			require.NotEmpty(t, diffs)
			require.NotNil(t, findFinish(diffs[len(diffs)-1].Txs, prepared.Nonce()),
				"the finish from the challenge response is in the diff the user sent")
		})
	}
}
