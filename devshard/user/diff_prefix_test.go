package user

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/types"
)

func TestSession_DropDiffPrefixWhenEveryHostPassesN(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	const n = uint64(4)

	session.mu.Lock()
	for nonce := uint64(1); nonce <= n+2; nonce++ {
		session.diffs = append(session.diffs, types.Diff{Nonce: nonce})
	}
	session.nonce = n + 2
	session.hostSyncNonce[0] = n
	session.hostSyncNonce[1] = n + 2
	session.hostSyncNonce[2] = n - 1
	session.dropDiffPrefixLocked()
	require.Equal(t, n, session.diffs[0].Nonce, "a host still at N-1 keeps the nonce it has not applied")

	session.hostSyncNonce[2] = n
	session.dropDiffPrefixLocked()
	require.Equal(t, n+1, session.diffs[0].Nonce, "once every host cursor passes N, the suffix starts at N+1")
	require.Equal(t, n+2, session.diffs[len(session.diffs)-1].Nonce)
	session.mu.Unlock()

	require.True(t, session.RewindHostCatchUp(1, "host lost the escrow"))
	session.mu.Lock()
	require.Equal(t, n, session.hostSyncNonce[1], "rewind stops at the trimmed start")
	session.mu.Unlock()
}

func TestSession_LiveCursorDropsTheDiffPrefix(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	for range 2 {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}
	require.Equal(t, uint64(1), session.Diffs()[0].Nonce, "a host with no cursor keeps the prefix")

	_, err := session.SendInference(ctx, params)
	require.NoError(t, err)
	require.Equal(t, uint64(2), session.Diffs()[0].Nonce, "the slowest cursor is nonce 1, so the suffix starts at 2")

	_, err = session.PrepareInference(context.Background(), params)
	require.NoError(t, err)
	diffs := session.Diffs()
	require.Equal(t, uint64(2), diffs[0].Nonce)
	require.Equal(t, uint64(4), diffs[len(diffs)-1].Nonce, "compose keeps the new diff and the suffix the slowest host still needs")

	require.False(t, session.RewindHostCatchUp(1, "host lost the escrow"),
		"host 1 is already at the trimmed start")
}

// Test flow:
//  1. Build a session without a store whose diffs run 1..3, and move every host cursor to 2 so the prefix through nonce 2 is dropped.
//  2. Process host 0's final response at nonce 2, the nonce its receipt already moved its cursor to, carrying the session's own root there.
//  3. Assert it verifies: the slowest cursor's root is kept, so a late response at that cursor is not taken for a state mismatch.
func TestSession_AFinalResponseAtTheDroppedFloorStillVerifies(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	root := bytes.Repeat([]byte{7}, 32)

	session.mu.Lock()
	defer session.mu.Unlock()
	for nonce := uint64(1); nonce <= 3; nonce++ {
		session.diffs = append(session.diffs, types.Diff{Nonce: nonce, PostStateRoot: bytes.Repeat([]byte{byte(nonce)}, 32)})
	}
	session.diffs[1].PostStateRoot = root
	session.nonce = 3
	for i := range 3 {
		session.hostSyncNonce[i] = 2
	}
	session.dropDiffPrefixLocked()
	require.Equal(t, uint64(3), session.diffs[0].Nonce)

	require.NoError(t, session.processResponse(0, &host.HostResponse{Nonce: 2, StateHash: root}, 2))
}
