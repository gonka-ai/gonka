package user

import (
	"context"
	"fmt"
	"testing"
	"time"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

func TestComposeDiffRollsBackWhenPersistenceFails(t *testing.T) {
	store := &closeCountingStore{appendErr: fmt.Errorf("disk unavailable")}
	session, _, _ := setupSessionWithOptions(t, 3, 1_000_000, 0, WithStorage(store))
	var committed []types.Diff
	_, err := session.SetCommittedDiffHook(0, func(diff types.Diff) { committed = append(committed, diff) })
	require.NoError(t, err)
	prepared, err := session.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 1, MaxTokens: 1, StartedAt: 1,
	})
	require.ErrorContains(t, err, "persist diff")
	require.Nil(t, prepared)
	require.Zero(t, session.Nonce())
	require.Zero(t, session.StateMachine().LatestNonce())
	_, found := session.StateMachine().GetInference(1)
	require.False(t, found)
	require.Empty(t, committed)
}

func TestCommittedDiffHookAndStartupTail(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	var committed []uint64
	tail, err := session.SetCommittedDiffHook(0, func(diff types.Diff) {
		committed = append(committed, diff.Nonce)
	})
	require.NoError(t, err)
	require.Empty(t, tail)
	_, err = session.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 1, MaxTokens: 1, StartedAt: 1,
	})
	require.NoError(t, err)
	require.Equal(t, []uint64{1}, committed)

	tail, err = session.SetCommittedDiffHook(0, nil)
	require.NoError(t, err)
	require.Len(t, tail, 1)
	require.Equal(t, uint64(1), tail[0].Nonce)
}

func TestCommittedDiffHookLoadsDurableTail(t *testing.T) {
	store := &closeCountingStore{}
	session, _, _ := setupSessionWithOptions(t, 3, 1_000_000, 0, WithStorage(store))
	_, err := session.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 1, MaxTokens: 1, StartedAt: 1,
	})
	require.NoError(t, err)
	require.Len(t, store.diffs, 1)

	// Recovery may retain only a post-snapshot in-memory suffix. Accounting
	// must read its tail from the durable diff journal instead.
	session.diffs = nil
	tail, err := session.SetCommittedDiffHook(0, nil)
	require.NoError(t, err)
	require.Len(t, tail, 1)
	require.Equal(t, uint64(1), tail[0].Nonce)
}

func TestHandleTimeoutReturnsStructuredAppliedResult(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	for i, client := range session.clients {
		session.clients[i] = &timeoutCapableClient{
			HostClient: client,
			verifier: &mockTimeoutVerifier{
				accept: true, signer: hosts[i], group: session.group, slotIdx: i,
			},
		}
	}
	prepared, err := session.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})
	require.NoError(t, err)

	result, err := session.HandleTimeout(
		context.Background(),
		prepared.Nonce(),
		time.Now().Add(-time.Hour),
		&host.InferencePayload{
			Prompt: testutil.TestPrompt, Model: "llama",
			InputLength: 100, MaxTokens: 50, StartedAt: 1000,
		},
	)
	require.NoError(t, err)
	require.Equal(t, "refused", result.Reason)
	require.Equal(t, "applied", result.Outcome)
	require.True(t, result.Applied)
	require.True(t, result.Delivered)
	require.Greater(t, result.AcceptWeight, uint32(0))
}

func TestHandleTimeoutReportsCanceledWaitAsSkipped(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	prepared, err := session.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := session.HandleTimeout(ctx, prepared.Nonce(), time.Now(), &host.InferencePayload{})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "skipped", result.Outcome)
	require.Equal(t, "context_canceled", result.DetailReason)
}

type timeoutCapableClient struct {
	HostClient
	verifier TimeoutVerifier
}

func (c *timeoutCapableClient) VerifyTimeout(
	ctx context.Context,
	inferenceID uint64,
	reason types.TimeoutReason,
	payload *host.InferencePayload,
	diffs []types.Diff,
) (bool, []byte, uint32, error) {
	return c.verifier.VerifyTimeout(ctx, inferenceID, reason, payload, diffs)
}
