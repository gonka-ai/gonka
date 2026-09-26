package host

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

// restoringMempoolClient answers GetMempool the way transport's
// HandleGetMempool does: restore persisted Finish messages, then read the mempool.
type restoringMempoolClient struct{ h *Host }

func (c restoringMempoolClient) GetMempool(_ context.Context) ([]*types.DevshardTx, error) {
	c.h.RestorePendingFinishes()
	return c.h.MempoolTxs(), nil
}

func (c restoringMempoolClient) ChallengeReceipt(_ context.Context, _ uint64, _ *InferencePayload, _ []types.Diff) ([]byte, []*types.DevshardTx, error) {
	return nil, nil, nil
}

// Restart after the gateway got the answer but before the Finish reached a
// diff: the gateway still runs HandleTimeout for such an attempt
// ("served_without_finish") and collects the late Finish from host mempools.
// The restarted executor must still hold it, or peers vote the EXECUTION
// timeout for an inference it served.
func TestExecutorRestartKeepsUnsequencedFinish(t *testing.T) {
	ctx := context.Background()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	const balance = 100000
	store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, balance)

	sm1, err := state.NewStateMachine("escrow-1", config, group, balance, user.Address(), verifier, store)
	require.NoError(t, err)
	exec1, err := NewHost(sm1, hosts[1], stub.NewInferenceEngine(), "escrow-1", group, nil, WithStorage(store))
	require.NoError(t, err)

	diff1 := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := handleAndExecute(t, exec1, ctx, HostRequest{Diffs: []types.Diff{diff1}, Nonce: 1, Payload: defaultPayload()})
	require.NoError(t, err)
	require.NotNil(t, findMempoolFinish(resp.Mempool))
	var confirmTx *types.DevshardTx
	for _, tx := range resp.Mempool {
		if tx.GetConfirmStart() != nil {
			confirmTx = tx
		}
	}
	require.NotNil(t, confirmTx)

	// Only ConfirmStart is sequenced; the Finish is still in the mempool.
	diff2 := testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{confirmTx})
	_, err = exec1.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{diff2}})
	require.NoError(t, err)
	st := exec1.SnapshotState()
	require.Equal(t, types.StatusStarted, st.Inferences[1].Status)

	exec2 := rebuildHostFromStore(t, store, hosts[1], verifier)
	require.NotNil(t, findMempoolFinish(exec2.MempoolTxs()), "restarted executor lost the Finish of an inference it served")
	ok, err := VerifyExecutionTimeout(ctx, exec2.SnapshotState(), 1, nil, restoringMempoolClient{exec2}, config, st.Inferences[1].ConfirmedAt+config.ExecutionTimeout)
	require.NoError(t, err)
	require.False(t, ok, "peer accepts an EXECUTION timeout for an inference the executor served before restarting")
}

// Blue/green swap: the new process loads the session while the old one is
// still draining an in-flight inference. The Finish the old process signs
// after the switch must still reach peers voting on an EXECUTION timeout.
func TestPendingFinishFromDrainingProcessReachesVoters(t *testing.T) {
	ctx := context.Background()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	const balance = 100000
	store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, balance)

	sm1, err := state.NewStateMachine("escrow-1", config, group, balance, user.Address(), verifier, store)
	require.NoError(t, err)
	oldProc, err := NewHost(sm1, hosts[1], stub.NewInferenceEngine(), "escrow-1", group, nil, WithStorage(store))
	require.NoError(t, err)

	diff1 := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := oldProc.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{diff1}, Nonce: 1, Payload: defaultPayload()})
	require.NoError(t, err)
	require.NotNil(t, resp.ExecutionJob)
	var confirmTx *types.DevshardTx
	for _, tx := range oldProc.MempoolTxs() {
		if tx.GetConfirmStart() != nil {
			confirmTx = tx
		}
	}
	require.NotNil(t, confirmTx)

	// Switch: the new process loads the session before the old one finishes.
	newProc := rebuildHostFromStore(t, store, hosts[1], verifier)
	require.Nil(t, findMempoolFinish(newProc.MempoolTxs()))

	// The old process drains its in-flight execution.
	_, err = oldProc.RunExecution(ctx, resp.ExecutionJob)
	require.NoError(t, err)

	// The creator sequences ConfirmStart only, through the new process.
	diff2 := testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{confirmTx})
	_, err = newProc.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{diff2}})
	require.NoError(t, err)
	st := newProc.SnapshotState()
	require.Equal(t, types.StatusStarted, st.Inferences[1].Status)

	ok, err := VerifyExecutionTimeout(ctx, st, 1, nil, restoringMempoolClient{newProc}, config, st.Inferences[1].ConfirmedAt+config.ExecutionTimeout)
	require.NoError(t, err)
	require.False(t, ok, "voter accepts an EXECUTION timeout although the executor signed a Finish")
}

// rebuildHostFromStore loads the session into a fresh Host the way
// recoverStoredSession does: replay stored diffs, then NewHost.
func rebuildHostFromStore(t *testing.T, store storage.Storage, signer *signing.Secp256k1Signer, verifier signing.Verifier) *Host {
	t.Helper()
	meta, err := store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	sm, err := state.NewStateMachine("escrow-1", meta.Config, meta.Group, meta.InitialBalance, meta.CreatorAddr, verifier, store)
	require.NoError(t, err)
	recs, err := store.GetDiffs("escrow-1", 1, meta.LatestNonce)
	require.NoError(t, err)
	for _, rec := range recs {
		sm.InjectWarmKeys(rec.WarmKeyDelta)
		_, err := sm.ApplyLocalPersisted(rec.Nonce, rec.Txs)
		require.NoError(t, err)
	}
	h, err := NewHost(sm, signer, stub.NewInferenceEngine(), "escrow-1", meta.Group, nil, WithStorage(store))
	require.NoError(t, err)
	return h
}

// RestorePendingFinishes runs on every GetMempool poll; re-adding a Finish that
// is already queued must not reset ProposedAt, or StaleFinishes never fires.
func TestMempoolAddIfAbsentKeepsProposedAt(t *testing.T) {
	m := NewMempool()
	tx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 7}}}
	require.True(t, m.AddIfAbsent(MempoolEntry{Tx: tx, ProposedAt: 3}))
	require.False(t, m.AddIfAbsent(MempoolEntry{Tx: tx, ProposedAt: 50}))
	require.Len(t, m.StaleFinishes(10, 4), 1)
}
