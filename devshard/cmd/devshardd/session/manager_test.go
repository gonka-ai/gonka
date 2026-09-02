package session

import (
	"bytes"
	"fmt"
	"path/filepath"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

// mockBridge implements bridge.MainnetBridge for testing recovery.
type mockBridge struct {
	escrow *bridge.EscrowInfo
}

func (b *mockBridge) GetEscrow(_ string) (*bridge.EscrowInfo, error) {
	return b.escrow, nil
}

func (b *mockBridge) GetHostInfo(address string) (*bridge.HostInfo, error) {
	return &bridge.HostInfo{Address: address, URL: "http://localhost"}, nil
}

func (b *mockBridge) GetValidationThreshold(uint64, string) (*bridge.Decimal, error) {
	return nil, bridge.ErrNotImplemented
}

func (b *mockBridge) VerifyWarmKey(string, string) (bool, error) { return false, nil }

func (b *mockBridge) OnEscrowCreated(bridge.EscrowInfo) error { return bridge.ErrNotImplemented }
func (b *mockBridge) OnSettlementProposed(_ string, _ []byte, _ uint64) error {
	return bridge.ErrNotImplemented
}
func (b *mockBridge) OnSettlementFinalized(_ string) error { return bridge.ErrNotImplemented }
func (b *mockBridge) SubmitDisputeState(_ string, _ []byte, _ uint64, _ map[uint32][]byte) error {
	return bridge.ErrNotImplemented
}

var _ bridge.MainnetBridge = (*mockBridge)(nil)

type countingBridge struct {
	escrow         *bridge.EscrowInfo
	getEscrowCalls int
}

func (b *countingBridge) GetEscrow(string) (*bridge.EscrowInfo, error) {
	b.getEscrowCalls++
	return b.escrow, nil
}

func (b *countingBridge) GetHostInfo(address string) (*bridge.HostInfo, error) {
	return &bridge.HostInfo{Address: address, URL: "http://localhost"}, nil
}

func (b *countingBridge) GetValidationThreshold(uint64, string) (*bridge.Decimal, error) {
	return nil, bridge.ErrNotImplemented
}

func (b *countingBridge) VerifyWarmKey(string, string) (bool, error) { return false, nil }

func (b *countingBridge) OnEscrowCreated(bridge.EscrowInfo) error { return bridge.ErrNotImplemented }
func (b *countingBridge) OnSettlementProposed(string, []byte, uint64) error {
	return bridge.ErrNotImplemented
}
func (b *countingBridge) OnSettlementFinalized(string) error { return bridge.ErrNotImplemented }
func (b *countingBridge) SubmitDisputeState(string, []byte, uint64, map[uint32][]byte) error {
	return bridge.ErrNotImplemented
}

var _ bridge.MainnetBridge = (*countingBridge)(nil)

type failingCreateStore struct {
	storage.Storage
	err error
}

func (s *failingCreateStore) CreateSession(storage.CreateSessionParams) error {
	return s.err
}

func countHostValidationWorkers() int {
	var buf bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
	return bytes.Count(buf.Bytes(), []byte("devshard/host.(*Host).startValidationWorkers.func1"))
}

func mustGenerateKey(t *testing.T) *signing.Secp256k1Signer {
	t.Helper()
	s, err := signing.GenerateKey()
	require.NoError(t, err)
	return s
}

func makeGroup(signers []*signing.Secp256k1Signer) []types.SlotAssignment {
	group := make([]types.SlotAssignment, len(signers))
	for i, s := range signers {
		group[i] = types.SlotAssignment{
			SlotID:           uint32(i),
			ValidatorAddress: s.Address(),
		}
	}
	return group
}

func defaultConfig(n int) types.SessionConfig {
	return types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    uint32(n) / 2,
		ValidationRate:   5000,
	}
}

func startTx(inferenceID uint64) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
		InferenceId: inferenceID,
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   1000,
	}}}
}

func signDiffWithRoot(t *testing.T, signer signing.Signer, escrowID string, nonce uint64, txs []*types.DevshardTx, postStateRoot []byte) types.Diff {
	t.Helper()
	content := &types.DiffContent{Nonce: nonce, Txs: txs, EscrowId: escrowID, PostStateRoot: postStateRoot}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(content)
	require.NoError(t, err)
	sig, err := signer.Sign(data)
	require.NoError(t, err)
	return types.Diff{Nonce: nonce, Txs: txs, UserSig: sig, PostStateRoot: postStateRoot}
}

func newManagerTestStore(t *testing.T) *storage.SQLite {
	t.Helper()
	db, err := storage.NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

// waitObsRepairsOnCleanup keeps a full-replay recovery from racing t.TempDir:
// startObsRepair still holds the SQLite WAL after RecoverSessions returns.
func waitObsRepairsOnCleanup(t *testing.T, mgr *HostManager) *HostManager {
	t.Helper()
	t.Cleanup(mgr.WaitObsRepairs)
	return mgr
}

// populateStore creates a session and appends diffs. Returns group, user signer,
// and the first host signer (for use as HostManager signer -- must be in group).
func populateStore(t *testing.T, store storage.Storage, numDiffs int) ([]types.SlotAssignment, *signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	config := defaultConfig(3)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "1",
		EpochID:        7,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	sm, err := state.NewStateMachine("1", config, group, 100000, user.Address(), verifier, store,
		state.WithVersion(testutil.RuntimeTestVersion))
	require.NoError(t, err)

	for i := uint64(1); i <= uint64(numDiffs); i++ {
		txs := []*types.DevshardTx{startTx(i)}
		root, err := sm.ApplyLocal(i, txs)
		require.NoError(t, err)

		diff := signDiffWithRoot(t, user, "1", i, txs, root)
		rec := types.DiffRecord{
			Diff:      diff,
			StateHash: root,
		}
		require.NoError(t, store.AppendDiff("1", rec))
	}

	return group, user, hosts[0]
}

func TestRecoverSessions_HappyPath(t *testing.T) {
	store := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, store, 10)

	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	mgr := waitObsRepairsOnCleanup(t, NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
	err := mgr.RecoverSessions()
	require.NoError(t, err)

	mgr.sessionsMutex.RLock()
	srv, ok := mgr.sessions["1"]
	mgr.sessionsMutex.RUnlock()
	require.True(t, ok, "session should exist after recovery")
	require.NotNil(t, srv)
	require.NotNil(t, srv.Host())
}

func TestRecoverSessions_Nonce0(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "1",
		EpochID:        7,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         defaultConfig(3),
		Group:          group,
		InitialBalance: 100000,
	}))

	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	mgr := waitObsRepairsOnCleanup(t, NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
	err := mgr.RecoverSessions()
	require.NoError(t, err)

	mgr.sessionsMutex.RLock()
	srv, ok := mgr.sessions["1"]
	mgr.sessionsMutex.RUnlock()
	require.True(t, ok, "nonce-0 session must be registered after recovery")
	require.NotNil(t, srv)
	require.NotNil(t, srv.Host())

	srv2, err := mgr.getOrCreate("1", nil)
	require.NoError(t, err)
	require.Equal(t, srv, srv2)
}

func TestCreateSession_BindsConfiguredVersion(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	const standaloneVersion = "v0.2.11"
	mgr := waitObsRepairsOnCleanup(t, NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, standaloneVersion, br, nil, nil))
	_, err := mgr.getOrCreate("1", nil)
	require.NoError(t, err)

	meta, err := store.GetSessionMeta("1")
	require.NoError(t, err)
	require.Equal(t, standaloneVersion, meta.Version)
}

func TestRecoverSessions_EmptyStore(t *testing.T) {
	store := newManagerTestStore(t)
	signer := mustGenerateKey(t)
	br := &mockBridge{}

	mgr := waitObsRepairsOnCleanup(t, NewHostManager(store, signer, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
	err := mgr.RecoverSessions()
	require.NoError(t, err)

	mgr.sessionsMutex.RLock()
	require.Empty(t, mgr.sessions)
	mgr.sessionsMutex.RUnlock()
}

// TestHandleSettlementFinalized_DoesNotResurrect pins the A3 stale-escrow
// contract: after MarkSettled + drop, getOrCreate must not rebuild a live
// host from the durable settled row (and must negative-cache the miss).
func TestHandleSettlementFinalized_DoesNotResurrect(t *testing.T) {
	store := newManagerTestStore(t)
	slots, user, hostSigner := createStoredSession(t, store, "1", 7, 1)
	addresses := make([]string, len(slots))
	for i, s := range slots {
		addresses[i] = s.ValidatorAddress
	}
	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}
	mgr := waitObsRepairsOnCleanup(t, NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
	require.NoError(t, mgr.RecoverSessions())
	_, ok := mgr.existingServer("1")
	require.True(t, ok, "precondition: session live after recover")

	require.NoError(t, mgr.HandleSettlementFinalized("1"))
	_, ok = mgr.existingServer("1")
	require.False(t, ok, "settlement must drop the live session")

	_, err := mgr.getOrCreate("1", nil)
	require.ErrorIs(t, err, storage.ErrSessionNotActive)

	// Permanent negative cache: a second bind must not re-read/rebuild.
	_, err = mgr.getOrCreate("1", nil)
	require.ErrorIs(t, err, storage.ErrSessionNotActive)
	_, ok = mgr.existingServer("1")
	require.False(t, ok, "settled session must stay out of the live map")
}

// TestRecoverStoredSession_RejectsSettledStatus covers the store-only path:
// even without HandleSettlementFinalized's negative cache, a settled meta row
// must not be rebuilt into a live host.
func TestRecoverStoredSession_RejectsSettledStatus(t *testing.T) {
	store := newManagerTestStore(t)
	_, _, hostSigner := createStoredSession(t, store, "1", 7, 1)
	require.NoError(t, store.MarkSettled("1"))

	mgr := waitObsRepairsOnCleanup(t, NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil))
	_, _, err := mgr.recoverStoredSession("1")
	require.ErrorIs(t, err, storage.ErrSessionNotActive)
}

func TestRecoverSessions_StateRootMismatch(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	config := defaultConfig(3)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "1",
		EpochID:        7,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	sm, err := state.NewStateMachine("1", config, group, 100000, user.Address(), verifier, store,
		state.WithVersion(testutil.RuntimeTestVersion))
	require.NoError(t, err)

	txs1 := []*types.DevshardTx{startTx(1)}
	root1, err := sm.ApplyLocal(1, txs1)
	require.NoError(t, err)
	diff1 := signDiffWithRoot(t, user, "1", 1, txs1, root1)
	require.NoError(t, store.AppendDiff("1", types.DiffRecord{Diff: diff1, StateHash: root1}))

	txs2 := []*types.DevshardTx{startTx(2)}
	_, err = sm.ApplyLocal(2, txs2)
	require.NoError(t, err)
	diff2 := signDiffWithRoot(t, user, "1", 2, txs2, []byte("tampered"))
	require.NoError(t, store.AppendDiff("1", types.DiffRecord{Diff: diff2, StateHash: []byte("tampered")}))

	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	mgr := waitObsRepairsOnCleanup(t, NewHostManager(store, mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
	err = mgr.RecoverSessions()
	require.NoError(t, err)

	mgr.sessionsMutex.RLock()
	_, ok := mgr.sessions["1"]
	mgr.sessionsMutex.RUnlock()
	require.False(t, ok, "corrupt session should be skipped, not recovered")
}

func TestRecoverSessions_SkipsForeignVersionSessions(t *testing.T) {
	store := newManagerTestStore(t)
	_, _, hostSigner := createStoredSessionWithVersion(t, store, "escrow-v2", 7, "foreign", 1)

	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)
	require.NoError(t, mgr.RecoverSessions())

	mgr.sessionsMutex.RLock()
	_, ok := mgr.sessions["escrow-v2"]
	mgr.sessionsMutex.RUnlock()
	require.False(t, ok, "foreign-version session must be skipped, not treated as failed recovery")
}

func TestHostManager_EvictBeforeClosesOldSessions(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	config := defaultConfig(3)
	for _, sess := range []struct {
		escrowID string
		epochID  uint64
	}{
		{escrowID: "escrow-old", epochID: 5},
		{escrowID: "escrow-current", epochID: 7},
	} {
		require.NoError(t, store.CreateSession(storage.CreateSessionParams{
			EscrowID:       sess.escrowID,
			EpochID:        sess.epochID,
			Version:        testutil.RuntimeTestVersion,
			CreatorAddr:    user.Address(),
			Config:         config,
			Group:          group,
			InitialBalance: 100000000,
		}))
	}

	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)
	require.NoError(t, mgr.RecoverSessions())
	t.Cleanup(func() { _ = mgr.Close() })
	beforeEvict := countHostValidationWorkers()
	require.GreaterOrEqual(t, beforeEvict, 2*20)

	evicted := mgr.EvictBefore(6)
	require.Equal(t, 1, evicted)
	require.Eventually(t, func() bool {
		return countHostValidationWorkers() <= beforeEvict-20
	}, time.Second, 10*time.Millisecond)

	mgr.sessionsMutex.RLock()
	_, oldOK := mgr.sessions["escrow-old"]
	_, currentOK := mgr.sessions["escrow-current"]
	mgr.sessionsMutex.RUnlock()
	require.False(t, oldOK)
	require.True(t, currentOK)
}

func TestSessionServer_StartsRegisteredHost(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	addresses := make([]string, len(hosts))
	for i, h := range hosts {
		addresses[i] = h.Address()
	}
	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-start",
			EpochID:        7,
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)

	before := countHostValidationWorkers()
	srv, err := mgr.SessionServer("escrow-start")
	require.NoError(t, err)
	t.Cleanup(srv.Host().Close)
	require.Eventually(t, func() bool {
		return countHostValidationWorkers() >= before+20
	}, time.Second, 10*time.Millisecond)
}

func TestSessionServer_FailedCreateDoesNotStartHost(t *testing.T) {
	base := newManagerTestStore(t)
	store := &failingCreateStore{Storage: base, err: storage.ErrEpochPruned}
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	addresses := make([]string, len(hosts))
	for i, h := range hosts {
		addresses[i] = h.Address()
	}
	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-pruned",
			EpochID:        1,
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)

	before := countHostValidationWorkers()
	_, err := mgr.SessionServer("escrow-pruned")
	require.ErrorIs(t, err, storage.ErrEpochPruned)
	require.Equal(t, before, countHostValidationWorkers())
}

func TestSessionServer_CachesFailedResolution(t *testing.T) {
	base := newManagerTestStore(t)
	store := &failingCreateStore{Storage: base, err: storage.ErrEpochPruned}
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	addresses := make([]string, len(hosts))
	for i, h := range hosts {
		addresses[i] = h.Address()
	}
	br := &countingBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-cache-failure",
			EpochID:        1,
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)

	_, err := mgr.SessionServer("escrow-cache-failure")
	require.ErrorIs(t, err, storage.ErrEpochPruned)
	_, err = mgr.SessionServer("escrow-cache-failure")
	require.ErrorIs(t, err, storage.ErrEpochPruned)
	require.Equal(t, 1, br.getEscrowCalls, "cached failure should avoid rebuilding the same broken escrow")
}

func TestHostManager_SweepsExpiredResolutionFailures(t *testing.T) {
	mgr := &HostManager{
		resolutionFailures: make(map[string]resolutionFailure),
	}
	now := time.Unix(1_000, 0)
	for i := 0; i <= maxResolutionFailures; i++ {
		mgr.resolutionFailures[fmt.Sprintf("expired-%d", i)] = resolutionFailure{
			err:       storage.ErrSessionNotFound,
			expiresAt: now.Add(-time.Second),
		}
	}

	mgr.rememberResolutionFailure("fresh", storage.ErrEpochPruned, now)

	require.Len(t, mgr.resolutionFailures, 1)
	require.Contains(t, mgr.resolutionFailures, "fresh")
	require.True(t, now.Before(mgr.resolutionFailures["fresh"].expiresAt))
}
