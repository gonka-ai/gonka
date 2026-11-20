package state

import (
	"bytes"
	"context"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/lightproof"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func heightFixture(t *testing.T) (*StateMachine, *signing.Secp256k1Signer, *mockoracle.Mock, func() *StateMachine) {
	t.Helper()
	validator := testutil.MustGenerateKey(t)
	addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
	require.NoError(t, err)
	source, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "gonka-height-test", Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}}, Start: time.Now().Add(-10 * time.Second).UTC(), InitialHeight: 100, BlockInterval: time.Second})
	require.NoError(t, err)
	anchor, err := source.AdvanceOne()
	require.NoError(t, err)
	cfg := testutil.DefaultConfig(2)
	cfg.MainnetAnchorHeight = anchor.Height
	cfg.MainnetAnchorHash = bytes.Clone(anchor.BlockHash)
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)})
	fresh := func() *StateMachine {
		sm, err := NewStateMachine("height-test", cfg, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), testutil.MustMemoryStore(t, "height-test", user.Address(), cfg, group, 1_000_000), WithVersion("v7"), WithMainnetHeights(source))
		require.NoError(t, err)
		return sm
	}
	_, err = source.AdvanceOne()
	require.NoError(t, err)
	return fresh(), user, source, fresh
}

type deadlineHeightSource struct {
	MainnetHeightSource
	blocks.BlockOracle
	check func(context.Context)
}

func (s deadlineHeightSource) Latest(ctx context.Context) (*blocks.Header, error) {
	s.check(ctx)
	return s.MainnetHeightSource.Latest(ctx)
}

func (s deadlineHeightSource) LightBlockAt(ctx context.Context, height int64) ([]byte, error) {
	s.check(ctx)
	return s.MainnetHeightSource.LightBlockAt(ctx, height)
}

func (s deadlineHeightSource) At(ctx context.Context, height int64) (*blocks.Header, error) {
	s.check(ctx)
	return s.BlockOracle.At(ctx, height)
}

func TestMainnetHeightBuildBoundsEveryRPCFromBackgroundContext(t *testing.T) {
	sm, _, source, _ := heightFixture(t)
	var deadline time.Time
	calls := 0
	sm.mainnetHeights = deadlineHeightSource{MainnetHeightSource: source, BlockOracle: source, check: func(ctx context.Context) {
		got, ok := ctx.Deadline()
		require.True(t, ok, "a stalled RPC must not hold the session mutex indefinitely")
		require.LessOrEqual(t, time.Until(got), 10*time.Second)
		if calls == 0 {
			deadline = got
		} else {
			require.Equal(t, deadline, got, "all RPCs share one budget rather than extending it per block")
		}
		calls++
	}}
	tx, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	require.Greater(t, calls, 1, "exercise both Latest and carried light-block reads")
	_, err = sm.ApplyLocal(1, []*types.DevshardTx{tx})
	require.NoError(t, err)
	calls = 0
	sm.headerTimes = nil // Exercise the direct canonical-oracle fallback rather than TestMain's fixture.
	require.NoError(t, sm.CheckMainnetHeightFreshness(context.Background(), time.Now()))
	require.Greater(t, calls, 1, "live signing must bound Latest and canonical lookup too")
}

func TestMainnetHeightLagRejectsBeforeCanonicalLookup(t *testing.T) {
	sm, _, source, _ := heightFixture(t)
	tx, err := sm.BuildMainnetHeightTx(t.Context())
	require.NoError(t, err)
	_, err = sm.ApplyLocal(1, []*types.DevshardTx{tx})
	require.NoError(t, err)
	for range MainnetCheckpointMaxLag + 1 {
		_, err := source.AdvanceOne()
		require.NoError(t, err)
	}

	calls := 0
	sm.headerTimes = nil
	sm.mainnetHeights = deadlineHeightSource{MainnetHeightSource: source, BlockOracle: source, check: func(context.Context) { calls++ }}
	require.ErrorIs(t, sm.CheckMainnetHeightFreshness(t.Context(), time.Now()), types.ErrHeaderPending)
	require.Equal(t, 1, calls, "only the live tip is queried when the checkpoint is too far behind")
}

func TestMainnetHeightApplyReplaySnapshotAndPreview(t *testing.T) {
	sm, user, _, fresh := heightFixture(t)
	before, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	tx, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, tx.GetMainnetHeight().LightBlocks)
	preview, err := sm.PreviewLocalBestEffort(1, []*types.DevshardTx{tx})
	require.NoError(t, err)
	require.NotEqual(t, before, preview.Root)
	require.Equal(t, int64(100), sm.MainnetCheckpoint().Height)
	unchanged, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, before, unchanged)
	require.True(t, sm.CommitValidated(preview))
	cp := sm.MainnetCheckpoint()
	require.Equal(t, int64(101), cp.Height)
	require.NoError(t, sm.CheckMainnetHeightFreshness(context.Background(), time.Now()))
	txs := []*types.DevshardTx{tx}
	host := fresh()
	diff := testutil.SignDiff(t, user, "height-test", 1, txs)
	diff.PostStateRoot = preview.Root
	// Post-state root is signed too.
	content := BuildDiffContent("height-test", 1, txs, preview.Root)
	data, err := proto.Marshal(content)
	require.NoError(t, err)
	diff.UserSig, err = user.Sign(data)
	require.NoError(t, err)
	root, err := host.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, preview.Root, root)
	replay := fresh()
	replayRoot, err := replay.ApplyLocalPersisted(1, txs)
	require.NoError(t, err)
	require.Equal(t, root, replayRoot)
	require.Equal(t, cp, replay.MainnetCheckpoint())
	encoded, err := types.MarshalStateSnapshotProto(ptrState(sm.SnapshotState()), nil, nil, sm.ExportHeightSyncFloor())
	require.NoError(t, err)
	snapshot, _, _, floorProto, err := types.UnmarshalStateSnapshotProto(encoded)
	require.NoError(t, err)
	restored := fresh()
	floor, err := heightsync.FloorIndexFromProto(heightsync.FloorConfig{}, floorProto)
	require.NoError(t, err)
	require.NoError(t, restored.RestoreStateWithFloor(snapshot, floor))
	require.Equal(t, cp, restored.MainnetCheckpoint())
	restoredRoot, err := restored.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, root, restoredRoot)
	same, err := restored.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	require.Empty(t, same.GetMainnetHeight().LightBlocks)
	nextRoot, err := sm.ApplyLocal(2, []*types.DevshardTx{same})
	require.NoError(t, err)
	restoredRoot, err = restored.ApplyLocalPersisted(2, []*types.DevshardTx{same})
	require.NoError(t, err)
	require.Equal(t, nextRoot, restoredRoot)
	// Freshness is checked only for newly issued signatures, never replay.
	require.ErrorIs(t, sm.CheckMainnetHeightFreshness(context.Background(), cp.Time.Add(3*time.Minute)), types.ErrHeaderPending)
}

func ptrState(s types.EscrowState) *types.EscrowState { return &s }

func TestMainnetHeightFailureIsAtomicAndReplayDoesNotBypassProof(t *testing.T) {
	sm, _, source, _ := heightFixture(t)
	tx, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	before := sm.MainnetCheckpoint()
	// A valid checkpoint followed by an invalid mandatory transaction rolls back
	// checkpoint, nonce, fees and the state root together.
	root, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	bad := txStart(&types.MsgStartInference{InferenceId: 1, MaxTokens: 0})
	_, err = sm.ApplyLocal(1, []*types.DevshardTx{tx, bad})
	require.Error(t, err)
	require.Equal(t, before, sm.MainnetCheckpoint())
	require.Zero(t, sm.LatestNonce())
	got, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, root, got)
	for _, txs := range [][]*types.DevshardTx{nil, {tx, tx}, {txFinalize(), tx}} {
		_, err = sm.ApplyLocalPersisted(1, txs)
		require.ErrorIs(t, err, lightproof.ErrInvalid)
		require.Equal(t, before, sm.MainnetCheckpoint())
	}
	forged := &types.DevshardTx{Tx: &types.DevshardTx_MainnetHeight{MainnetHeight: &types.MsgMainnetHeight{Height: 101}}}
	_, err = sm.ApplyLocalPersisted(1, []*types.DevshardTx{forged})
	require.ErrorIs(t, err, lightproof.ErrInvalid)
	_, err = sm.ApplyLocal(1, []*types.DevshardTx{tx})
	require.NoError(t, err)
	for i := 0; i < 4; i++ {
		_, err = source.AdvanceOne()
		require.NoError(t, err)
	}
	require.ErrorIs(t, sm.CheckMainnetHeightFreshness(context.Background(), time.Now()), types.ErrHeaderPending)
	rollback := &types.DevshardTx{Tx: &types.DevshardTx_MainnetHeight{MainnetHeight: &types.MsgMainnetHeight{Height: 100}}}
	_, err = sm.ApplyLocalPersisted(2, []*types.DevshardTx{rollback})
	require.ErrorIs(t, err, lightproof.ErrInvalid)
}

func TestMainnetHeightClaimedTimeIsAuthenticated(t *testing.T) {
	sm, user, _, _ := heightFixture(t)
	tx, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	tampered := proto.Clone(tx).(*types.DevshardTx)
	tampered.GetMainnetHeight().TimeUnixNano++
	_, err = sm.ApplyLocalPersisted(1, []*types.DevshardTx{tampered})
	require.ErrorIs(t, err, lightproof.ErrInvalid)
	require.Zero(t, sm.LatestNonce())
	diff := testutil.SignDiff(t, user, "height-test", 1, []*types.DevshardTx{tx})
	diff.Txs = []*types.DevshardTx{tampered}
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidUserSig)
}

func TestMainnetPreludeDoesNotSpendNonceForRejectedHeightAck(t *testing.T) {
	sm, _, _, _ := heightFixture(t)
	sm.state.Config.FeePerNonce = 7
	tx, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	before := sm.SnapshotState()
	root, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	ack := &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{
		RefNonce: 1, SlotId: 0, ObservedHeight: uint64(tx.GetMainnetHeight().Height),
		ObservedBlockHash: []byte("invalid-ack-hash"), SyncState: types.SyncState_SYNCED,
		PeerSeen: []byte{0xff}, HostSig: make([]byte, 65),
	}}}
	_, applied, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{tx, ack})
	require.Error(t, err)
	require.Empty(t, applied)
	require.Equal(t, before.LatestNonce, sm.LatestNonce())
	require.Equal(t, before.Balance, sm.SnapshotState().Balance)
	require.Equal(t, before.MainnetCheckpoint, sm.MainnetCheckpoint())
	after, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, root, after)
	// An intentional checkpoint-only diff is still useful when no ack was rejected.
	_, applied, err = sm.ApplyLocalBestEffort(1, []*types.DevshardTx{tx})
	require.NoError(t, err)
	require.Len(t, applied, 1)
}

func TestMainnetSnapshotCannotBootstrapItsOwnTimeOrValidatorAuthority(t *testing.T) {
	sm, _, _, fresh := heightFixture(t)
	tx, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	_, err = sm.ApplyLocal(1, []*types.DevshardTx{tx})
	require.NoError(t, err)
	for _, mutate := range []func(*types.EscrowState){
		func(s *types.EscrowState) { s.MainnetCheckpoint.Time = s.MainnetCheckpoint.Time.Add(-time.Second) },
		func(s *types.EscrowState) { s.MainnetCheckpoint.NextValidatorsHash = make([]byte, 32) },
		func(s *types.EscrowState) { s.Config.MainnetAnchorHash = make([]byte, 32) },
		func(s *types.EscrowState) { s.MainnetCheckpoint.Height++ },
	} {
		snapshot := sm.SnapshotState()
		mutate(&snapshot)
		restored := fresh()
		err := restored.RestoreState(&snapshot)
		require.Error(t, err)
		require.Zero(t, restored.LatestNonce(), "bad snapshot must not replace live state")
	}
}

// ParamsForHost permits a nil persistence context, which reaches prelude construction.
func TestMainnetHeightBuildAcceptsNilPersistenceContext(t *testing.T) {
	sm, _, _, _ := heightFixture(t)
	var tx *types.DevshardTx
	var err error
	require.NotPanics(t, func() { tx, err = sm.BuildMainnetHeightTx(nil) })
	require.NoError(t, err)
	require.NotNil(t, tx)
}
