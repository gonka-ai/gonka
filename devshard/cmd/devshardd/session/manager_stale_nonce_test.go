package session

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/types"
)

func appendStoreAhead(t *testing.T, store storage.Storage, user *signing.Secp256k1Signer) uint64 {
	t.Helper()
	meta, err := store.GetSessionMeta("1")
	require.NoError(t, err)
	sm, err := state.NewStateMachine("1", meta.Config, meta.Group, meta.InitialBalance,
		meta.CreatorAddr, signing.NewSecp256k1Verifier(), store,
		state.WithVersion(meta.Version))
	require.NoError(t, err)
	if meta.LatestNonce > 0 {
		records, err := store.GetDiffs("1", 1, meta.LatestNonce)
		require.NoError(t, err)
		for _, rec := range records {
			_, err := sm.ApplyLocal(rec.Nonce, rec.Txs)
			require.NoError(t, err)
		}
	}
	next := meta.LatestNonce + 1
	txs := []*types.DevshardTx{startTx(next)}
	root, err := sm.ApplyLocal(next, txs)
	require.NoError(t, err)
	require.NoError(t, store.AppendDiff("1", types.DiffRecord{
		Diff:      signDiffWithRoot(t, user, "1", next, txs, root),
		StateHash: root,
	}))
	return next
}

func TestReloadStaleSession_CatchesUpToStoreAhead(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, inner, 5)
	mgr := recoverTestManager(t, inner, hostSigner, user, group)
	require.NoError(t, mgr.RecoverSessions())
	mgr.WaitRecoveryRepairs()

	stale, ok := mgr.existingServer("1")
	require.True(t, ok)
	require.Equal(t, uint64(5), stale.Host().LatestNonce())

	want := appendStoreAhead(t, inner, user)
	next, err := mgr.ReloadStaleSession("1", stale)
	require.NoError(t, err)
	require.Equal(t, want, next.Host().LatestNonce())
	live, ok := mgr.existingServer("1")
	require.True(t, ok)
	require.Equal(t, next, live)
	require.NotEqual(t, stale, live)
}

func TestReloadStaleSession_NegativeCachesBogusNonce(t *testing.T) {
	inner := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, inner, 3)
	mgr := recoverTestManager(t, inner, hostSigner, user, group)
	require.NoError(t, mgr.RecoverSessions())
	mgr.WaitRecoveryRepairs()

	stale, ok := mgr.existingServer("1")
	require.True(t, ok)
	mgr.RememberStaleNonce("1")

	_, err := mgr.ReloadStaleSession("1", stale)
	require.ErrorIs(t, err, types.ErrInvalidNonce)

	live, ok := mgr.existingServer("1")
	require.True(t, ok)
	require.Equal(t, stale, live, "a cached mismatch must not evict the live session")
	require.Equal(t, uint64(3), live.Host().LatestNonce())

	_, err = mgr.ReloadStaleSession("1", stale)
	require.ErrorIs(t, err, types.ErrInvalidNonce)
}
