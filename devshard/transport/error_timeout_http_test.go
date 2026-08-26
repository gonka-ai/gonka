package transport

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"common/completionapi"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func engineCoreResponsePayload(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(completionapi.SerializedStreamedResponse{Events: []string{
		`data: {"error":{"code":500,"message":"EngineCore encountered an issue. See stack trace (above) for the root cause.","param":null,"type":"InternalServerError"},"id":"devshard-57577-89"}`,
		`data: [DONE]`,
	}})
	require.NoError(t, err)
	return b
}

func applyStartedInference(t *testing.T, env *serverTestEnv, inferenceID uint64) {
	t.Helper()
	ctx := context.Background()
	start := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(inferenceID)})
	_, err := env.server.host.HandleRequest(ctx, host.HostRequest{Diffs: []types.Diff{start}})
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, env.hostSigner, "escrow-1", inferenceID, testutil.TestPromptHash[:], "llama", 100, 50, 1000, 1000)
	confirm := testutil.SignDiff(t, env.userSigner, "escrow-1", 2, []*types.DevshardTx{
		{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
			InferenceId: inferenceID, ExecutorSig: execSig, ConfirmedAt: 1000,
		}}},
	})
	_, err = env.server.host.HandleRequest(ctx, host.HostRequest{Diffs: []types.Diff{confirm}})
	require.NoError(t, err)
	require.Equal(t, types.StatusStarted, env.server.host.SnapshotState().Inferences[inferenceID].Status)
}

func TestServer_VerifyTimeout_ERRORAcceptsAndBindsHash(t *testing.T) {
	env := setupServerEnv(t)
	applyStartedInference(t, env, 1)

	payload := engineCoreResponsePayload(t)
	sum := sha256.Sum256(payload)
	msg := &types.MsgFinishInference{
		InferenceId:  1,
		ResponseHash: sum[:],
		ExecutorSlot: 0,
		EscrowId:     "escrow-1",
	}
	msg.ProposerSig = testutil.SignProposerTx(t, env.hostSigner, msg)
	finishTx, err := proto.Marshal(&types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}})
	require.NoError(t, err)

	body, err := json.Marshal(VerifyTimeoutRequest{
		InferenceID:     1,
		Reason:          "error",
		FinishTx:        finishTx,
		ResponsePayload: payload,
	})
	require.NoError(t, err)

	rec := env.doPost(t, testRoutePrefix+"/sessions/escrow-1/verify-timeout", body)
	require.Equal(t, 200, rec.Code, rec.Body.String())

	var resp VerifyTimeoutResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.Accept)
	require.NotEmpty(t, resp.Signature)

	content := &types.TimeoutVoteContent{
		EscrowId:     "escrow-1",
		InferenceId:  1,
		Reason:       types.TimeoutReason_TIMEOUT_REASON_ERROR,
		Accept:       true,
		ResponseHash: sum[:],
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(content)
	require.NoError(t, err)
	verifier := signing.NewSecp256k1Verifier()
	recovered, err := verifier.RecoverAddress(data, resp.Signature)
	require.NoError(t, err)
	require.Equal(t, env.hostSigner.Address(), recovered)
}

func TestServer_VerifyTimeout_ERRORRejectsWithoutArtifacts(t *testing.T) {
	env := setupServerEnv(t)
	applyStartedInference(t, env, 1)

	body, err := json.Marshal(VerifyTimeoutRequest{InferenceID: 1, Reason: "error"})
	require.NoError(t, err)
	rec := env.doPost(t, testRoutePrefix+"/sessions/escrow-1/verify-timeout", body)
	require.Equal(t, 200, rec.Code, rec.Body.String())

	var resp VerifyTimeoutResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.False(t, resp.Accept)
	require.Empty(t, resp.Signature)
	require.Equal(t, host.ErrorTimeoutRejectNoFinishTx, resp.RejectCause)
}
