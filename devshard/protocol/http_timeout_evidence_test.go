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

// forgingExecutor answers every ChallengeReceipt with evidence that names the
// right inference, escrow, and executor slot but carries no executor signature:
// a forged receipt with a matching ConfirmStart, and a Finish signed by another
// group member.
type forgingExecutor struct {
	challenges atomic.Int32
}

func (f *forgingExecutor) serve(t *testing.T, env *httpTestEnv, executorIdx int) *transport.HTTPClient {
	t.Helper()
	otherIdx := (executorIdx + 1) % len(env.signers)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req transport.ChallengeReceiptRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.challenges.Add(1)
		forged := []byte("forged-receipt")
		finish := &types.MsgFinishInference{
			InferenceId:  req.InferenceID,
			EscrowId:     "escrow-1",
			ExecutorSlot: env.group[executorIdx].SlotID,
			ResponseHash: testutil.TestResponseHash,
			ServedHash:   testutil.TestServedHash,
		}
		finish.ProposerSig = testutil.SignProposerTx(t, env.signers[otherIdx], finish)
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

			votes, _, _, err := env.session.CollectTimeoutVotes(ctx, prepared.Nonce(), types.TimeoutReason_TIMEOUT_REASON_EXECUTION, nil, env.session.TimeoutVerifiers(), env.session.Diffs())
			require.NoError(t, err)
			require.Empty(t, votes, "a finish signed by the executor rejects the execution timeout")
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
