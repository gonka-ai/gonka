package user

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
)

const sessionStateNonceCount = 120

// corruptedStateStore drops one live entry from the stored state, as a state write that went wrong.
type corruptedStateStore struct {
	*storage.SQLite
}

func (store corruptedStateStore) LoadSessionState(escrowID string) (storage.SessionState, error) {
	stored, err := store.SQLite.LoadSessionState(escrowID)
	for id := range stored.Entries {
		delete(stored.Entries, id)
		break
	}
	return stored, err
}

// advanceSession composes nonceCount diffs, a real inference every inferenceEveryNonces of them.
func advanceSession(t *testing.T, session *Session, nonceCount int, afterEach func()) {
	t.Helper()
	for nonce := 1; nonce <= nonceCount; nonce++ {
		if nonce%inferenceEveryNonces == 1 {
			_, err := session.SendInference(context.Background(), storedCatchUpInference())
			require.NoError(t, err)
		} else {
			composeUnsentDiffs(t, session, 1)
		}
		afterEach()
	}
}

// requireStoredStateMatchesLive rebuilds the stored state in a fresh machine and compares its nonce and root with the live session.
func requireStoredStateMatchesLive(t *testing.T, store storage.SessionStateStore, live *Session, liveMachine *state.StateMachine) {
	t.Helper()
	stored, err := store.LoadSessionState(live.escrowID)
	require.NoError(t, err)
	require.Equal(t, live.Nonce(), stored.Nonce, "stored state nonce")
	restored, _, err := decodeSessionState(stored)
	require.NoError(t, err)
	machine := newTestStateMachine(t, live.escrowID, liveMachine.Config(), live.group, 100000, live.signer.Address(), signing.NewSecp256k1Verifier())
	machine.RestoreState(restored)
	storedRoot, err := machine.ComputeStateRoot()
	require.NoError(t, err)
	liveRoot, err := liveMachine.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, liveRoot, storedRoot, "stored state root at nonce %d", stored.Nonce)
}

// Test flow:
//  1. Run a session on a store that writes state with each diff, mixing inferences and empty diffs.
//  2. After every diff, rebuild the stored state in a fresh machine.
//  3. Its nonce and state root equal the live session's at every step.
//  4. Finalize, which drains every live record into the sealed accumulator: the stored live set empties with it.
func TestSessionStateWrittenWithEachDiffMatchesLiveState(t *testing.T) {
	store := newTestStore(t)
	live, liveMachine, _, _, _ := buildLiveSession(t, 3, store)

	advanceSession(t, live, sessionStateNonceCount, func() {
		requireStoredStateMatchesLive(t, store, live, liveMachine)
	})
	require.NoError(t, live.Finalize(context.Background()))

	requireStoredStateMatchesLive(t, store, live, liveMachine)
	stored, err := store.LoadSessionState(live.escrowID)
	require.NoError(t, err)
	require.Empty(t, stored.Entries, "the settlement drain removes every live record")
}

// Test flow:
//  1. Run a session whose store hides state writes, so only the journal and snapshots exist.
//  2. Recover it on the same store with state writes, then compose one diff.
//  3. That first diff stores the whole live set: the stored state equals the live one.
func TestSessionStateFirstDiffAfterRecoveryStoresWholeLiveSet(t *testing.T) {
	store := newTestStore(t)
	history, _, group, hostKeys, userKey := buildLiveSession(t, 3, snapshotOnlyStore{store})
	advanceSession(t, history, sessionStateNonceCount, func() {})
	_, err := store.LoadSessionState(history.escrowID)
	require.ErrorIs(t, err, storage.ErrSessionStateNotFound)

	recovered, recoveredMachine, err := RecoverSession(store, userKey, signing.NewSecp256k1Verifier(), history.escrowID, testutil.RuntimeTestVersion, group, buildRecoveryClients(t, hostKeys, group, userKey))
	require.NoError(t, err)
	composeUnsentDiffs(t, recovered, 1)

	requireStoredStateMatchesLive(t, store, recovered, recoveredMachine)
}

// Test flow:
//  1. Run a session that writes state with each diff.
//  2. Recover it: the state root and nonce equal the live session's without replaying the journal into memory.
//  3. Recover it again through a store whose state lost an entry: the root check rejects it and replay still yields the live root.
func TestRecoverSessionFromStateWrittenWithDiffs(t *testing.T) {
	store := newTestStore(t)
	live, liveMachine, group, hostKeys, userKey := buildLiveSession(t, 3, store)
	advanceSession(t, live, sessionStateNonceCount, func() {})
	liveRoot, err := liveMachine.ComputeStateRoot()
	require.NoError(t, err)

	for _, testCase := range []struct {
		name           string
		store          storage.Storage
		replaysJournal bool
	}{
		{name: "stored state", store: store},
		{name: "corrupted stored state", store: corruptedStateStore{store}, replaysJournal: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recovered, recoveredMachine, err := RecoverSession(testCase.store, userKey, signing.NewSecp256k1Verifier(), live.escrowID, testutil.RuntimeTestVersion, group, buildRecoveryClients(t, hostKeys, group, userKey))
			require.NoError(t, err)
			require.Equal(t, live.Nonce(), recovered.Nonce())
			recoveredRoot, err := recoveredMachine.ComputeStateRoot()
			require.NoError(t, err)
			require.Equal(t, liveRoot, recoveredRoot)
			if testCase.replaysJournal {
				require.Len(t, recovered.Diffs(), sessionStateNonceCount, "a rejected state replays the whole journal")
			} else {
				require.Less(t, len(recovered.Diffs()), sessionStateNonceCount, "a restored state skips the replay")
			}
		})
	}
}
