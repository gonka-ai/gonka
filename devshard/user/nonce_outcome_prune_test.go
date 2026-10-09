package user

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/storage"
)

func storedSessionInference() InferenceParams {
	return InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
}

func composeUnsentDiffs(t *testing.T, session *Session, count int) {
	t.Helper()
	session.mu.Lock()
	defer session.mu.Unlock()
	for range count {
		_, _, err := session.composeDiffLocked(nil)
		require.NoError(t, err)
	}
}

// Test flow:
//  1. Start one inference per host on a stored session and compose past the retention window: live outcomes stay.
//  2. Finalize to seal them: an outcome sealed within the window stays, however old its nonce.
//  3. Compose one diff short of the window past the seal: the outcomes stay.
//  4. Compose the last diff of the window: the sealed outcomes are dropped.
func TestSession_DropSealedOutcomesKeepsThemAWindowPastTheirSeal(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	for range len(session.group) {
		_, err := session.SendInference(context.Background(), storedSessionInference())
		require.NoError(t, err)
	}
	composeUnsentDiffs(t, session, nonceOutcomeRetention+10)

	session.mu.Lock()
	liveOutcomes := len(session.nonceStates)
	session.dropSealedOutcomesLocked()
	require.Len(t, session.nonceStates, liveOutcomes, "live inferences keep their outcomes")
	session.mu.Unlock()
	require.Equal(t, len(session.group), liveOutcomes)

	require.NoError(t, session.Finalize(context.Background()))
	session.mu.Lock()
	session.dropSealedOutcomesLocked()
	require.Len(t, session.nonceStates, liveOutcomes, "an outcome sealed within the retention window stays, however old its nonce")
	session.mu.Unlock()

	sealNonce := session.Nonce()
	composeUnsentDiffs(t, session, nonceOutcomeRetention-1)
	session.mu.Lock()
	session.dropSealedOutcomesLocked()
	require.Len(t, session.nonceStates, liveOutcomes, "sealed %d nonces ago, one short of the window", nonceOutcomeRetention-1)
	session.mu.Unlock()

	composeUnsentDiffs(t, session, 1)
	require.Equal(t, sealNonce+nonceOutcomeRetention, session.Nonce())
	session.mu.Lock()
	defer session.mu.Unlock()
	session.dropSealedOutcomesLocked()
	require.Empty(t, session.nonceStates, "outcomes sealed a full retention window ago are dropped")
}
