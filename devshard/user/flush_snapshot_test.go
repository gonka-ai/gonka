package user

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

// replaySpyStore wraps a storage.Storage and records every GetDiffs range so a
// test can distinguish diff replay from catch-up backfill. During recovery,
// replayed diffs (fed through ApplyLocal to rebuild SM state) come from a
// GetDiffs call whose range starts after the snapshot nonce; backfill calls
// end at the snapshot nonce.
type replaySpyStore struct {
	storage.Storage
	mu    sync.Mutex
	calls []spyGetDiffs
}

type spyGetDiffs struct {
	from, to uint64
	count    int
}

func (s *replaySpyStore) GetDiffs(escrowID string, from, to uint64) ([]types.DiffRecord, error) {
	recs, err := s.Storage.GetDiffs(escrowID, from, to)
	s.mu.Lock()
	s.calls = append(s.calls, spyGetDiffs{from: from, to: to, count: len(recs)})
	s.mu.Unlock()
	return recs, err
}

// replayedRecords sums records returned for calls whose range starts strictly
// after snapNonce -- exactly the post-snapshot diffs RecoverSession replays.
func (s *replaySpyStore) replayedRecords(snapNonce uint64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, c := range s.calls {
		if c.from > snapNonce {
			total += c.count
		}
	}
	return total
}

// buildLiveSession builds a storage-backed session and its state machine,
// mirroring setupRecoverableSession but returning the live session so a test
// can drive it and then flush a snapshot.
func buildLiveSession(
	t *testing.T, numHosts int, store storage.Storage,
) (*Session, *state.StateMachine, []types.SlotAssignment, []*signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        types.SessionVersionV1,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	clients := make([]HostClient, numHosts)
	for i := range hosts {
		hostStore := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000)
		sm, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier, hostStore)
		require.NoError(t, err)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(10))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	userSM, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier, store)
	require.NoError(t, err)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier, WithStorage(store))
	require.NoError(t, err)

	return session, userSM, group, hosts, user
}

// TestFlushSnapshot_RetiredEscrowRebuildsWithoutReplay is the core guarantee of
// the retire-time snapshot flush: an escrow whose nonce advanced past the last
// periodic snapshot (here: none, since numInferences < snapshotInterval) must,
// after FlushSnapshot, rebuild via RecoverSession with zero diff replay.
func TestFlushSnapshot_RetiredEscrowRebuildsWithoutReplay(t *testing.T) {
	store := newTestStore(t)
	numHosts := 3
	numInferences := 5 // < snapshotInterval, so no periodic snapshot is taken

	session, liveSM, group, hosts, user := buildLiveSession(t, numHosts, store)

	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}
	for i := 0; i < numInferences; i++ {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}
	require.Equal(t, uint64(numInferences), session.Nonce())

	// No periodic snapshot exists yet: a plain rebuild here would replay all
	// numInferences diffs.
	_, _, err := store.LoadSnapshot("escrow-1")
	require.ErrorIs(t, err, storage.ErrSnapshotNotFound)

	// Retire: flush a final snapshot at the current (frozen) nonce.
	require.NoError(t, session.FlushSnapshot())

	snapNonce, _, err := store.LoadSnapshot("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(numInferences), snapNonce, "flush must snapshot at the frozen nonce")

	// Rebuild through a spy to observe replay. With a snapshot at LatestNonce,
	// RecoverSession must take the early-return path and never fetch (let alone
	// replay) any post-snapshot diff.
	verifier := signing.NewSecp256k1Verifier()
	spy := &replaySpyStore{Storage: store}
	rec, recSM, err := RecoverSession(spy, user, verifier, "escrow-1", types.SessionVersionV1, group, buildRecoveryClients(t, hosts, group, user))
	require.NoError(t, err)
	require.Equal(t, uint64(numInferences), rec.Nonce())
	require.Zero(t, spy.replayedRecords(snapNonce), "retired escrow must rebuild with zero diff replay")

	// The rebuilt state must be identical to the live session's state.
	recRoot, err := recSM.ComputeStateRoot()
	require.NoError(t, err)
	liveRoot, err := liveSM.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, liveRoot, recRoot, "flushed snapshot must reproduce the live state root")
}

// TestFlushSnapshot_NoStoreOrEmptyIsNoop verifies FlushSnapshot is safe on
// sessions with nothing to persist: no store configured, or nonce still 0.
func TestFlushSnapshot_NoStoreOrEmptyIsNoop(t *testing.T) {
	// nonce == 0 (no inferences) with a store: must not write a snapshot.
	store := newTestStore(t)
	session, _, _, _, _ := buildLiveSession(t, 3, store)
	require.Equal(t, uint64(0), session.Nonce())
	require.NoError(t, session.FlushSnapshot())
	_, _, err := store.LoadSnapshot("escrow-1")
	require.ErrorIs(t, err, storage.ErrSnapshotNotFound, "flush at nonce 0 must not write a snapshot")

	// No store configured: flush is a no-op that returns nil.
	verifier := signing.NewSecp256k1Verifier()
	hostSigner := testutil.MustGenerateKey(t)
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostSigner})
	config := testutil.DefaultConfig(1)
	hostStore := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000)
	hostSM, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier, hostStore)
	require.NoError(t, err)
	h, err := host.NewHost(hostSM, hostSigner, stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(10))
	require.NoError(t, err)
	userStore := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000)
	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier, userStore)
	require.NoError(t, err)
	noStore, err := NewSession(sm, user, "escrow-1", group, []HostClient{&InProcessClient{Host: h}}, verifier)
	require.NoError(t, err)
	require.NoError(t, noStore.FlushSnapshot())
}
