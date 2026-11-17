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
		_, _, err := session.composeDiffLocked(context.Background(), nil)
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

// A mismatch recorded for a nonce that never had an outcome is dropped once
// that inference is sealed past the retention window. A sample inside the
// window, and one that was never sealed, stay.
func TestDropSealedOutcomesDropsUntrackedClockMismatches(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	session.mu.Lock()
	defer session.mu.Unlock()
	session.nonce = nonceOutcomeRetention + 10
	cutoff := session.nonce - nonceOutcomeRetention
	session.sm.RestoreSealedNonces(map[uint64]uint64{
		4: 1,
		7: 1,
		8: cutoff + 1,
	})
	session.nonceStates[4] = &nonceOutcome{}
	session.clockMismatchOnce = map[clockMismatchKey]struct{}{
		{nonce: 4, kind: "refused"}:    {},
		{nonce: 7, kind: "refused"}:    {},
		{nonce: 7, kind: "execution"}:  {},
		{nonce: 8, kind: "refused"}:    {},
		{nonce: 99, kind: "execution"}: {},
	}

	session.dropSealedOutcomesLocked()

	require.NotContains(t, session.nonceStates, uint64(4))
	require.NotContains(t, session.clockMismatchOnce, clockMismatchKey{nonce: 4, kind: "refused"})
	require.NotContains(t, session.clockMismatchOnce, clockMismatchKey{nonce: 7, kind: "refused"})
	require.NotContains(t, session.clockMismatchOnce, clockMismatchKey{nonce: 7, kind: "execution"})
	require.Contains(t, session.clockMismatchOnce, clockMismatchKey{nonce: 8, kind: "refused"})
	require.Contains(t, session.clockMismatchOnce, clockMismatchKey{nonce: 99, kind: "execution"})
}
