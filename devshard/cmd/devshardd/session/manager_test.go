package session

import (
	"testing"

	"github.com/stretchr/testify/require"
	"path/filepath"

	"devshard/bridge"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/internal/testutil"
	"devshard/types"
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

	srv2, err := mgr.getOrCreate("1")
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
	_, err := mgr.getOrCreate("1")
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
