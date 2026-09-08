package session

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"runtime/pprof"
	"strconv"
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
	escrow       *bridge.EscrowInfo
	getEscrowErr error
}

func (b *mockBridge) GetEscrow(_ string) (*bridge.EscrowInfo, error) {
	if b.getEscrowErr != nil {
		return nil, b.getEscrowErr
	}
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

// blockingBridge models a chain node that accepts the query and never answers.
type blockingBridge struct {
	mockBridge
	release chan struct{}
}

func (b *blockingBridge) GetEscrow(id string) (*bridge.EscrowInfo, error) {
	<-b.release
	return b.mockBridge.GetEscrow(id)
}

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
		MaxTokens:   testutil.TestMaxTokens,
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

// waitRecoveryRepairsOnCleanup keeps a full-replay recovery from racing t.TempDir:
// startObsRepair still holds the SQLite WAL after RecoverSessions returns.
func waitRecoveryRepairsOnCleanup(t *testing.T, mgr *HostManager) *HostManager {
	t.Helper()
	t.Cleanup(mgr.WaitRecoveryRepairs)
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

	manager := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(),
		nil, testutil.RuntimeTestVersion, bridgeStub, nil, nil))
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

	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
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

	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
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
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, standaloneVersion, br, nil, nil))
	_, err := mgr.getOrCreate("1", nil)
	require.NoError(t, err)

	meta, err := store.GetSessionMeta("1")
	require.NoError(t, err)
	require.Equal(t, standaloneVersion, meta.Version)
}

func TestCreate_RejectsSettledEscrow(t *testing.T) {
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
			Settled:        true,
		},
	}

	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)
	_, err := mgr.getOrCreate("1", nil)
	require.ErrorIs(t, err, bridge.ErrEscrowSettled)

	_, err = store.GetSessionMeta("1")
	require.ErrorIs(t, err, storage.ErrSessionNotFound)

	_, err = mgr.getOrCreate("1", nil)
	require.ErrorIs(t, err, bridge.ErrEscrowSettled)
}

func TestHandleSettlementFinalized_NoLocalRow_BlocksColdBind(t *testing.T) {
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

	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)
	require.NoError(t, mgr.HandleSettlementFinalized("1"))

	_, err := mgr.getOrCreate("1", nil)
	require.ErrorIs(t, err, bridge.ErrEscrowSettled)

	_, err = store.GetSessionMeta("1")
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

// TestStoreSessionIfAbsent_RejectsSettlementRacingCreate covers the interleaving
// where settlement lands after create() read an open escrow but before the built
// server is installed: the tombstone must survive, no session may go live, and
// the row create() already wrote must not stay active for RecoverSessions.
func TestStoreSessionIfAbsent_RejectsSettlementRacingCreate(t *testing.T) {
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

	escrow := &bridge.EscrowInfo{
		EscrowID:       "1",
		EpochID:        7,
		Amount:         100000,
		CreatorAddress: user.Address(),
		Slots:          addresses,
	}
	br := &mockBridge{escrow: escrow}
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)

	require.NoError(t, mgr.HandleSettlementFinalized("1"))

	srv, err := mgr.create("1", escrow)
	require.NoError(t, err)

	_, err = mgr.storeSessionIfAbsent("1", srv)
	require.ErrorIs(t, err, bridge.ErrEscrowSettled)

	_, ok := mgr.existingServer("1")
	require.False(t, ok, "settled escrow must not get a live session")

	meta, err := store.GetSessionMeta("1")
	require.NoError(t, err)
	require.NotEqual(t, "active", meta.Status, "racing row must not stay active")

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Empty(t, active)

	_, err = mgr.getOrCreate("1", nil)
	require.ErrorIs(t, err, bridge.ErrEscrowSettled, "tombstone must survive the rejected install")
}

func TestInstallSession_IgnoresTransientAndExpiredFailures(t *testing.T) {
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

	escrow := &bridge.EscrowInfo{
		EscrowID:       "1",
		EpochID:        7,
		Amount:         100000,
		CreatorAddress: user.Address(),
		Slots:          addresses,
	}
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{escrow: escrow}, nil, nil)

	now := time.Now()
	mgr.rememberResolutionFailure("1", bridge.ErrChainUnavailable, now)

	srv, err := mgr.create("1", escrow)
	require.NoError(t, err)
	installed, err := mgr.storeSessionIfAbsent("1", srv)
	require.NoError(t, err, "a transient failure must not block install")
	require.Equal(t, srv, installed)

	mgr.EvictBefore(8)
	mgr.rememberResolutionFailure("2", bridge.ErrEscrowSettled, now)
	_, settled := mgr.installSession("2", srv, now.Add(permanentFailureTTL+time.Second))
	require.False(t, settled, "an expired settled tombstone must not block install")
}

// Settlement events arrive for every escrow this host holds a slot in, each
// parking a live 10-minute tombstone. Sweeping only drops expired entries, so
// the map needs a hard cap or a settlement burst grows it without limit.
func TestRememberResolutionFailure_BoundsMapUnderLiveTombstoneBurst(t *testing.T) {
	store := newManagerTestStore(t)
	mgr := NewHostManager(store, mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)

	now := time.Now()
	for i := 0; i < maxResolutionFailures*3; i++ {
		// Every entry is permanent and unexpired, so sweeping cannot help.
		mgr.rememberResolutionFailure(strconv.Itoa(i), bridge.ErrEscrowSettled, now)
	}

	mgr.sessionsMutex.RLock()
	size := len(mgr.resolutionFailures)
	mgr.sessionsMutex.RUnlock()
	require.LessOrEqual(t, size, maxResolutionFailures)

	// Eviction is by nearest expiry, so the most recent tombstones survive and
	// still block a bind.
	last := strconv.Itoa(maxResolutionFailures*3 - 1)
	require.ErrorIs(t, mgr.cachedResolutionFailure(last, now), bridge.ErrEscrowSettled)
}

func TestRecoverSessions_EmptyStore(t *testing.T) {
	store := newManagerTestStore(t)
	signer := mustGenerateKey(t)
	br := &mockBridge{}

	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, signer, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
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
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
	require.NoError(t, mgr.RecoverSessions())
	_, ok := mgr.existingServer("1")
	require.True(t, ok, "precondition: session live after recover")

	require.NoError(t, mgr.HandleSettlementFinalized("1"))
	_, ok = mgr.existingServer("1")
	require.False(t, ok, "settlement must drop the live session")

	_, err := mgr.getOrCreate("1", nil)
	require.ErrorIs(t, err, bridge.ErrEscrowSettled)

	// Permanent negative cache: a second bind must not re-read/rebuild.
	_, err = mgr.getOrCreate("1", nil)
	require.ErrorIs(t, err, bridge.ErrEscrowSettled)
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

	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil))
	_, _, err := mgr.recoverStoredSession("1")
	require.ErrorIs(t, err, storage.ErrSessionNotActive)
}

// RecoverSessions asks the chain before replaying a locally-active row. A missed
// settlement event (or one that aged out of the dapi ring) leaves status
// "active"; without this check the host would serve work it can never settle.
func TestRecoverSessions_DoesNotReviveChainSettledEscrow(t *testing.T) {
	store := newManagerTestStore(t)
	_, _, hostSigner := createStoredSession(t, store, "1", 7, 1)

	br := &mockBridge{escrow: &bridge.EscrowInfo{EscrowID: "1", Settled: true}}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))

	require.NoError(t, mgr.RecoverSessions())

	_, live := mgr.existingServer("1")
	require.False(t, live, "a chain-settled escrow must not become a live session")

	meta, err := store.GetSessionMeta("1")
	require.NoError(t, err)
	require.Equal(t, "settled", meta.Status)

	_, err = mgr.getOrCreate("1", nil)
	require.ErrorIs(t, err, bridge.ErrEscrowSettled)
}

// GetEscrow takes no context and the gRPC bridges use context.Background(), so
// a chain node that never answers must not hang startup.
func TestRecoverSessions_HungChainDoesNotBlockStartup(t *testing.T) {
	store := newManagerTestStore(t)
	_, _, hostSigner := createStoredSession(t, store, "1", 7, 1)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	br := &blockingBridge{mockBridge: mockBridge{escrow: &bridge.EscrowInfo{EscrowID: "1", Settled: true}}, release: release}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))

	done := make(chan error, 1)
	go func() { done <- mgr.RecoverSessions() }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(recoveryEscrowCheckTimeout + 10*time.Second):
		t.Fatal("RecoverSessions blocked on a hung chain query")
	}

	_, live := mgr.existingServer("1")
	require.True(t, live, "a timed-out settled-check must fail open and recover the row")
}

// A chain blip at boot must not skip recovery of work this host already bound.
func TestRecoverSessions_RecoversWhenChainUnreachable(t *testing.T) {
	store := newManagerTestStore(t)
	_, _, hostSigner := createStoredSession(t, store, "1", 7, 1)

	br := &mockBridge{getEscrowErr: bridge.ErrChainUnavailable}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))

	require.NoError(t, mgr.RecoverSessions())

	_, live := mgr.existingServer("1")
	require.True(t, live, "transient GetEscrow failure must fail-open and recover the local row")
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

	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
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

func TestRecoverSessions_DoesNotRevivePrunedEpochs(t *testing.T) {
	inner := newManagerTestStore(t)
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
		{escrowID: "11", epochID: 5},
		{escrowID: "12", epochID: 7},
	} {
		require.NoError(t, inner.CreateSession(storage.CreateSessionParams{
			EscrowID:       sess.escrowID,
			EpochID:        sess.epochID,
			Version:        testutil.RuntimeTestVersion,
			CreatorAddr:    user.Address(),
			Config:         config,
			Group:          group,
			InitialBalance: 100000000,
		}))
	}

	store := storage.NewManagedStorage(inner, 3, nil)
	store.ObserveEpoch(8) // retain=3 → cutoff=6, epoch 5 is pruneable
	store.PruneOnce(context.Background())

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, "12", active[0].EscrowID)
	require.Equal(t, uint64(7), active[0].EpochID)

	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil))
	require.NoError(t, mgr.RecoverSessions())
	t.Cleanup(func() { _ = mgr.Close() })

	mgr.sessionsMutex.RLock()
	_, oldOK := mgr.sessions["11"]
	_, currentOK := mgr.sessions["12"]
	mgr.sessionsMutex.RUnlock()
	require.False(t, oldOK, "pruned epoch must not be rebuilt into RAM")
	require.True(t, currentOK)
	require.Equal(t, 0, mgr.EvictBefore(store.PruneCutoff()), "prune-first recovery must leave nothing for EvictBefore")
}

func TestRecoverSessions_SkipsSessionsBelowPruneCutoff(t *testing.T) {
	inner := newManagerTestStore(t)
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
		{escrowID: "11", epochID: 5},
		{escrowID: "12", epochID: 7},
	} {
		require.NoError(t, inner.CreateSession(storage.CreateSessionParams{
			EscrowID:       sess.escrowID,
			EpochID:        sess.epochID,
			Version:        testutil.RuntimeTestVersion,
			CreatorAddr:    user.Address(),
			Config:         config,
			Group:          group,
			InitialBalance: 100000000,
		}))
	}

	store := storage.NewManagedStorage(inner, 3, nil)
	store.ObserveEpoch(8) // cutoff=6, rows still on disk

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Len(t, active, 2)

	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil))
	require.NoError(t, mgr.RecoverSessions())
	t.Cleanup(func() { _ = mgr.Close() })

	mgr.sessionsMutex.RLock()
	_, oldOK := mgr.sessions["11"]
	_, currentOK := mgr.sessions["12"]
	mgr.sessionsMutex.RUnlock()
	require.False(t, oldOK)
	require.True(t, currentOK)

	_, err = mgr.getOrCreate("11", nil)
	require.ErrorIs(t, err, storage.ErrEpochPruned)
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
		{escrowID: "11", epochID: 5},
		{escrowID: "12", epochID: 7},
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

	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil))
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
	_, oldOK := mgr.sessions["11"]
	_, currentOK := mgr.sessions["12"]
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
			EscrowID:       "13",
			EpochID:        7,
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)

	before := countHostValidationWorkers()
	srv, err := mgr.SessionServer("13")
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
			EscrowID:       "14",
			EpochID:        1,
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)

	before := countHostValidationWorkers()
	_, err := mgr.SessionServer("14")
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
			EscrowID:       "15",
			EpochID:        1,
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)

	_, err := mgr.SessionServer("15")
	require.ErrorIs(t, err, storage.ErrEpochPruned)
	_, err = mgr.SessionServer("15")
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
