package user

import (
	"context"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/types"
	"errors"
	"github.com/stretchr/testify/require"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type failedRecoverySendClient struct {
	HostClient
	*mockTimeoutVerifier
	sends    atomic.Int32
	recovery []*types.DevshardTx
}

func (c *failedRecoverySendClient) Send(ctx context.Context, req host.HostRequest, w io.Writer, receipt func(*host.HostResponse)) (*host.HostResponse, error) {
	if c.sends.Add(1) == 1 {
		return nil, errors.New("temporary recovery delivery failure")
	}
	return c.HostClient.Send(ctx, req, w, receipt)
}
func (c *failedRecoverySendClient) VerifyTimeout(ctx context.Context, id uint64, reason types.TimeoutReason, p *host.InferencePayload, diffs []types.Diff, art host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	if reason == types.TimeoutReason_TIMEOUT_REASON_REFUSED && len(c.recovery) > 0 {
		return false, nil, 0, c.recovery, "", nil
	}
	return c.mockTimeoutVerifier.VerifyTimeout(ctx, id, reason, p, diffs, art)
}

func TestHandleTimeoutContinuesAfterRecoverySendFailure(t *testing.T) {
	for _, scenario := range []string{"queued_receipt", "queued_invalid_finish", "recovered_receipt"} {
		t.Run(scenario, func(t *testing.T) {
			session, signers, _ := setupSession(t, 3, 100000, 10)
			params := InferenceParams{Model: "llama", Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}
			prepared, err := session.PrepareInference(params)
			require.NoError(t, err)
			nonce := prepared.Nonce()
			executor := int(nonce % 3)
			receipt := testutil.SignExecutorReceipt(t, signers[executor], "escrow-1", nonce, testutil.TestPromptHash[:], "llama", 100, testutil.TestMaxTokens, 1000, 1000)
			confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: nonce, ExecutorSig: receipt, ConfirmedAt: 1000}}}
			if scenario != "recovered_receipt" {
				session.confirmStartOnReceipt(nonce, &host.HostResponse{Receipt: receipt, ConfirmedAt: 1000})
			}
			if scenario == "queued_invalid_finish" {
				require.NoError(t, session.SendPendingDiff(context.Background()))
				bad := &types.MsgFinishInference{InferenceId: nonce, ExecutorSlot: uint32(executor), EscrowId: "escrow-1", ResponseHash: []byte("short"), ServedHash: testutil.TestServedHash}
				bad.ProposerSig = testutil.SignProposerTx(t, signers[executor], bad)
				session.mu.Lock()
				session.addPendingTx(&types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: bad}})
				session.mu.Unlock()
			}
			for i, client := range session.clients {
				wrapper := &failedRecoverySendClient{HostClient: client, mockTimeoutVerifier: &mockTimeoutVerifier{accept: true, signer: signers[i], group: session.group, slotIdx: i}}
				if scenario == "recovered_receipt" {
					wrapper.recovery = []*types.DevshardTx{confirm}
				}
				session.clients[i] = wrapper
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			payload := &host.InferencePayload{Prompt: params.Prompt, Model: params.Model, InputLength: params.InputLength, MaxTokens: params.MaxTokens, StartedAt: params.StartedAt}
			result, err := session.HandleTimeout(ctx, nonce, time.Unix(1000, 0), payload)
			require.Error(t, err, "an applied timeout is reported as an inference error")
			require.Equal(t, "execution", result.Reason)
			require.True(t, result.Applied, "a failed recovery send must not abandon the timeout")
			require.Equal(t, types.StatusTimedOut, session.sm.SnapshotState().Inferences[nonce].Status)
			require.Zero(t, session.sm.SnapshotState().HostStats[uint32(executor)].Cost)
		})
	}
}

func TestHandleTimeoutRefusalVoteRaceContinuesAsExecution(t *testing.T) {
	session, signers, _ := setupSession(t, 3, 100000, 10)
	params := InferenceParams{Model: "llama", Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}
	prepared, err := session.PrepareInference(params)
	require.NoError(t, err)
	nonce := prepared.Nonce()
	receipt := testutil.SignExecutorReceipt(t, signers[nonce%3], "escrow-1", nonce, testutil.TestPromptHash[:], "llama", 100, testutil.TestMaxTokens, 1000, 1000)
	var once sync.Once
	for i, client := range session.clients {
		session.clients[i] = &timeoutVoteClient{HostClient: client, mockTimeoutVerifier: &mockTimeoutVerifier{
			accept: true, signer: signers[i], group: session.group, slotIdx: i,
			onVerify: func() {
				once.Do(func() { session.confirmStartOnReceipt(nonce, &host.HostResponse{Receipt: receipt, ConfirmedAt: 1000}) })
			},
		}}
	}
	payload := &host.InferencePayload{Prompt: params.Prompt, Model: params.Model, InputLength: params.InputLength, MaxTokens: params.MaxTokens, StartedAt: params.StartedAt}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := session.HandleTimeout(ctx, nonce, time.Unix(1000, 0), payload)
	require.Error(t, err)
	require.Equal(t, "execution", result.Reason)
	require.True(t, result.Applied)
	require.Equal(t, types.StatusTimedOut, session.sm.SnapshotState().Inferences[nonce].Status)
}
