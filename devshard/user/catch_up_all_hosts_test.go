package user

import (
	"context"
	"testing"

	"devshard/internal/testutil"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

func warmupParams() InferenceParams {
	return InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
}

func TestCatchUpAllHostsTeachesHostsTheGatewayNeverDispatchedTo(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()

	_, err := session.SendInference(ctx, warmupParams())
	require.NoError(t, err)
	require.Equal(t, uint64(1), session.Nonce())

	session.mu.Lock()
	behind := 0
	for slot := 0; slot < len(session.group); slot++ {
		if session.hostSyncNonce[slot] == 0 {
			behind++
		}
	}
	session.mu.Unlock()
	require.Equal(t, 2, behind, "only the dispatched slot has seen a diff")

	require.NoError(t, session.CatchUpAllHosts(ctx))

	session.mu.Lock()
	defer session.mu.Unlock()
	for slot := 0; slot < len(session.group); slot++ {
		require.Equal(t, uint64(1), session.hostSyncNonce[slot],
			"slot %d must hold the escrow, or it answers a timeout vote with session not found", slot)
	}
}

func TestCatchUpAllHostsSpendsNoNonce(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()

	_, err := session.SendInference(ctx, warmupParams())
	require.NoError(t, err)
	before := session.Nonce()

	require.NoError(t, session.CatchUpAllHosts(ctx))

	require.Equal(t, before, session.Nonce(), "catch-up replays diffs that exist; it must not commit new ones")
}

func TestCatchUpAllHostsRefusesOutsideTheActivePhase(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()

	_, err := session.SendInference(ctx, warmupParams())
	require.NoError(t, err)
	require.NoError(t, session.Finalize(ctx))
	require.NotEqual(t, types.PhaseActive, session.sm.Phase())

	require.Error(t, session.CatchUpAllHosts(ctx))
}
