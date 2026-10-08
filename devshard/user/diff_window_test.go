package user

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/types"
)

const bareHistoryLength = 3 * defaultDiffsKeptInMemory

// diffRecordingClient acknowledges every request at its nonce and keeps the diffs it was sent.
type diffRecordingClient struct {
	mu       sync.Mutex
	received []types.Diff
}

func (client *diffRecordingClient) Send(_ context.Context, request host.HostRequest, _ io.Writer, _ func(*host.HostResponse)) (*host.HostResponse, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.received = append(client.received, request.Diffs...)
	return &host.HostResponse{Nonce: request.Nonce}, nil
}

func (client *diffRecordingClient) receivedDiffs() []types.Diff {
	client.mu.Lock()
	defer client.mu.Unlock()
	return append([]types.Diff(nil), client.received...)
}

func recoverOverBareHistory(t *testing.T, store storage.Storage, rawStore storage.Storage, hostCursors map[int]uint64) *Session {
	t.Helper()
	const hostCount = 3
	hosts := make([]*signing.Secp256k1Signer, hostCount)
	for hostIdx := range hosts {
		hosts[hostIdx] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(hostCount)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, rawStore.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))
	machine := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	snapshotRoot, err := machine.ComputeStateRoot()
	require.NoError(t, err)
	for nonce := uint64(1); nonce <= bareHistoryLength; nonce++ {
		record := types.DiffRecord{Diff: types.Diff{Nonce: nonce}}
		if nonce == bareHistoryLength && hostCursors != nil {
			record.StateHash = snapshotRoot
		}
		require.NoError(t, rawStore.AppendDiff("escrow-1", record))
	}
	if hostCursors != nil {
		saveSnapshot(rawStore, machine, "escrow-1", bareHistoryLength, hostCursors)
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
		require.Equal(t, firstNonce+uint64(offset), diff.Nonce, "diff %d nonce = %d, want %d", offset, diff.Nonce, firstNonce+uint64(offset))
	}
}

func buildLiveSessionWithSmallDiffWindow(t *testing.T, diffCount int) (*Session, storage.Storage) {
	t.Helper()
	store := newTestStore(t)
	session, _, _, _, _ := buildLiveSession(t, 3, store)
	session.diffsKeptInMemory = 2
	for range diffCount {
		_, err := session.PrepareInference(storedCatchUpInference())
		require.NoError(t, err)
	}
	require.Equal(t, uint64(diffCount), session.Nonce())
	return session, store
}

// catchUpThroughRecorder closes a host's stored gap through a recording client and returns every diff it
// would be taught: what the store sent, then what memory still holds past the cursor.
func catchUpThroughRecorder(t *testing.T, session *Session, hostIdx int) []types.Diff {
	t.Helper()
	recorder := &diffRecordingClient{}
	require.NoError(t, session.closeStoredGap(context.Background(), hostIdx, recorder))
	session.mu.Lock()
	defer session.mu.Unlock()
	return append(recorder.receivedDiffs(), session.diffsForHost(hostIdx)...)
}

// Test flow:
//  1. Store a history three pages long with a snapshot at its tip.
//  2. Recover the session through a store that records every diff read.
//  3. No single read may span more than one page.
func TestRecoverSession_ReadsHistoryInPages(t *testing.T) {
	rawStore := newTestStore(t)
	spy := &replaySpyStore{Storage: rawStore}
	upToDate := map[int]uint64{0: bareHistoryLength, 1: bareHistoryLength, 2: bareHistoryLength}

	recoverOverBareHistory(t, spy, rawStore, upToDate)

	require.NotEmpty(t, spy.calls)
	for _, call := range spy.calls {
		require.LessOrEqual(t, call.to-call.from+1, diffPageSize, "read %d..%d spans more than one page", call.from, call.to)
	}
}

// Test flow:
//  1. Store a history three pages long and no snapshot.
//  2. Recover the session through a store that records every diff read, so it replays from nonce 1.
//  3. No single read may span more than one page.
func TestRecoverSession_ReplaysHistoryWithoutSnapshotInPages(t *testing.T) {
	rawStore := newTestStore(t)
	spy := &replaySpyStore{Storage: rawStore}

	session := recoverOverBareHistory(t, spy, rawStore, nil)

	require.Equal(t, uint64(bareHistoryLength), session.Nonce())
	require.NotEmpty(t, spy.calls)
	for _, call := range spy.calls {
		require.LessOrEqual(t, call.to-call.from+1, diffPageSize, "read %d..%d spans more than one page", call.from, call.to)
	}
}

// Test flow:
//  1. Store a long history with a snapshot at its tip and host 0 stranded at nonce 10.
//  2. Recover the session.
//  3. Memory holds less than twice the diff window.
//  4. Host 0 is still taught every diff from nonce 11 to the tip, the older part from the store.
func TestRecoverSession_StrandedHostIsServedFromStoreNotMemory(t *testing.T) {
	rawStore := newTestStore(t)
	strandedHost := map[int]uint64{0: 10, 1: bareHistoryLength, 2: bareHistoryLength}

	session := recoverOverBareHistory(t, rawStore, rawStore, strandedHost)

	require.Less(t, len(session.Diffs()), 2*session.diffsKeptInMemory)
	requireContiguousDiffs(t, catchUpThroughRecorder(t, session, 0), 11, bareHistoryLength)
}

// Test flow:
//  1. Run a stored session with a two-diff window through ten nonces.
//  2. Memory holds less than twice the window.
//  3. A host whose cursor is at nonce 1 is taught every diff from 2 to 10.
func TestSession_TrimsDiffsInMemoryAndServesLaggingHostFromStore(t *testing.T) {
	session, _ := buildLiveSessionWithSmallDiffWindow(t, 10)

	require.Less(t, len(session.Diffs()), 2*session.diffsKeptInMemory)
	session.mu.Lock()
	session.hostSyncNonce[2] = 1
	session.mu.Unlock()
	requireContiguousDiffs(t, catchUpThroughRecorder(t, session, 2), 2, 10)
}

// Test flow:
//  1. Run a stored session with a two-diff window through ten nonces.
//  2. Take the stored post-state root of nonce 2, which memory no longer holds.
//  3. A host response at nonce 2 carrying that root is accepted.
func TestSession_VerifiesStateHashForNonceTrimmedFromMemory(t *testing.T) {
	session, store := buildLiveSessionWithSmallDiffWindow(t, 10)
	records, err := store.GetDiffs("escrow-1", 2, 2)
	require.NoError(t, err)
	require.Len(t, records, 1)

	err = session.ProcessResponse(0, &host.HostResponse{Nonce: 2, StateHash: records[0].PostStateRoot}, 2)

	require.NoError(t, err)
}

// Test flow:
//  1. Run a stored session with a two-diff window through ten nonces.
//  2. Rewind a host whose cursor is at the tip.
//  3. The host is taught the whole history from nonce 1, the trimmed part from the store.
func TestSession_RewindReachesHistoryTrimmedFromMemory(t *testing.T) {
	session, _ := buildLiveSessionWithSmallDiffWindow(t, 10)
	session.mu.Lock()
	session.hostSyncNonce[2] = 10
	session.mu.Unlock()

	require.True(t, session.RewindHostCatchUp(2, "host lost the escrow"))

	requireContiguousDiffs(t, catchUpThroughRecorder(t, session, 2), 1, 10)
}
