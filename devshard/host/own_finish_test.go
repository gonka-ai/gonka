package host

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

type ownFinishFixture struct {
	hosts []*signing.Secp256k1Signer
	user  *signing.Secp256k1Signer
	group []types.SlotAssignment
	store storage.Storage
}

func newOwnFinishFixture(t *testing.T) *ownFinishFixture {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	return &ownFinishFixture{
		hosts: hosts,
		user:  user,
		group: group,
		store: storage.NewObsRepairGate(storage.NewManagedStorage(storage.NewHybridStorage(
			testutil.MustMemoryStore(t, "escrow-1", user.Address(), testutil.DefaultConfig(len(hosts)), group, 100000)), 3, nil)),
	}
}

func (f *ownFinishFixture) openHost(t *testing.T, engine devshard.InferenceEngine) *Host {
	t.Helper()
	config := testutil.DefaultConfig(len(f.hosts))
	sm, err := state.NewStateMachine("escrow-1", config, f.group, 100000, f.user.Address(), signing.NewSecp256k1Verifier(), f.store)
	require.NoError(t, err)
	meta, err := f.store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	if meta.LatestNonce > 0 {
		require.NoError(t, storage.ReadDiffPages(f.store, "escrow-1", 1, meta.LatestNonce, func(page []types.DiffRecord) error {
			for _, rec := range page {
				if _, err := sm.ApplyLocalPersisted(rec.Nonce, rec.Txs); err != nil {
					return err
				}
			}
			return nil
		}))
	}
	h, err := NewHost(sm, f.hosts[1], engine, "escrow-1", f.group, nil, WithGrace(100), WithStorage(f.store))
	require.NoError(t, err)
	return h
}

func (f *ownFinishFixture) execute(t *testing.T, h *Host) (*types.DevshardTx, *types.DevshardTx, types.Diff) {
	t.Helper()
	start := testutil.SignDiff(t, f.user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{start}, Nonce: 1, Payload: defaultPayload()})
	require.NoError(t, err)
	require.NotNil(t, resp.ExecutionJob)
	confirm := findMempoolConfirm(h.MempoolTxs())
	require.NotNil(t, confirm)
	_, err = h.RunExecution(context.Background(), resp.ExecutionJob)
	require.NoError(t, err)
	h.ownFinishWrites.Wait()
	finish := findMempoolFinish(h.MempoolTxs())
	require.NotNil(t, finish)
	return confirm, finish, start
}

func (f *ownFinishFixture) apply(t *testing.T, h *Host, nonce uint64, txs ...*types.DevshardTx) {
	t.Helper()
	_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{testutil.SignDiff(t, f.user, "escrow-1", nonce, txs)}})
	require.NoError(t, err)
}

func TestHost_RestoredPendingInferenceReloadsOwnFinish(t *testing.T) {
	f := newOwnFinishFixture(t)
	live := f.openHost(t, stub.NewInferenceEngine())
	confirm, finish, _ := f.execute(t, live)

	engine := &recoveringEngine{}
	restored := f.openHost(t, engine)
	rec, ok := restored.sm.GetInference(1)
	require.True(t, ok)
	require.Equal(t, types.StatusPending, rec.Status)

	reloaded := findMempoolFinish(restored.MempoolTxs())
	require.NotNil(t, reloaded)
	require.True(t, proto.Equal(finish, reloaded))
	require.Empty(t, engine.modes)

	f.apply(t, restored, 2, confirm)
	f.apply(t, restored, 3, reloaded)
	rec, ok = restored.sm.GetInference(1)
	require.True(t, ok)
	require.Equal(t, types.StatusFinished, rec.Status)
	require.Empty(t, engine.modes)
}

func TestHost_RestoredStartedInferenceReloadsOwnFinish(t *testing.T) {
	f := newOwnFinishFixture(t)
	live := f.openHost(t, stub.NewInferenceEngine())
	confirm, finish, _ := f.execute(t, live)
	f.apply(t, live, 2, confirm)

	restored := f.openHost(t, &recoveringEngine{})
	rec, ok := restored.sm.GetInference(1)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status)
	reloaded := findMempoolFinish(restored.MempoolTxs())
	require.NotNil(t, reloaded)
	require.True(t, proto.Equal(finish, reloaded))

	f.apply(t, restored, 3, reloaded)
	rec, ok = restored.sm.GetInference(1)
	require.True(t, ok)
	require.Equal(t, types.StatusFinished, rec.Status)
}

func TestHost_RestoreSkipsOwnFinishOfClosedInference(t *testing.T) {
	f := newOwnFinishFixture(t)
	live := f.openHost(t, stub.NewInferenceEngine())
	confirm, finish, _ := f.execute(t, live)
	f.apply(t, live, 2, confirm)
	f.apply(t, live, 3, finish)

	restored := f.openHost(t, &recoveringEngine{})
	require.Nil(t, findMempoolFinish(restored.MempoolTxs()))
}

func TestHost_CatchUpFromStoreReloadsFinishPersistedByReplica(t *testing.T) {
	f := newOwnFinishFixture(t)
	owner := f.openHost(t, stub.NewInferenceEngine())
	start := testutil.SignDiff(t, f.user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := owner.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{start}, Nonce: 1, Payload: defaultPayload()})
	require.NoError(t, err)
	require.NotNil(t, resp.ExecutionJob)

	standby := f.openHost(t, &recoveringEngine{})
	require.Nil(t, findMempoolFinish(standby.MempoolTxs()))

	_, err = owner.RunExecution(context.Background(), resp.ExecutionJob)
	require.NoError(t, err)
	owner.ownFinishWrites.Wait()

	require.NoError(t, standby.CatchUpFromStore(context.Background()))
	reloaded := findMempoolFinish(standby.MempoolTxs())
	require.NotNil(t, reloaded)
	require.True(t, proto.Equal(findMempoolFinish(owner.MempoolTxs()), reloaded))
}

func TestHost_StandbyReloadsOwnFinishAfterDurableGapHeal(t *testing.T) {
	f := newOwnFinishFixture(t)
	owner := f.openHost(t, stub.NewInferenceEngine())
	start := testutil.SignDiff(t, f.user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := owner.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{start}, Nonce: 1, Payload: defaultPayload()})
	require.NoError(t, err)
	require.NotNil(t, resp.ExecutionJob)
	confirm := findMempoolConfirm(owner.MempoolTxs())
	require.NotNil(t, confirm)

	standby := f.openHost(t, &recoveringEngine{})

	_, err = owner.RunExecution(context.Background(), resp.ExecutionJob)
	require.NoError(t, err)
	owner.ownFinishWrites.Wait()
	f.apply(t, owner, 2, confirm)

	resp, err = standby.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{testutil.SignDiff(t, f.user, "escrow-1", 3, nil)}})
	require.NoError(t, err)
	require.Equal(t, uint64(3), standby.LatestNonce())
	reloaded := findMempoolFinish(resp.Mempool)
	require.NotNil(t, reloaded)
	require.True(t, proto.Equal(findMempoolFinish(owner.MempoolTxs()), reloaded))
}

func TestHost_ExecutingInferenceIsNotReloaded(t *testing.T) {
	f := newOwnFinishFixture(t)
	h := f.openHost(t, stub.NewInferenceEngine())
	start := testutil.SignDiff(t, f.user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{start}, Nonce: 1, Payload: defaultPayload()})
	require.NoError(t, err)
	require.NotNil(t, resp.ExecutionJob)

	h.mu.Lock()
	ids := h.ownFinishCandidatesLocked()
	h.mu.Unlock()
	require.Empty(t, ids)
	h.ReleaseExecution(1)
	h.mu.Lock()
	ids = h.ownFinishCandidatesLocked()
	h.mu.Unlock()
	require.Equal(t, []uint64{1}, ids)
}
