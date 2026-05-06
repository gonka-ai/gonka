package devshard

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"decentralized-api/payloadstorage"

	"devshard/bridge"
	"devshard/host"
	devshardserver "devshard/server"
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

func (b *mockBridge) GetEscrow(string) (*bridge.EscrowInfo, error) {
	return b.escrow, nil
}

func (b *mockBridge) GetHostInfo(address string) (*bridge.HostInfo, error) {
	return &bridge.HostInfo{Address: address, URL: "http://localhost"}, nil
}

func (b *mockBridge) VerifyWarmKey(string, string) (bool, error) { return false, nil }

func (b *mockBridge) OnEscrowCreated(bridge.EscrowInfo) error { return bridge.ErrNotImplemented }
func (b *mockBridge) OnSettlementProposed(string, []byte, uint64) error {
	return bridge.ErrNotImplemented
}
func (b *mockBridge) OnSettlementFinalized(string) error { return bridge.ErrNotImplemented }
func (b *mockBridge) SubmitDisputeState(string, []byte, uint64, map[uint32][]byte) error {
	return bridge.ErrNotImplemented
}

var _ bridge.MainnetBridge = (*mockBridge)(nil)

type payloadEntry struct {
	prompt   []byte
	response []byte
}

type mockPayloadStore struct {
	byEpoch map[uint64]map[string]payloadEntry
}

func (m *mockPayloadStore) Store(_ context.Context, inferenceID string, epochID uint64, prompt, response []byte) error {
	if m.byEpoch == nil {
		m.byEpoch = make(map[uint64]map[string]payloadEntry)
	}
	if m.byEpoch[epochID] == nil {
		m.byEpoch[epochID] = make(map[string]payloadEntry)
	}
	m.byEpoch[epochID][inferenceID] = payloadEntry{prompt: prompt, response: response}
	return nil
}

func (m *mockPayloadStore) Retrieve(_ context.Context, inferenceID string, epochID uint64) ([]byte, []byte, error) {
	if entries := m.byEpoch[epochID]; entries != nil {
		if entry, ok := entries[inferenceID]; ok {
			return entry.prompt, entry.response, nil
		}
	}
	return nil, nil, payloadstorage.ErrNotFound
}

func (m *mockPayloadStore) PruneEpoch(context.Context, uint64) error { return nil }

type currentEpochStore struct {
	storage.Storage
	epoch uint64
}

func (s currentEpochStore) CurrentEpochID() uint64 { return s.epoch }

type rangeRecordingStore struct {
	storage.Storage
	from uint64
	to   uint64
}

func (s *rangeRecordingStore) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	s.from = fromNonce
	s.to = toNonce
	return s.Storage.GetDiffs(escrowID, fromNonce, toNonce)
}

type blockingMetaStore struct {
	storage.Storage
	release     <-chan struct{}
	bothStarted chan<- struct{}
	once        sync.Once
	mu          sync.Mutex
	started     int
}

func (s *blockingMetaStore) GetSessionMeta(escrowID string) (*storage.SessionMeta, error) {
	s.mu.Lock()
	s.started++
	if s.started == 2 {
		s.once.Do(func() { close(s.bothStarted) })
	}
	s.mu.Unlock()

	<-s.release
	return s.Storage.GetSessionMeta(escrowID)
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
		EscrowID:       "escrow-1",
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000000,
	}))

	sm, err := state.NewStateMachine("escrow-1", config, group, 100000000, user.Address(), verifier)
	require.NoError(t, err)

	for i := uint64(1); i <= uint64(numDiffs); i++ {
		txs := []*types.DevshardTx{startTx(i)}
		root, err := sm.ApplyLocal(i, txs)
		require.NoError(t, err)

		diff := signDiffWithRoot(t, user, "escrow-1", i, txs, root)
		rec := types.DiffRecord{
			Diff:      diff,
			StateHash: root,
		}
		require.NoError(t, store.AppendDiff("escrow-1", rec))
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
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	signer := hostSigner
	engine := stub.NewInferenceEngine()
	validator := stub.NewValidationEngine()

	mgr := NewHostManager(store, signer, engine, validator, types.LegacySessionVersion, br, nil, nil)
	err := mgr.RecoverSessions()
	require.NoError(t, err)

	mgr.mu.RLock()
	srv, ok := mgr.sessions["escrow-1"]
	mgr.mu.RUnlock()
	require.True(t, ok, "session should exist after recovery")
	require.NotNil(t, srv)
	require.NotNil(t, srv.Host())
}

func TestRecoverSessions_LoadsEscrowsInParallel(t *testing.T) {
	base := newManagerTestStore(t)
	hostSigner := mustGenerateKey(t)
	hosts := []*signing.Secp256k1Signer{
		hostSigner,
		mustGenerateKey(t),
		mustGenerateKey(t),
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	for _, escrowID := range []string{"escrow-a", "escrow-b"} {
		require.NoError(t, base.CreateSession(storage.CreateSessionParams{
			EscrowID:       escrowID,
			CreatorAddr:    user.Address(),
			Config:         defaultConfig(3),
			Group:          group,
			InitialBalance: 100000,
		}))
	}

	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	bothStarted := make(chan struct{})
	store := &blockingMetaStore{
		Storage:     base,
		release:     release,
		bothStarted: bothStarted,
	}
	br := &mockBridge{}
	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), types.LegacySessionVersion, br, nil, nil)

	errCh := make(chan error, 1)
	go func() {
		errCh <- mgr.RecoverSessions()
	}()

	select {
	case <-bothStarted:
	case <-time.After(time.Second):
		t.Fatal("expected at least two sessions to enter recovery concurrently")
	}

	releaseOnce.Do(func() { close(release) })
	require.NoError(t, <-errCh)
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	require.Len(t, mgr.sessions, 2)
}

func TestRecoverSessions_UsesSnapshotBeforeReplay(t *testing.T) {
	store := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, store, 750)
	verifier := signing.NewSecp256k1Verifier()
	config := defaultConfig(3)

	sm, err := state.NewStateMachine("escrow-1", config, group, 100000000, user.Address(), verifier)
	require.NoError(t, err)
	records, err := store.GetDiffs("escrow-1", 1, host.SnapshotInterval)
	require.NoError(t, err)
	for _, rec := range records {
		_, err := sm.ApplyLocal(rec.Nonce, rec.Txs)
		require.NoError(t, err)
	}
	require.NoError(t, saveHostSnapshot(store, sm, "escrow-1", host.SnapshotInterval))
	_, snapshotData, err := store.LoadSnapshot("escrow-1")
	require.NoError(t, err)
	var snapshotEnvelope host.StateSnapshot
	require.NoError(t, json.Unmarshal(snapshotData, &snapshotEnvelope))
	require.NotNil(t, snapshotEnvelope.State)
	require.Nil(t, snapshotEnvelope.HostSyncNonce)

	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}
	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	recording := &rangeRecordingStore{Storage: store}
	mgr := NewHostManager(recording, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), types.LegacySessionVersion, br, nil, nil)
	require.NoError(t, mgr.RecoverSessions())
	require.Equal(t, uint64(host.SnapshotInterval+1), recording.from)
	require.Equal(t, uint64(750), recording.to)

	mgr.mu.RLock()
	srv := mgr.sessions["escrow-1"]
	mgr.mu.RUnlock()
	require.NotNil(t, srv)
	require.Equal(t, uint64(750), srv.Host().SnapshotState().LatestNonce)
}

func TestRecoverSessions_Nonce0(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)

	// Create a session with no diffs (nonce 0).
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
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
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), types.LegacySessionVersion, br, nil, nil)
	err := mgr.RecoverSessions()
	require.NoError(t, err)

	// Session must be registered despite nonce 0.
	mgr.mu.RLock()
	srv, ok := mgr.sessions["escrow-1"]
	mgr.mu.RUnlock()
	require.True(t, ok, "nonce-0 session must be registered after recovery")
	require.NotNil(t, srv)
	require.NotNil(t, srv.Host())

	// Subsequent getOrCreate must return the same session without error.
	srv2, err := mgr.getOrCreate("escrow-1")
	require.NoError(t, err)
	require.Equal(t, srv, srv2)
}

func TestGetOrCreate_RecoversExistingStoredSession(t *testing.T) {
	store := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, store, 3)

	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), types.LegacySessionVersion, br, nil, nil)
	srv, err := mgr.getOrCreate("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(3), srv.Host().LatestNonce(), "existing storage session should be replayed before serving")
}

func TestSessionServer_DefaultsToInitializing(t *testing.T) {
	store := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, store, 1)

	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), types.LegacySessionVersion, br, nil, nil)

	_, err := mgr.SessionServer("escrow-1")
	require.ErrorIs(t, err, devshardserver.ErrInitializing)
}

func TestSessionServer_UnavailableIncludesCause(t *testing.T) {
	store := newManagerTestStore(t)
	mgr := NewHostManager(store, mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), types.LegacySessionVersion, &mockBridge{}, nil, nil)
	mgr.SetUnavailable(errors.New("boom"))

	_, err := mgr.SessionServer("escrow-1")
	require.ErrorIs(t, err, devshardserver.ErrInitializing)
	require.Contains(t, err.Error(), "boom")
}

func TestSessionServer_GatedUntilReady(t *testing.T) {
	store := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, store, 1)

	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), types.LegacySessionVersion, br, nil, nil)
	mgr.SetReady()

	srv, err := mgr.SessionServer("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(1), srv.Host().LatestNonce())
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
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	const standaloneVersion = "v0.2.11"
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), standaloneVersion, br, nil, nil)
	_, err := mgr.getOrCreate("escrow-1")
	require.NoError(t, err)

	meta, err := store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, standaloneVersion, meta.Version)
}

func TestCreateSession_RejectsExistingDifferentVersion(t *testing.T) {
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
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		EpochID:        7,
		Version:        "v1",
		CreatorAddr:    user.Address(),
		Config:         types.SessionConfigWithPrice(len(group), 1),
		Group:          group,
		InitialBalance: 100000,
	}))

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
			EpochID:        7,
			TokenPrice:     1,
		},
	}

	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), "v2", br, nil, nil)
	_, err := mgr.getOrCreate("escrow-1")
	require.ErrorIs(t, err, storage.ErrSessionVersionConflict)
}

func TestRetrievePayloadsFallsBackToChainEscrowEpoch(t *testing.T) {
	store := newManagerTestStore(t)
	payloadStore := &mockPayloadStore{}
	key := devshardserver.PayloadKey("460", 70)
	require.NoError(t, payloadStore.Store(context.Background(), key, 254, []byte("prompt"), []byte("response")))

	mgr := NewHostManager(
		store,
		mustGenerateKey(t),
		stub.NewInferenceEngine(),
		stub.NewValidationEngine(),
		types.LegacySessionVersion,
		&mockBridge{escrow: &bridge.EscrowInfo{EscrowID: "460", EpochID: 254}},
		payloadStore,
		nil,
	)

	prompt, response, epoch, err := mgr.retrievePayloadsWithAdjacentEpochs(context.Background(), "460", "70", 0)
	require.NoError(t, err)
	require.Equal(t, []byte("prompt"), prompt)
	require.Equal(t, []byte("response"), response)
	require.Equal(t, uint64(254), epoch)
}

func TestRetrievePayloadsKeyIncludesEscrowID(t *testing.T) {
	store := newManagerTestStore(t)
	payloadStore := &mockPayloadStore{}
	require.NoError(t, payloadStore.Store(
		context.Background(),
		devshardserver.PayloadKey("other", 70),
		254,
		[]byte("wrong"),
		[]byte("wrong"),
	))

	mgr := NewHostManager(
		store,
		mustGenerateKey(t),
		stub.NewInferenceEngine(),
		stub.NewValidationEngine(),
		types.LegacySessionVersion,
		&mockBridge{escrow: &bridge.EscrowInfo{EscrowID: "460", EpochID: 254}},
		payloadStore,
		nil,
	)

	_, _, _, err := mgr.retrievePayloadsWithAdjacentEpochs(context.Background(), "460", "70", 254)
	require.ErrorIs(t, err, payloadstorage.ErrNotFound)
}

func TestRetrievePayloadsEpochZeroFallsBackToCurrentEpoch(t *testing.T) {
	store := currentEpochStore{Storage: newManagerTestStore(t), epoch: 254}
	payloadStore := &mockPayloadStore{}
	key := devshardserver.PayloadKey("460", 70)
	require.NoError(t, payloadStore.Store(context.Background(), key, 253, []byte("prompt"), []byte("response")))

	mgr := NewHostManager(
		store,
		mustGenerateKey(t),
		stub.NewInferenceEngine(),
		stub.NewValidationEngine(),
		types.LegacySessionVersion,
		&mockBridge{},
		payloadStore,
		nil,
	)

	prompt, response, epoch, err := mgr.retrievePayloadsWithAdjacentEpochs(context.Background(), "460", "70", 0)
	require.NoError(t, err)
	require.Equal(t, []byte("prompt"), prompt)
	require.Equal(t, []byte("response"), response)
	require.Equal(t, uint64(253), epoch)
}

func TestRecoverSessions_EmptyStore(t *testing.T) {
	store := newManagerTestStore(t)
	signer := mustGenerateKey(t)
	br := &mockBridge{}

	mgr := NewHostManager(store, signer, stub.NewInferenceEngine(), stub.NewValidationEngine(), types.LegacySessionVersion, br, nil, nil)
	err := mgr.RecoverSessions()
	require.NoError(t, err)

	mgr.mu.RLock()
	require.Empty(t, mgr.sessions)
	mgr.mu.RUnlock()
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
		EscrowID:       "escrow-1",
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier)
	require.NoError(t, err)

	// Diff 1: correct state hash.
	txs1 := []*types.DevshardTx{startTx(1)}
	root1, err := sm.ApplyLocal(1, txs1)
	require.NoError(t, err)
	diff1 := signDiffWithRoot(t, user, "escrow-1", 1, txs1, root1)
	require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{Diff: diff1, StateHash: root1}))

	// Diff 2: tampered state hash.
	txs2 := []*types.DevshardTx{startTx(2)}
	root2, err := sm.ApplyLocal(2, txs2)
	require.NoError(t, err)
	_ = root2
	diff2 := signDiffWithRoot(t, user, "escrow-1", 2, txs2, []byte("tampered"))
	require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{Diff: diff2, StateHash: []byte("tampered")}))

	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	signer := mustGenerateKey(t)
	mgr := NewHostManager(store, signer, stub.NewInferenceEngine(), stub.NewValidationEngine(), types.LegacySessionVersion, br, nil, nil)
	err = mgr.RecoverSessions()
	// RecoverSessions logs and skips corrupt sessions, does not return error.
	require.NoError(t, err)

	// The corrupt session should NOT be present in the sessions map.
	mgr.mu.RLock()
	_, ok := mgr.sessions["escrow-1"]
	mgr.mu.RUnlock()
	require.False(t, ok, "corrupt session should be skipped, not recovered")
}
