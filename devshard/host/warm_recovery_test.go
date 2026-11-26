package host

import (
	"context"
	"fmt"
	"sync"
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
	require.NoError(f.t, f.store.AppendDiff("warm-recovery", types.DiffRecord{Diff: testutil.SignDiffWithRoot(f.t, f.user, "warm-recovery", n, txs, root), StateHash: root, HostAdmitted: true}))
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

func TestRetiredQueuedFinishDoesNotBlockStoredRecovery(t *testing.T) {
	f := newWarmRecoveryFixture(t)
	f.apply(testutil.WarmKeyBindTx(1, f.warm[0].Address(), 1), testutil.WarmKeyBindTx(1, f.warm[1].Address(), 2), testutil.StartTx(1))
	engine := &storedFinishEngine{result: &devshard.ExecuteResult{ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash, InputTokens: 80, OutputTokens: 40}}
	old := f.host(f.warm[0], engine)
	f.apply(f.confirm(old))
	_, err := old.RunExecution(context.Background(), &devshard.ExecuteRequest{InferenceID: 1})
	require.NoError(t, err)
	retired := findMempoolFinish(old.MempoolTxs())
	require.NotNil(t, retired)
	f.apply(testutil.SignedWarmKeyRevokeTx(t, 1, f.warm[0], 1, "warm-recovery"))
	replacement := f.host(f.warm[1], engine, WithStoredResponse(func(context.Context, string, uint64, uint64) ([]byte, error) { return []byte("stored"), nil }))
	replacement.HostMempool().Add(MempoolEntry{Tx: retired, ProposedAt: 2})
	_, _, err = replacement.ChallengeReceipt(context.Background(), 1, nil, nil)
	require.NoError(t, err)
	finish := findMempoolFinish(replacement.MempoolTxs())
	require.NotNil(t, finish, "one timeout challenge must recover through the live signer even when a retired signature was queued")
	require.Equal(t, int32(1), engine.calls.Load())
	require.Equal(t, int32(1), engine.recoveries.Load())
}

type heldStoredFinishEngine struct {
	storedFinishEngine
	ready, release chan struct{}
	once           sync.Once
}

func (e *heldStoredFinishEngine) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	if req.Recovery != devshard.RecoveryStoredOnly {
		e.calls.Add(1)
		return nil, fmt.Errorf("unexpected model execution")
	}
	e.recoveries.Add(1)
	e.once.Do(func() { close(e.ready) })
	select {
	case <-e.release:
		return e.result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestConcurrentChallengesShareStoredFinishDespiteCallerCancellation(t *testing.T) {
	f := newWarmRecoveryFixture(t)
	f.apply(testutil.WarmKeyBindTx(1, f.warm[0].Address(), 1), testutil.StartTx(1))
	engine := &heldStoredFinishEngine{storedFinishEngine: storedFinishEngine{result: &devshard.ExecuteResult{ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash, InputTokens: 80, OutputTokens: 40}}, ready: make(chan struct{}), release: make(chan struct{})}
	h := f.host(f.warm[0], engine, WithStoredResponse(func(context.Context, string, uint64, uint64) ([]byte, error) { return []byte("stored"), nil }))
	f.apply(f.confirm(h))
	firstCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() { _, _, err := h.ChallengeReceipt(firstCtx, 1, nil, nil); first <- err }()
	select {
	case <-engine.ready:
	case <-time.After(time.Second):
		t.Fatal("stored recovery did not start")
	}
	followers := make(chan error, 3)
	for range 3 {
		go func() { _, _, err := h.ChallengeReceipt(context.Background(), 1, nil, nil); followers <- err }()
	}
	cancel()
	select {
	case err := <-first:
		require.ErrorIs(t, err, types.ErrHeaderPending)
	case <-time.After(time.Second):
		t.Fatal("cancelled caller did not leave")
	}
	for range 3 {
		select {
		case err := <-followers:
			require.ErrorIs(t, err, types.ErrHeaderPending, "in-progress storage is an explicit deferral, never empty success")
		case <-time.After(time.Second):
			t.Fatal("follower did not receive stored recovery")
		}
	}
	close(engine.release)
	var finish *types.DevshardTx
	require.Eventually(t, func() bool { finish = findMempoolFinish(h.MempoolTxs()); return finish != nil }, time.Second, time.Millisecond)
	require.NotNil(t, finish)
	rec, _ := f.sm.GetInference(1)
	require.NoError(t, f.sm.CheckEvidence(&rec, finish))
	require.Equal(t, int32(1), engine.recoveries.Load())
	require.Zero(t, engine.calls.Load())
}
func TestPendingFinishRetrySharesBoundedAuthorizationBatch(t *testing.T) {
	f := newWarmRecoveryFixture(t)
	f.apply(testutil.WarmKeyBindTx(1, f.warm[0].Address(), 1), testutil.WarmKeyBindTx(2, f.warm[0].Address(), 1))
	h := f.host(f.warm[0], stub.NewInferenceEngine())
	h.pendingFinishes = make(map[uint64]*types.MsgFinishInference)
	for id := uint64(100); id < 120; id++ {
		h.pendingFinishes[id] = &types.MsgFinishInference{InferenceId: id, ExecutorSlot: 1 + uint32(id%2), EscrowId: "warm-recovery", ResponseHash: []byte("stored")}
	}
	var calls atomic.Int32
	f.sm.SetWarmKeyHeightChecker(func(string, string, uint64, int64, time.Time) (bool, error) {
		calls.Add(1)
		time.Sleep(150 * time.Millisecond)
		return false, fmt.Errorf("stalled grant RPC")
	})
	started := time.Now()
	require.Empty(t, h.MempoolTxs())
	require.Less(t, time.Since(started), 500*time.Millisecond, "one failed lookup must not multiply by queued results or other cold keys")
	require.Equal(t, int32(1), calls.Load())
	require.Len(t, h.pendingFinishes, 20)
	f.sm.SetWarmKeyHeightChecker(func(string, string, uint64, int64, time.Time) (bool, error) { return true, nil })
	require.Len(t, h.MempoolTxs(), 20, "all unsigned results remain publishable after evidence recovers")
}

func TestPendingFinishRetryRotatesPastUnavailableOlderResults(t *testing.T) {
	f := newWarmRecoveryFixture(t)
	f.apply(testutil.WarmKeyBindTx(1, f.warm[0].Address(), 1), testutil.WarmKeyBindTx(2, f.warm[0].Address(), 1))
	h := f.host(f.warm[0], stub.NewInferenceEngine())
	h.pendingFinishes = make(map[uint64]*types.MsgFinishInference)
	for id := uint64(100); id < 164; id++ {
		h.pendingFinishes[id] = &types.MsgFinishInference{InferenceId: id, ExecutorSlot: 1, EscrowId: "warm-recovery", ResponseHash: []byte("stored")}
	}
	h.pendingFinishes[1000] = &types.MsgFinishInference{InferenceId: 1000, ExecutorSlot: 2, EscrowId: "warm-recovery", ResponseHash: []byte("later result")}
	f.sm.SetWarmKeyHeightChecker(func(c, w string, id uint64, h int64, at time.Time) (bool, error) {
		if c == f.cold[1].Address() {
			return false, fmt.Errorf("unavailable old source")
		}
		return true, nil
	})
	var published []*types.DevshardTx
	for range 65 {
		published = h.MempoolTxs()
		if len(published) > 0 {
			break
		}
	}
	require.Len(t, published, 1)
	require.Equal(t, uint64(1000), published[0].GetFinishInference().InferenceId)
	require.Len(t, h.pendingFinishes, 64)
}
func TestHostCloseCancelsStoredFinishRecovery(t *testing.T) {
	f := newWarmRecoveryFixture(t)
	f.apply(testutil.WarmKeyBindTx(1, f.warm[0].Address(), 1), testutil.StartTx(1))
	engine := &heldStoredFinishEngine{ready: make(chan struct{}), release: make(chan struct{})}
	h := f.host(f.warm[0], engine, WithStoredResponse(func(context.Context, string, uint64, uint64) ([]byte, error) { return []byte("stored"), nil }))
	f.apply(f.confirm(h))
	done := make(chan error, 1)
	go func() { _, _, err := h.ChallengeReceipt(context.Background(), 1, nil, nil); done <- err }()
	select {
	case <-engine.ready:
	case <-time.After(time.Second):
		t.Fatal("stored recovery did not start")
	}
	h.Close()
	select {
	case err := <-done:
		require.ErrorIs(t, err, types.ErrHeaderPending)
	case <-time.After(time.Second):
		t.Fatal("host close left a recovery behind")
	}
	require.Zero(t, engine.calls.Load())
}

func TestFullPendingPassRotatesFirstAuthorizationIdentity(t *testing.T) {
	f := newWarmRecoveryFixture(t)
	f.apply(testutil.WarmKeyBindTx(1, f.warm[0].Address(), 1), testutil.WarmKeyBindTx(2, f.warm[0].Address(), 1))
	h := f.host(f.warm[0], stub.NewInferenceEngine())
	h.pendingFinishes = map[uint64]*types.MsgFinishInference{
		100: {InferenceId: 100, ExecutorSlot: 1, EscrowId: "warm-recovery", ResponseHash: []byte("old")},
		101: {InferenceId: 101, ExecutorSlot: 2, EscrowId: "warm-recovery", ResponseHash: []byte("healthy")},
	}
	f.sm.SetWarmKeyHeightChecker(func(c, w string, id uint64, h int64, at time.Time) (bool, error) {
		if c == f.cold[1].Address() {
			time.Sleep(150 * time.Millisecond)
			return false, fmt.Errorf("slow old source")
		}
		return true, nil
	})
	require.Empty(t, h.MempoolTxs())
	ready := h.MempoolTxs()
	require.Len(t, ready, 1)
	require.Equal(t, uint64(101), ready[0].GetFinishInference().InferenceId)
}
func TestChallengeRecoveryIgnoresUnrelatedQueueAndReportsMissingTargetEvidence(t *testing.T) {
	f := newWarmRecoveryFixture(t)
	f.apply(testutil.WarmKeyBindTx(1, f.warm[0].Address(), 1), testutil.WarmKeyBindTx(2, f.warm[1].Address(), 2), testutil.StartTx(1))
	engine := &storedFinishEngine{result: &devshard.ExecuteResult{ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash, InputTokens: 80, OutputTokens: 40}}
	h := f.host(f.warm[0], engine, WithStoredResponse(func(context.Context, string, uint64, uint64) ([]byte, error) { return []byte("stored"), nil }))
	f.apply(f.confirm(h))
	unrelated := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 2, EscrowId: "warm-recovery", Valid: true}
	unrelated.ProposerSig = testutil.SignProposerTx(t, f.warm[1], unrelated)
	h.AddTx(&types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: unrelated}})
	var unrelatedCalls atomic.Int32
	down := false
	f.sm.SetWarmKeyHeightChecker(func(c, w string, id uint64, h int64, at time.Time) (bool, error) {
		if c == f.cold[2].Address() {
			unrelatedCalls.Add(1)
			time.Sleep(150 * time.Millisecond)
			return false, fmt.Errorf("unrelated outage")
		}
		if down {
			return false, fmt.Errorf("target evidence reset")
		}
		return true, nil
	})
	_, _, err := h.ChallengeReceipt(context.Background(), 1, nil, nil)
	require.NoError(t, err)
	pool, err := h.RecoveryMempoolFor(1)
	require.NoError(t, err)
	require.NotNil(t, findMempoolFinish(pool))
	require.Zero(t, unrelatedCalls.Load(), "foreign proposals cannot consume the challenged inference's budget")
	require.Equal(t, int32(1), engine.recoveries.Load())
	down = true
	pool, err = h.RecoveryMempoolFor(1)
	require.ErrorIs(t, err, types.ErrHeaderPending, "final response cannot turn unknown completed work into empty success")
	require.Empty(t, pool)
	require.True(t, h.HostMempool().HasFinish(1), "unresolved completed work stays queued")
}
func TestStoredRecoveryReturnsPendingBeforePeerDeadline(t *testing.T) {
	f := newWarmRecoveryFixture(t)
	f.apply(testutil.WarmKeyBindTx(1, f.warm[0].Address(), 1), testutil.StartTx(1))
	engine := &heldStoredFinishEngine{ready: make(chan struct{}), release: make(chan struct{})}
	h := f.host(f.warm[0], engine, WithStoredResponse(func(context.Context, string, uint64, uint64) ([]byte, error) { return []byte("stored"), nil }))
	f.apply(f.confirm(h))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, _, err := h.ChallengeReceipt(ctx, 1, nil, nil)
	require.ErrorIs(t, err, types.ErrHeaderPending)
	require.NoError(t, ctx.Err(), "pending must reach the peer before its network deadline")
	h.Close()
}

type cancelResultEngine struct {
	cancel context.CancelFunc
	result *devshard.ExecuteResult
}

func (e cancelResultEngine) Execute(context.Context, devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	e.cancel()
	return e.result, nil
}
func TestFinishPublicationStopsAuthorizationAfterCancellationAndSkipsForeignColdGrants(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			f := newWarmRecoveryFixture(t)
			f.apply(testutil.WarmKeyBindTx(2, f.warm[0].Address(), 1), testutil.StartTx(1))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			engine := devshard.InferenceEngine(stub.NewInferenceEngine())
			if cancelled {
				engine = cancelResultEngine{cancel: cancel, result: &devshard.ExecuteResult{ResponseHash: testutil.TestResponseHash}}
			}
			h := f.host(f.cold[1], engine)
			f.apply(f.confirm(h))
			var calls atomic.Int32
			f.sm.SetWarmKeyHeightChecker(func(string, string, uint64, int64, time.Time) (bool, error) {
				calls.Add(1)
				time.Sleep(150 * time.Millisecond)
				return false, fmt.Errorf("foreign source stalled")
			})
			_, err := h.RunExecution(ctx, &devshard.ExecuteRequest{InferenceID: 1})
			if cancelled {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.NoError(t, err)
			}
			require.Zero(t, calls.Load(), "cancelled publication and unconditional cold authority must start no foreign grant RPCs")
		})
	}
}

func TestFinalRecoveryReadReportsUnknownTargetFinish(t *testing.T) {
	f := newWarmRecoveryFixture(t)
	f.apply(testutil.WarmKeyBindTx(1, f.warm[0].Address(), 1), testutil.StartTx(1))
	h := f.host(f.warm[0], stub.NewInferenceEngine())
	f.apply(f.confirm(h))
	_, err := h.RunExecution(context.Background(), &devshard.ExecuteRequest{InferenceID: 1})
	require.NoError(t, err)
	require.NotNil(t, findMempoolFinish(h.MempoolTxs()))
	f.sm.SetWarmKeyHeightChecker(func(string, string, uint64, int64, time.Time) (bool, error) {
		return false, fmt.Errorf("grant RPC unavailable after recovery")
	})
	pool, err := h.RecoveryMempoolFor(1)
	require.ErrorIs(t, err, types.ErrHeaderPending)
	require.Empty(t, pool)
	require.True(t, h.HostMempool().HasFinish(1))
}

func TestCompletedExecutionReportsPendingWhilePublicationAuthorizationBlocks(t *testing.T) {
	f := newWarmRecoveryFixture(t)
	f.apply(testutil.WarmKeyBindTx(1, f.warm[0].Address(), 1), testutil.StartTx(1))
	h := f.host(f.warm[0], stub.NewInferenceEngine(), WithStoredResponse(func(context.Context, string, uint64, uint64) ([]byte, error) { return []byte("stored"), nil }))
	f.apply(f.confirm(h))
	h.mu.Lock()
	h.executing[1] = struct{}{}
	h.mu.Unlock()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.sm.SetWarmKeyHeightChecker(func(string, string, uint64, int64, time.Time) (bool, error) {
		once.Do(func() { close(entered) })
		<-release
		return false, fmt.Errorf("slow grant RPC")
	})
	done := make(chan error, 1)
	go func() {
		_, err := h.RunExecution(context.Background(), &devshard.ExecuteRequest{InferenceID: 1})
		done <- err
	}()
	defer func() {
		close(release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("publication did not exit")
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("finished execution did not reach authorization")
	}
	_, _, err := h.ChallengeReceipt(context.Background(), 1, nil, nil)
	require.ErrorIs(t, err, types.ErrHeaderPending, "completed work waiting on publication must not look like no result to timeout verifiers")
	read := make(chan error, 1)
	go func() { _, err := h.RecoveryMempoolFor(1); read <- err }()
	select {
	case err := <-read:
		require.ErrorIs(t, err, types.ErrHeaderPending)
	case <-time.After(time.Second):
		t.Fatal("known completion must not start another blocked grant lookup")
	}

}
