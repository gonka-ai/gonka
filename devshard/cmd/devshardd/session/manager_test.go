package session

import (
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devshard/bridge"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
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

func mustGenerateKey(t testing.TB) *signing.Secp256k1Signer {
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
		MaxTokens:   testutil.TestMaxTokens,
		StartedAt:   1000,
	}}}
}

func signDiffWithRoot(t testing.TB, signer signing.Signer, escrowID string, nonce uint64, txs []*types.DevshardTx, postStateRoot []byte) types.Diff {
	t.Helper()
	content := &types.DiffContent{Nonce: nonce, Txs: txs, EscrowId: escrowID, PostStateRoot: postStateRoot}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(content)
	require.NoError(t, err)
	sig, err := signer.Sign(data)
	require.NoError(t, err)
	return types.Diff{Nonce: nonce, Txs: txs, UserSig: sig, PostStateRoot: postStateRoot}
}

func newManagerTestStore(t testing.TB) *storage.SQLite {
	t.Helper()
	db, err := storage.NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

// populateStore creates a session and appends diffs. Returns group, user signer,
// and the first host signer (for use as HostManager signer -- must be in group).
func populateStore(t testing.TB, store storage.Storage, numDiffs int) ([]types.SlotAssignment, *signing.Secp256k1Signer, *signing.Secp256k1Signer) {
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

type diffRange struct {
	from uint64
	to   uint64
}

type recordingStorage struct {
	storage.Storage
	mu     sync.Mutex
	ranges []diffRange
}

type concurrencyRecordingStorage struct {
	storage.Storage
	active atomic.Int32
	max    atomic.Int32
}

type snapshotHidingStorage struct {
	storage.Storage
}

type truncatingStorage struct {
	storage.Storage
}

type rootlessStorage struct {
	storage.Storage
}

type boundaryRootStorage struct {
	storage.Storage
	nonce uint64
	root  []byte
}

func (s snapshotHidingStorage) LoadSnapshot(string) (uint64, []byte, error) {
	return 0, nil, storage.ErrSnapshotNotFound
}

func (s truncatingStorage) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	if fromNonce < toNonce {
		toNonce--
	}
	return s.Storage.GetDiffs(escrowID, fromNonce, toNonce)
}

func (s rootlessStorage) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	records, err := s.Storage.GetDiffs(escrowID, fromNonce, toNonce)
	if err != nil {
		return nil, err
	}
	for i := range records {
		records[i].StateHash = nil
		records[i].PostStateRoot = nil
	}
	return records, nil
}

func (s boundaryRootStorage) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	records, err := s.Storage.GetDiffs(escrowID, fromNonce, toNonce)
	if err != nil {
		return nil, err
	}
	for i := range records {
		if records[i].Nonce == s.nonce {
			records[i].StateHash = append([]byte(nil), s.root...)
		}
	}
	return records, nil
}

func (s *concurrencyRecordingStorage) GetSessionMeta(escrowID string) (*storage.SessionMeta, error) {
	active := s.active.Add(1)
	for {
		maximum := s.max.Load()
		if active <= maximum || s.max.CompareAndSwap(maximum, active) {
			break
		}
	}
	defer s.active.Add(-1)
	time.Sleep(10 * time.Millisecond)
	return s.Storage.GetSessionMeta(escrowID)
}

func (s *recordingStorage) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	s.mu.Lock()
	s.ranges = append(s.ranges, diffRange{from: fromNonce, to: toNonce})
	s.mu.Unlock()
	return s.Storage.GetDiffs(escrowID, fromNonce, toNonce)
}

func (s *recordingStorage) diffRanges() []diffRange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]diffRange(nil), s.ranges...)
}

func populateStoreWithSnapshot(
	t testing.TB,
	store storage.Storage,
	snapshotNonce uint64,
	latestNonce uint64,
) ([]types.SlotAssignment, *signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	config := defaultConfig(len(group))
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

	for nonce := uint64(1); nonce <= latestNonce; nonce++ {
		txs := []*types.DevshardTx{startTx(nonce)}
		root, err := sm.ApplyLocal(nonce, txs)
		require.NoError(t, err)
		require.NoError(t, store.AppendDiff("1", types.DiffRecord{
			Diff:      signDiffWithRoot(t, user, "1", nonce, txs, root),
			StateHash: root,
		}))
		if nonce != snapshotNonce {
			continue
		}
		snapshot, err := host.MarshalStateSnapshotWithCommitted(
			sm.ExportState(),
			sm.ExportCommittedEntries(),
			sm.ExportSealedNonces(),
		)
		require.NoError(t, err)
		require.NoError(t, store.SaveSnapshot("1", nonce, snapshot))
	}

	return group, user, hosts[0]
}

func BenchmarkRecoverStoredSession(b *testing.B) {
	for _, benchmark := range []struct {
		name         string
		hideSnapshot bool
	}{
		{name: "full_history", hideSnapshot: true},
		{name: "snapshot_tail"},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			for range b.N {
				b.StopTimer()
				base := newManagerTestStore(b)
				group, user, hostSigner := populateStoreWithSnapshot(b, base, 500, 600)
				var store storage.Storage = base
				if benchmark.hideSnapshot {
					store = snapshotHidingStorage{Storage: base}
				}
				addresses := make([]string, len(group))
				for i, slot := range group {
					addresses[i] = slot.ValidatorAddress
				}
				manager := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
					testutil.RuntimeTestVersion, &mockBridge{escrow: &bridge.EscrowInfo{
						EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
					}}, nil, nil)
				b.StartTimer()
				server, err := manager.recoverStoredSession("1")
				require.NoError(b, err)
				server.Host().Close()
			}
		})
	}
}

func BenchmarkRecoverStoredSessionSealedSnapshot(b *testing.B) {
	const sealedCount = 10_000
	for range b.N {
		b.StopTimer()
		base := newManagerTestStore(b)
		hosts := []*signing.Secp256k1Signer{
			mustGenerateKey(b),
			mustGenerateKey(b),
			mustGenerateKey(b),
		}
		user := mustGenerateKey(b)
		group := makeGroup(hosts)
		config := defaultConfig(len(group))
		require.NoError(b, base.CreateSession(storage.CreateSessionParams{
			EscrowID:       "1",
			EpochID:        7,
			Version:        testutil.RuntimeTestVersion,
			CreatorAddr:    user.Address(),
			Config:         config,
			Group:          group,
			InitialBalance: 100000,
		}))
		machine, err := state.NewStateMachine(
			"1", config, group, 100000, user.Address(), signing.NewSecp256k1Verifier(), base,
			state.WithVersion(testutil.RuntimeTestVersion),
		)
		require.NoError(b, err)
		snapshotState := machine.ExportState()
		snapshotState.LatestNonce = sealedCount
		sealedNonces := make(map[uint64]uint64, sealedCount)
		for id := uint64(1); id <= sealedCount; id++ {
			sealedNonces[id] = id
		}
		machine.RestoreState(snapshotState)
		machine.RestoreSealedNonces(sealedNonces)
		root, err := machine.ComputeStateRoot()
		require.NoError(b, err)
		require.NoError(b, base.AppendDiff("1", types.DiffRecord{
			Diff:      types.Diff{Nonce: sealedCount},
			StateHash: root,
		}))
		snapshot, err := host.MarshalStateSnapshotWithCommitted(snapshotState, nil, sealedNonces)
		require.NoError(b, err)
		require.NoError(b, base.SaveSnapshot("1", sealedCount, snapshot))

		manager := NewHostManager(base, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
			testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)
		b.StartTimer()
		server, err := manager.recoverStoredSession("1")
		require.NoError(b, err)
		server.Host().Close()
	}
}

func TestRecoverSessions_UsesSnapshotTail(t *testing.T) {
	base := newManagerTestStore(t)
	group, user, hostSigner := populateStoreWithSnapshot(t, base, 2, 4)
	expectedRecords, err := base.GetDiffs("1", 4, 4)
	require.NoError(t, err)
	require.Len(t, expectedRecords, 1)
	store := &recordingStorage{Storage: base}
	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	manager := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{escrow: &bridge.EscrowInfo{
			EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
		}}, nil, nil)

	require.NoError(t, manager.RecoverSessions())
	require.Equal(t, []diffRange{{from: 2, to: 2}, {from: 3, to: 4}}, store.diffRanges())

	srv, ok := manager.existingServer("1")
	require.True(t, ok)
	require.Equal(t, uint64(4), srv.Host().LatestNonce())
	root, err := srv.Host().StateRoot()
	require.NoError(t, err)
	require.Equal(t, expectedRecords[0].StateHash, root)
}

func TestRecoverSessions_SnapshotAtLatestSkipsTailReplay(t *testing.T) {
	base := newManagerTestStore(t)
	group, user, hostSigner := populateStoreWithSnapshot(t, base, 4, 4)
	store := &recordingStorage{Storage: base}
	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	manager := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{escrow: &bridge.EscrowInfo{
			EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
		}}, nil, nil)

	require.NoError(t, manager.RecoverSessions())
	require.Equal(t, []diffRange{{from: 4, to: 4}}, store.diffRanges())
	srv, ok := manager.existingServer("1")
	require.True(t, ok)
	require.Equal(t, uint64(4), srv.Host().LatestNonce())
}

func TestRecoverSessions_MissingSnapshotReplaysAllAndSavesReplacement(t *testing.T) {
	base := newManagerTestStore(t)
	group, user, hostSigner := populateStoreWithSnapshot(t, base, 0, 4)
	store := &recordingStorage{Storage: base}
	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	manager := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{escrow: &bridge.EscrowInfo{
			EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
		}}, nil, nil)

	require.NoError(t, manager.RecoverSessions())
	require.Equal(t, []diffRange{{from: 1, to: 4}}, store.diffRanges())
	snapshotNonce, snapshotData, err := base.LoadSnapshot("1")
	require.NoError(t, err)
	require.Equal(t, uint64(4), snapshotNonce)
	_, _, _, err = host.UnmarshalStateSnapshotWithCommitted(snapshotData)
	require.NoError(t, err)
}

func TestRecoverSessions_CorruptSnapshotFallsBackToFullReplay(t *testing.T) {
	base := newManagerTestStore(t)
	group, user, hostSigner := populateStoreWithSnapshot(t, base, 0, 4)
	require.NoError(t, base.SaveSnapshot("1", 2, []byte("not a snapshot")))
	store := &recordingStorage{Storage: base}
	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	manager := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{escrow: &bridge.EscrowInfo{
			EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
		}}, nil, nil)

	require.NoError(t, manager.RecoverSessions())
	require.Equal(t, []diffRange{{from: 1, to: 4}}, store.diffRanges())
}

func TestRecoverSessions_InconsistentSnapshotFallsBackToFullReplay(t *testing.T) {
	base := newManagerTestStore(t)
	group, user, hostSigner := populateStoreWithSnapshot(t, base, 2, 4)
	snapshotNonce, snapshotData, err := base.LoadSnapshot("1")
	require.NoError(t, err)
	snapshotState, committedEntries, sealedNonces, err :=
		host.UnmarshalStateSnapshotWithCommitted(snapshotData)
	require.NoError(t, err)
	snapshotState.LatestNonce = snapshotNonce - 1
	snapshotData, err = host.MarshalStateSnapshotWithCommitted(snapshotState, committedEntries, sealedNonces)
	require.NoError(t, err)
	require.NoError(t, base.SaveSnapshot("1", snapshotNonce, snapshotData))

	store := &recordingStorage{Storage: base}
	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	manager := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{escrow: &bridge.EscrowInfo{
			EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
		}}, nil, nil)

	require.NoError(t, manager.RecoverSessions())
	require.Equal(t, []diffRange{{from: 1, to: 4}}, store.diffRanges())
}

func TestRecoverSessions_SnapshotMetadataMismatchFallsBackToFullReplay(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*types.EscrowState)
	}{
		{name: "escrow", mutate: func(snapshot *types.EscrowState) {
			snapshot.EscrowID = "other"
		}},
		{name: "version", mutate: func(snapshot *types.EscrowState) {
			snapshot.StateRootAndProtocolVersion = "other"
		}},
		{name: "config", mutate: func(snapshot *types.EscrowState) {
			snapshot.Config.TokenPrice++
		}},
		{name: "group", mutate: func(snapshot *types.EscrowState) {
			snapshot.Group[0].ValidatorAddress = snapshot.Group[1].ValidatorAddress
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := newManagerTestStore(t)
			group, user, hostSigner := populateStoreWithSnapshot(t, base, 2, 4)
			snapshotNonce, snapshotData, err := base.LoadSnapshot("1")
			require.NoError(t, err)
			snapshotState, committedEntries, sealedNonces, err :=
				host.UnmarshalStateSnapshotWithCommitted(snapshotData)
			require.NoError(t, err)
			test.mutate(snapshotState)
			snapshotData, err = host.MarshalStateSnapshotWithCommitted(snapshotState, committedEntries, sealedNonces)
			require.NoError(t, err)
			require.NoError(t, base.SaveSnapshot("1", snapshotNonce, snapshotData))

			store := &recordingStorage{Storage: base}
			addresses := make([]string, len(group))
			for i, slot := range group {
				addresses[i] = slot.ValidatorAddress
			}
			manager := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
				testutil.RuntimeTestVersion, &mockBridge{escrow: &bridge.EscrowInfo{
					EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
				}}, nil, nil)

			require.NoError(t, manager.RecoverSessions())
			require.Equal(t, []diffRange{{from: 1, to: 4}}, store.diffRanges())
		})
	}
}

func TestRecoverSessions_SnapshotRootMismatchFallsBackToFullReplay(t *testing.T) {
	base := newManagerTestStore(t)
	group, user, hostSigner := populateStoreWithSnapshot(t, base, 2, 4)
	snapshotNonce, snapshotData, err := base.LoadSnapshot("1")
	require.NoError(t, err)
	snapshotState, committedEntries, sealedNonces, err :=
		host.UnmarshalStateSnapshotWithCommitted(snapshotData)
	require.NoError(t, err)
	snapshotState.Balance++
	snapshotData, err = host.MarshalStateSnapshotWithCommitted(snapshotState, committedEntries, sealedNonces)
	require.NoError(t, err)
	require.NoError(t, base.SaveSnapshot("1", snapshotNonce, snapshotData))

	store := &recordingStorage{Storage: base}
	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	manager := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{escrow: &bridge.EscrowInfo{
			EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
		}}, nil, nil)

	require.NoError(t, manager.RecoverSessions())
	require.Equal(t, []diffRange{{from: 2, to: 2}, {from: 1, to: 4}}, store.diffRanges())
}

func TestRecoverSessions_InvalidSnapshotIndexesFallBackToFullReplay(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[uint64][]byte, map[uint64]uint64)
	}{
		{name: "committed entry is not live", mutate: func(committed map[uint64][]byte, _ map[uint64]uint64) {
			committed[99] = []byte("unexpected")
		}},
		{name: "sealed inference follows seal nonce", mutate: func(_ map[uint64][]byte, sealed map[uint64]uint64) {
			sealed[99] = 2
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := newManagerTestStore(t)
			group, user, hostSigner := populateStoreWithSnapshot(t, base, 2, 4)
			snapshotNonce, snapshotData, err := base.LoadSnapshot("1")
			require.NoError(t, err)
			snapshotState, committedEntries, sealedNonces, err :=
				host.UnmarshalStateSnapshotWithCommitted(snapshotData)
			require.NoError(t, err)
			if committedEntries == nil {
				committedEntries = make(map[uint64][]byte)
			}
			if sealedNonces == nil {
				sealedNonces = make(map[uint64]uint64)
			}
			test.mutate(committedEntries, sealedNonces)
			snapshotData, err = host.MarshalStateSnapshotWithCommitted(snapshotState, committedEntries, sealedNonces)
			require.NoError(t, err)
			require.NoError(t, base.SaveSnapshot("1", snapshotNonce, snapshotData))

			store := &recordingStorage{Storage: base}
			addresses := make([]string, len(group))
			for i, slot := range group {
				addresses[i] = slot.ValidatorAddress
			}
			manager := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
				testutil.RuntimeTestVersion, &mockBridge{escrow: &bridge.EscrowInfo{
					EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
				}}, nil, nil)

			require.NoError(t, manager.RecoverSessions())
			require.Equal(t, []diffRange{{from: 1, to: 4}}, store.diffRanges())
		})
	}
}

func TestRecoverSessions_SnapshotDoesNotRequireSealedObservabilityRows(t *testing.T) {
	base := newManagerTestStore(t)
	group, user, hostSigner := populateStoreWithSnapshot(t, base, 2, 2)
	snapshotNonce, snapshotData, err := base.LoadSnapshot("1")
	require.NoError(t, err)
	snapshotState, committedEntries, sealedNonces, err :=
		host.UnmarshalStateSnapshotWithCommitted(snapshotData)
	require.NoError(t, err)

	var accumulator [32]byte
	copy(accumulator[:], snapshotState.SealedAcc)
	accumulator = state.FoldSealedAccumulator(accumulator, snapshotNonce, 1, committedEntries[1])
	snapshotState.SealedAcc = append([]byte(nil), accumulator[:]...)
	delete(snapshotState.Inferences, 1)
	delete(committedEntries, 1)
	if sealedNonces == nil {
		sealedNonces = make(map[uint64]uint64)
	}
	sealedNonces[1] = snapshotNonce

	machine, err := state.NewStateMachine(
		"1", snapshotState.Config, snapshotState.Group, 100000, user.Address(),
		signing.NewSecp256k1Verifier(), base, state.WithVersion(testutil.RuntimeTestVersion),
	)
	require.NoError(t, err)
	machine.RestoreState(snapshotState)
	machine.RestoreCommittedEntries(committedEntries)
	machine.RestoreSealedNonces(sealedNonces)
	snapshotRoot, err := machine.ComputeStateRoot()
	require.NoError(t, err)
	snapshotData, err = host.MarshalStateSnapshotWithCommitted(snapshotState, committedEntries, sealedNonces)
	require.NoError(t, err)
	require.NoError(t, base.SaveSnapshot("1", snapshotNonce, snapshotData))

	store := boundaryRootStorage{Storage: base, nonce: snapshotNonce, root: snapshotRoot}
	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	manager := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{escrow: &bridge.EscrowInfo{
			EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
		}}, nil, nil)

	require.NoError(t, manager.RecoverSessions())
	_, found, err := base.GetSealedInference("1", 1)
	require.NoError(t, err)
	require.False(t, found)
	server, ok := manager.existingServer("1")
	require.True(t, ok)
	require.Equal(t, snapshotNonce, server.Host().LatestNonce())
}

func TestRecoverSessions_FutureSnapshotFallsBackToFullReplay(t *testing.T) {
	base := newManagerTestStore(t)
	group, user, hostSigner := populateStoreWithSnapshot(t, base, 4, 4)
	_, snapshotData, err := base.LoadSnapshot("1")
	require.NoError(t, err)
	require.NoError(t, base.SaveSnapshot("1", 5, snapshotData))
	store := &recordingStorage{Storage: base}
	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	manager := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{escrow: &bridge.EscrowInfo{
			EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
		}}, nil, nil)

	require.NoError(t, manager.RecoverSessions())
	require.Equal(t, []diffRange{{from: 1, to: 4}}, store.diffRanges())
}

func TestRecoverStoredSession_RejectsIncompleteTail(t *testing.T) {
	base := newManagerTestStore(t)
	group, user, hostSigner := populateStoreWithSnapshot(t, base, 2, 4)
	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	manager := NewHostManager(truncatingStorage{Storage: base}, hostSigner,
		stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{escrow: &bridge.EscrowInfo{
			EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
		}}, nil, nil)

	_, err := manager.recoverStoredSession("1")
	require.ErrorContains(t, err, "missing trailing nonces 4..4")
}

func TestRecoverStoredSession_AllowsLegacyRootlessDiffs(t *testing.T) {
	base := newManagerTestStore(t)
	group, user, hostSigner := populateStoreWithSnapshot(t, base, 0, 4)
	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	manager := NewHostManager(rootlessStorage{Storage: base}, hostSigner,
		stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{escrow: &bridge.EscrowInfo{
			EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
		}}, nil, nil)

	server, err := manager.recoverStoredSession("1")
	require.NoError(t, err)
	require.Equal(t, uint64(4), server.Host().LatestNonce())
	server.Host().Close()

	store := &recordingStorage{Storage: rootlessStorage{Storage: base}}
	manager = NewHostManager(store, hostSigner,
		stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{escrow: &bridge.EscrowInfo{
			EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
		}}, nil, nil)
	server, err = manager.recoverStoredSession("1")
	require.NoError(t, err)
	require.Equal(t, []diffRange{{from: 4, to: 4}}, store.diffRanges())
	server.Host().Close()
}

func TestRecoverSessions_SnapshotPreservesValidationObservability(t *testing.T) {
	base := newManagerTestStore(t)
	group, user, hostSigner := populateStoreWithSnapshot(t, base, 2, 4)
	require.NoError(t, base.RecordValidationsAppliedOnce("1", []storage.ValidationObsEntry{
		{InferenceID: 1, SlotID: 2},
	}))
	before, err := base.GetValidationObservability("1")
	require.NoError(t, err)
	require.NotEmpty(t, before)

	store := &recordingStorage{Storage: base}
	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	manager := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{escrow: &bridge.EscrowInfo{
			EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
		}}, nil, nil)

	require.NoError(t, manager.RecoverSessions())
	after, err := base.GetValidationObservability("1")
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestRecoverSessions_SnapshotDoesNotEraseSealedInferenceObservability(t *testing.T) {
	base := newManagerTestStore(t)
	group, user, hostSigner := populateStoreWithSnapshot(t, base, 2, 4)
	require.NoError(t, base.InsertSealedInference("1", storage.InferenceRow{
		InferenceID: 99,
		SealedNonce: 2,
		ObsPresent:  true,
		SealedModel: "preserved-model",
	}))

	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	manager := NewHostManager(base, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{escrow: &bridge.EscrowInfo{
			EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
		}}, nil, nil)

	require.NoError(t, manager.RecoverSessions())
	row, found, err := base.GetSealedInference("1", 99)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, row.ObsPresent)
	require.Equal(t, "preserved-model", row.SealedModel)
}

func TestRecoverSessions_UsesBoundedParallelism(t *testing.T) {
	base := newManagerTestStore(t)
	hosts := []*signing.Secp256k1Signer{
		mustGenerateKey(t),
		mustGenerateKey(t),
		mustGenerateKey(t),
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	expectedRoots := make(map[string][]byte, 16)
	for id := 1; id <= 16; id++ {
		escrowID := strconv.Itoa(id)
		config := defaultConfig(len(group))
		require.NoError(t, base.CreateSession(storage.CreateSessionParams{
			EscrowID:       escrowID,
			EpochID:        7,
			Version:        testutil.RuntimeTestVersion,
			CreatorAddr:    user.Address(),
			Config:         config,
			Group:          group,
			InitialBalance: 100000,
		}))
		machine, err := state.NewStateMachine(
			escrowID, config, group, 100000, user.Address(), signing.NewSecp256k1Verifier(), base,
			state.WithVersion(testutil.RuntimeTestVersion),
		)
		require.NoError(t, err)
		txs := []*types.DevshardTx{startTx(1)}
		root, err := machine.ApplyLocal(1, txs)
		require.NoError(t, err)
		require.NoError(t, base.AppendDiff(escrowID, types.DiffRecord{
			Diff:      signDiffWithRoot(t, user, escrowID, 1, txs, root),
			StateHash: root,
		}))
		expectedRoots[escrowID] = root
	}
	store := &concurrencyRecordingStorage{Storage: base}
	manager := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)

	require.NoError(t, manager.RecoverSessions())
	require.Greater(t, store.max.Load(), int32(1))
	require.LessOrEqual(t, store.max.Load(), int32(recoverSessionsConcurrency))
	manager.sessionsMutex.RLock()
	require.Len(t, manager.sessions, 16)
	for escrowID, server := range manager.sessions {
		require.Equal(t, uint64(1), server.Host().LatestNonce())
		root, err := server.Host().StateRoot()
		require.NoError(t, err)
		require.Equal(t, expectedRoots[escrowID], root)
	}
	manager.sessionsMutex.RUnlock()
}

// A host that served sub-floor reservations before the floor landed has them in its own diff log, and
// replaying that log is the only way back up.
func TestRecoverSessions_ReplaysADiffWrittenBeforeTheMaxTokensFloor(t *testing.T) {
	store := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, store, 0)
	config := defaultConfig(3)
	verifier := signing.NewSecp256k1Verifier()

	sm, err := state.NewStateMachine("1", config, group, 100000, user.Address(), verifier, store,
		state.WithVersion(testutil.RuntimeTestVersion))
	require.NoError(t, err)

	txs := []*types.DevshardTx{{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
		InferenceId: 1, Model: "llama", InputLength: 100,
		MaxTokens: testutil.TestMaxTokens - 1, StartedAt: 1000,
	}}}}
	_, err = sm.ApplyLocal(1, txs)
	require.ErrorIs(t, err, types.ErrMaxTokensBelowFloor, "the fixture must be a diff this build refuses to author")
	root, err := sm.ApplyLocalPersisted(1, txs)
	require.NoError(t, err)
	require.NoError(t, store.AppendDiff("1", types.DiffRecord{
		Diff: signDiffWithRoot(t, user, "1", 1, txs, root), StateHash: root,
	}))

	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	bridgeStub := &mockBridge{escrow: &bridge.EscrowInfo{
		EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses,
	}}

	manager := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(),
		nil, testutil.RuntimeTestVersion, bridgeStub, nil, nil)
	require.NoError(t, manager.RecoverSessions())

	manager.sessionsMutex.RLock()
	_, recovered := manager.sessions["1"]
	manager.sessionsMutex.RUnlock()
	require.True(t, recovered, "a session whose diffs predate the floor must still come back up")
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

	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)
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

	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)
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
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, standaloneVersion, br, nil, nil)
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

	mgr := NewHostManager(store, signer, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)
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
	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)
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

	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)
	_, err := mgr.recoverStoredSession("1")
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

	mgr := NewHostManager(store, mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)
	err = mgr.RecoverSessions()
	require.NoError(t, err)

	mgr.sessionsMutex.RLock()
	_, ok := mgr.sessions["1"]
	mgr.sessionsMutex.RUnlock()
	require.False(t, ok, "corrupt session should be skipped, not recovered")
}
