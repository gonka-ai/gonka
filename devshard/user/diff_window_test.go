package user

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/types"
)

const bareHistoryLength = 3 * defaultDiffsKeptInMemory

func recoverOverBareHistory(t *testing.T, store storage.Storage, rawStore storage.Storage, hostCursors map[int]uint64) *Session {
	t.Helper()
	const numHosts = 3
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, rawStore.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))
	for nonce := uint64(1); nonce <= bareHistoryLength; nonce++ {
		require.NoError(t, rawStore.AppendDiff("escrow-1", types.DiffRecord{Diff: types.Diff{Nonce: nonce}}))
	}
	if hostCursors != nil {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		saveSnapshot(rawStore, sm, "escrow-1", bareHistoryLength, hostCursors)
	}

	session, _, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group,
		buildRecoveryClients(t, hosts, group, user))
	require.NoError(t, err)
	return session
}

func requireContiguousDiffs(t *testing.T, diffs []types.Diff, firstNonce, lastNonce uint64) {
	t.Helper()
	require.Len(t, diffs, int(lastNonce-firstNonce+1))
	for offset, diff := range diffs {
		require.Equal(t, firstNonce+uint64(offset), diff.Nonce)
	}
}

func buildLiveSessionWithSmallDiffWindow(t *testing.T, diffCount int) (*Session, storage.Storage) {
	t.Helper()
	store := newTestStore(t)
	session, _, _, _, _ := buildLiveSession(t, 3, store)
	session.diffsKeptInMemory = 2
	for range diffCount {
		_, err := session.PrepareInference(InferenceParams{
			Model: "llama", Prompt: testutil.TestPrompt,
			InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
		})
		require.NoError(t, err)
	}
	require.Equal(t, uint64(diffCount), session.Nonce())
	return session, store
}

func TestRecoverSession_ReadsHistoryInPages(t *testing.T) {
	// Test flow:
	// 1. Store a history three pages long with a snapshot at its tip.
	// 2. Recover the session through a store that records every diff read.
	// 3. No single read may span more than one page.
	rawStore := newTestStore(t)
	spy := &replaySpyStore{Storage: rawStore}
	upToDate := map[int]uint64{0: bareHistoryLength, 1: bareHistoryLength, 2: bareHistoryLength}

	recoverOverBareHistory(t, spy, rawStore, upToDate)

	require.NotEmpty(t, spy.calls)
	for _, call := range spy.calls {
		require.LessOrEqual(t, call.to-call.from+1, uint64(recoverDiffPageSize),
			"read %d..%d spans more than one page", call.from, call.to)
	}
}

func TestRecoverSession_ReplaysHistoryWithoutSnapshotInPages(t *testing.T) {
	// Test flow:
	// 1. Store a history three pages long and no snapshot.
	// 2. Recover the session through a store that records every diff read, so it replays from nonce 1.
	// 3. No single read may span more than one page.
	rawStore := newTestStore(t)
	spy := &replaySpyStore{Storage: rawStore}

	session := recoverOverBareHistory(t, spy, rawStore, nil)

	require.Equal(t, uint64(bareHistoryLength), session.Nonce())
	for _, call := range spy.calls {
		require.LessOrEqual(t, call.to-call.from+1, uint64(recoverDiffPageSize),
			"read %d..%d spans more than one page", call.from, call.to)
	}
}

func TestRecoverSession_StrandedHostIsServedFromStoreNotMemory(t *testing.T) {
	// Test flow:
	// 1. Store a long history with a snapshot at its tip and host 0 stranded at nonce 10.
	// 2. Recover the session.
	// 3. Memory holds less than twice the diff window.
	// 4. Host 0 is still handed every diff from nonce 11 to the tip.
	rawStore := newTestStore(t)
	strandedHost := map[int]uint64{0: 10, 1: bareHistoryLength, 2: bareHistoryLength}

	session := recoverOverBareHistory(t, rawStore, rawStore, strandedHost)

	require.Less(t, len(session.Diffs()), 2*session.diffsKeptInMemory)
	session.mu.Lock()
	catchUp := session.diffsForHost(0)
	session.mu.Unlock()
	requireContiguousDiffs(t, catchUp, 11, bareHistoryLength)
}

func TestSession_TrimsDiffsInMemoryAndServesLaggingHostFromStore(t *testing.T) {
	// Test flow:
	// 1. Run a stored session with a two-diff window through ten nonces.
	// 2. Memory holds less than twice the window.
	// 3. A host whose cursor is at nonce 1 is handed every diff from 2 to 10.
	session, _ := buildLiveSessionWithSmallDiffWindow(t, 10)

	require.Less(t, len(session.Diffs()), 2*session.diffsKeptInMemory)
	session.mu.Lock()
	session.hostSyncNonce[2] = 1
	catchUp := session.diffsForHost(2)
	session.mu.Unlock()
	requireContiguousDiffs(t, catchUp, 2, 10)
}

func TestSession_VerifiesStateHashForNonceTrimmedFromMemory(t *testing.T) {
	// Test flow:
	// 1. Run a stored session with a two-diff window through ten nonces.
	// 2. Take the stored post-state root of nonce 2, which memory no longer holds.
	// 3. A host response at nonce 2 carrying that root is accepted.
	session, store := buildLiveSessionWithSmallDiffWindow(t, 10)
	records, err := store.GetDiffs("escrow-1", 2, 2)
	require.NoError(t, err)
	require.Len(t, records, 1)

	err = session.ProcessResponse(0, &host.HostResponse{Nonce: 2, StateHash: records[0].PostStateRoot}, 2)

	require.NoError(t, err)
}

func TestSession_RewindReachesHistoryTrimmedFromMemory(t *testing.T) {
	// Test flow:
	// 1. Run a stored session with a two-diff window through ten nonces.
	// 2. Rewind a host whose cursor is at the tip.
	// 3. The host is handed the whole history from nonce 1.
	session, _ := buildLiveSessionWithSmallDiffWindow(t, 10)
	session.mu.Lock()
	session.hostSyncNonce[2] = 10
	session.mu.Unlock()

	require.True(t, session.RewindHostCatchUp(2, "host lost the escrow"))

	session.mu.Lock()
	catchUp := session.diffsForHost(2)
	session.mu.Unlock()
	requireContiguousDiffs(t, catchUp, 1, 10)
}
