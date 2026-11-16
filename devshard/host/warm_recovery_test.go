package host

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"devshard"
	"devshard/bridge"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type warmRecoveryFixture struct {
	t          *testing.T
	sm         *state.StateMachine
	store      storage.Storage
	user       *signing.Secp256k1Signer
	cold, warm []*signing.Secp256k1Signer
	group      []types.SlotAssignment
	oracle     blocks.BlockOracle
}

func newWarmRecoveryFixture(t *testing.T) *warmRecoveryFixture {
	t.Helper()
	validator := testutil.MustGenerateKey(t)
	addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
	require.NoError(t, err)
	oracle, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "warm-recovery", Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}}, InitialHeight: 100, Start: time.Now().Add(-10 * time.Second), BlockInterval: time.Second})
	require.NoError(t, err)
	anchor, err := oracle.AdvanceOne()
	require.NoError(t, err)
	f := &warmRecoveryFixture{t: t, oracle: oracle, user: testutil.MustGenerateKey(t)}
	for range 3 {
		f.cold = append(f.cold, testutil.MustGenerateKey(t))
	}
	for range 2 {
		f.warm = append(f.warm, testutil.MustGenerateKey(t))
	}
	f.group = testutil.MakeGroup(f.cold)
	cfg := testutil.DefaultConfig(len(f.group))
	cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, anchor.BlockHash
	f.store = testutil.MustMemoryStore(t, "warm-recovery", f.user.Address(), cfg, f.group, 1_000_000)
	f.sm, err = state.NewStateMachine("warm-recovery", cfg, f.group, 1_000_000, f.user.Address(), signing.NewSecp256k1Verifier(), f.store, state.WithVersion("v7"), state.WithMainnetHeights(oracle), state.WithWarmKeyHeightChecker(func(string, string, uint64, int64, time.Time) (bool, error) { return true, nil }))
	require.NoError(t, err)
	return f
}

func (f *warmRecoveryFixture) apply(extra ...*types.DevshardTx) {
	f.t.Helper()
	height, err := f.sm.BuildMainnetHeightTx(context.Background())
	require.NoError(f.t, err)
	txs := append([]*types.DevshardTx{height}, extra...)
	n := f.sm.LatestNonce() + 1
	root, err := f.sm.ApplyLocal(n, txs)
	require.NoError(f.t, err)
	require.NoError(f.t, f.store.AppendDiff("warm-recovery", types.DiffRecord{Diff: testutil.SignDiffWithRoot(f.t, f.user, "warm-recovery", n, txs, root), StateHash: root}))
}

func (f *warmRecoveryFixture) host(key signing.Signer, engine devshard.InferenceEngine, opts ...HostOption) *Host {
	f.t.Helper()
	opts = append(opts, WithStorage(f.store), WithVerifier(signing.NewSecp256k1Verifier()), WithChainOracle(f.oracle))
	h, err := NewHost(f.sm, key, engine, "warm-recovery", f.group, nil, opts...)
	require.NoError(f.t, err)
	f.t.Cleanup(h.Close)
	return h
}

func (f *warmRecoveryFixture) confirm(h *Host) *types.DevshardTx {
	f.t.Helper()
	rec, ok := f.sm.GetInference(1)
	require.True(f.t, ok)
	h.mu.Lock()
	msg, err := h.confirmStartLocked(1, rec, nil, nil)
	h.mu.Unlock()
	require.NoError(f.t, err)
	return &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: msg}}
}

func TestCreatorCannotRevokeLiveExecutorToHideCompletedWork(t *testing.T) {
	f := newWarmRecoveryFixture(t)
	f.apply(testutil.WarmKeyBindTx(1, f.warm[0].Address(), 1), testutil.StartTx(1))
	h := f.host(f.warm[0], stub.NewInferenceEngine())
	f.apply(f.confirm(h))
	_, err := h.RunExecution(context.Background(), &devshard.ExecuteRequest{InferenceID: 1})
	require.NoError(t, err)
	finish := findMempoolFinish(h.MempoolTxs())
	require.NotNil(t, finish)
	rec, ok := f.sm.GetInference(1)
	require.True(t, ok)
	require.NoError(t, f.sm.CheckEvidence(&rec, finish))
	height, err := f.sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	before, err := f.sm.ComputeStateRoot()
	require.NoError(t, err)
	_, err = f.sm.ApplyLocal(3, []*types.DevshardTx{height, testutil.WarmKeyRevokeTx(1, f.warm[0].Address(), 1)})
	require.ErrorIs(t, err, types.ErrInvalidWarmKeyEntry, "the escrow creator cannot disable another participant's live grant")
	after, err := f.sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.NoError(t, f.sm.CheckEvidence(&rec, finish), "the completed work must still defeat an execution timeout")
	f.apply(finish)
	stats := f.sm.SnapshotState().HostStats[1]
	require.Positive(t, stats.Cost)
	require.Zero(t, stats.Missed)
}

type storedFinishEngine struct {
	calls      atomic.Int32
	recoveries atomic.Int32
	result     *devshard.ExecuteResult
}

func (e *storedFinishEngine) Execute(_ context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	if req.Recovery == devshard.RecoveryStoredOnly {
		e.recoveries.Add(1)
	} else {
		e.calls.Add(1)
	}
	return e.result, nil
}

func TestCompletedFinishRetriesAfterGrantRPCRecoveryAndRestart(t *testing.T) {
	f := newWarmRecoveryFixture(t)
	fetch := &resetGrantFetcher{entry: bridge.WarmKeyEntry{Granter: f.cold[1].Address(), Grantee: f.warm[0].Address(), ID: 1, GrantedAtHeight: 100}}
	grants := bridge.NewWarmKeyLogCopy(fetch, time.Hour)
	f.sm.SetWarmKeyHeightChecker(func(c, w string, id uint64, h int64, at time.Time) (bool, error) {
		return bridge.CheckWarmKeyAtHeight(grants, c, w, id, h, at)
	})
	f.apply(testutil.WarmKeyBindTx(1, f.warm[0].Address(), 1), testutil.StartTx(1))
	engine := &storedFinishEngine{result: &devshard.ExecuteResult{ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash, InputTokens: 80, OutputTokens: 40}}
	h := f.host(f.warm[0], engine)
	f.apply(f.confirm(h))
	fetch.down.Store(true)
	grants.Reset()
	result, err := h.RunExecution(context.Background(), &devshard.ExecuteRequest{InferenceID: 1})
	require.ErrorIs(t, err, types.ErrWarmKeyUnconfirmed)
	require.NotNil(t, result)
	require.Empty(t, h.MempoolTxs(), "an unknown grant must not expose a new signature")
	fetch.down.Store(false)
	finish := findMempoolFinish(h.MempoolTxs())
	require.NotNil(t, finish, "ordinary polling retries the completed result without another inference request")
	require.Equal(t, int32(1), engine.calls.Load())
	rec, _ := f.sm.GetInference(1)
	require.NoError(t, f.sm.CheckEvidence(&rec, finish))

	// A replacement process has no pending map or mempool. The payload-only
	// timeout challenge must reconstruct the finish synchronously, without ML.
	restarted := f.host(f.warm[0], engine, WithStoredResponse(func(context.Context, string, uint64, uint64) ([]byte, error) { return []byte("stored"), nil }))
	receipt, _, err := restarted.ChallengeReceipt(context.Background(), 1, nil, nil)
	require.NoError(t, err)
	require.Empty(t, receipt)
	recovered := findMempoolFinish(restarted.MempoolTxs())
	require.NotNil(t, recovered)
	require.NoError(t, f.sm.CheckEvidence(&rec, recovered))
	require.Equal(t, int32(1), engine.calls.Load())
	require.Equal(t, int32(1), engine.recoveries.Load())
}

func TestHAInclusionClearsOtherWarmSignatureVariants(t *testing.T) {
	f := newWarmRecoveryFixture(t)
	var binds []*types.DevshardTx
	for _, slot := range []uint32{1, 2} {
		for i, warm := range f.warm {
			binds = append(binds, testutil.WarmKeyBindTx(slot, warm.Address(), uint64(i+1)))
		}
	}
	f.apply(append(binds, testutil.StartTx(1))...)
	a, b := f.host(f.warm[0], stub.NewInferenceEngine()), f.host(f.warm[1], stub.NewInferenceEngine())
	ca, cb := f.confirm(a), f.confirm(b)
	require.NotEqual(t, types.TxHash(ca), types.TxHash(cb))
	f.apply(ca)
	for _, h := range []*Host{a, b} {
		require.Empty(t, h.MempoolTxs())
		require.Empty(t, h.HostMempool().Txs(), "committed receipt clears every signature variant")
		_, err := h.RunExecution(context.Background(), &devshard.ExecuteRequest{InferenceID: 1})
		require.NoError(t, err)
	}
	fa, fb := findMempoolFinish(a.MempoolTxs()), findMempoolFinish(b.MempoolTxs())
	require.NotNil(t, fa)
	require.NotNil(t, fb)
	require.NotEqual(t, types.TxHash(fa), types.TxHash(fb))
	f.apply(fa)
	for _, h := range []*Host{a, b} {
		require.Empty(t, h.MempoolTxs())
		require.False(t, h.HostMempool().HasStaleEntry(100, 0))
		msg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 2, Valid: true, EscrowId: "warm-recovery"}
		sig, err := h.signProposer(msg)
		require.NoError(t, err)
		msg.ProposerSig = sig
		h.HostMempool().Add(MempoolEntry{Tx: &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: msg}}, ProposedAt: 3})
	}
	va, vb := a.MempoolTxs()[0], b.MempoolTxs()[0]
	require.NotEqual(t, types.TxHash(va), types.TxHash(vb))
	// A different participant still owes its own validation.
	other := proto.Clone(va).(*types.DevshardTx)
	other.GetValidation().ValidatorSlot = 0
	other.GetValidation().ProposerSig = nil
	other.GetValidation().ProposerSig = testutil.SignProposerTx(t, f.cold[0], other.GetValidation())
	b.HostMempool().Add(MempoolEntry{Tx: other, ProposedAt: 3})
	f.apply(va)
	require.Empty(t, a.MempoolTxs())
	require.Equal(t, []*types.DevshardTx{other}, b.MempoolTxs(), "another participant's unperformed validation is retained")
}
