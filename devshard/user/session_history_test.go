package user

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/storage"
)

func hostSlot(session *Session, hostIdx int) uint32 {
	return session.addrToSlots[session.group[hostIdx].ValidatorAddress][0]
}

// Test flow (from 4eeb3ad31, triggered here by pruneSessionHistoryLocked):
//  1. Hold signatures at nonces 1, 2, 3 and the current 6, then move every host cursor to 6.
//  2. Prune: nonce 1 goes; 2 and 3 stay as some validator's highest; 6 stays as current.
//  3. The quorum status is unchanged and the trim stops below the current nonce.
func TestSession_TrimKeepsTheCurrentNonceAndTheQuorumStatus(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	composeUnsentDiffs(t, session, 6)
	firstSlot, secondSlot, thirdSlot := hostSlot(session, 0), hostSlot(session, 1), hostSlot(session, 2)
	signature := []byte("sig")

	session.mu.Lock()
	defer session.mu.Unlock()
	session.signatures = map[uint64]map[uint32][]byte{
		1: {firstSlot: signature, secondSlot: signature},
		2: {secondSlot: signature},
		3: {firstSlot: signature},
		6: {thirdSlot: signature},
	}
	wantEntries, wantHighest, wantAny := session.signatureStatusLocked()
	for hostIdx := range session.group {
		session.hostSyncNonce[hostIdx] = 6
	}
	session.pruneSessionHistoryLocked()

	require.ElementsMatch(t, []uint64{2, 3, 6}, slices.Collect(maps.Keys(session.signatures)),
		"nonce 1 is no validator's highest; 2 and 3 are, and 6 is current")
	_, gotHighest, gotAny := session.signatureStatusLocked()
	require.Equal(t, wantAny, gotAny)
	require.Equal(t, wantHighest, gotHighest, "the trim does not change the quorum nonce")
	require.NotEmpty(t, wantEntries)
	require.Equal(t, uint64(5), session.sigsTrimmedThrough, "the current nonce is never trimmed")
}

// Test flow (from 4eeb3ad31):
//  1. Store a full quorum of signatures for nonce 2 and trim memory past it.
//  2. Nonce 2 is gone from memory, yet its quorum check reads the stored signatures.
//  3. Nonce 3, never signed, still has no quorum.
func TestSession_QuorumAtATrimmedNonceIsReadFromTheStore(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	composeUnsentDiffs(t, session, 6)
	for hostIdx := range session.group {
		for _, slot := range session.addrToSlots[session.group[hostIdx].ValidatorAddress] {
			require.NoError(t, session.store.AddSignature(session.escrowID, 2, slot, []byte("sig")))
		}
	}

	session.mu.Lock()
	for hostIdx := range session.group {
		session.hostSyncNonce[hostIdx] = 6
	}
	session.pruneSessionHistoryLocked()
	require.NotContains(t, session.signatures, uint64(2))
	session.mu.Unlock()

	require.True(t, session.hasQuorum(2, session.sm.QuorumThreshold()),
		"a quorum check below the trim floor reads the stored signatures")
	require.False(t, session.hasQuorum(3, session.sm.QuorumThreshold()))
}

// Test flow:
//  1. Send inferences so each nonce gets an outcome, then finalize, which seals every live inference.
//  2. Before pruning, the outcomes are still held; after pruning none are left.
//  3. A session with live inferences keeps their outcomes through a prune.
func TestSession_PruneDropsOutcomesOfSealedInferencesOnly(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	for range len(session.group) {
		_, err := session.SendInference(context.Background(), storedCatchUpInference())
		require.NoError(t, err)
	}

	session.mu.Lock()
	liveOutcomes := len(session.nonceStates)
	session.pruneSessionHistoryLocked()
	require.Len(t, session.nonceStates, liveOutcomes, "live inferences keep their outcomes")
	session.mu.Unlock()
	require.NotZero(t, liveOutcomes)

	require.NoError(t, session.Finalize(context.Background()))
	session.mu.Lock()
	defer session.mu.Unlock()
	require.NotEmpty(t, session.nonceStates, "precondition: outcomes outlive the seal until a prune")
	session.pruneSessionHistoryLocked()
	require.Empty(t, session.nonceStates, "the settlement drain sealed every inference")
}
