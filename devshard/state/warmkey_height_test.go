package state

import (
	"context"
	"testing"
	"time"

	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
	"github.com/stretchr/testify/require"
)

func TestWarmKeyGrantHeightApplyReplayAndHistoricalStateSignatures(t *testing.T) {
	sm, _, source, fresh := heightFixture(t)
	warm := testutil.MustGenerateKey(t).Address()
	cold := sm.SlotAddress(0)
	entry := bridge.WarmKeyEntry{Granter: cold, Grantee: warm, ID: 1, GrantedAtHeight: 100, RevokedAtHeight: 102, End: "REVOKED"}
	checker := func(c, w string, id uint64, h int64, at time.Time) (bool, error) {
		return c == cold && w == warm && id == 1 && entry.LiveAtHeight(h, at), nil
	}
	sm.SetWarmKeyHeightChecker(checker)
	height, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	bind := testutil.WarmKeyBindTx(0, warm, 1)
	vd, err := sm.PreviewLocalBestEffort(1, []*types.DevshardTx{height, bind})
	require.NoError(t, err)
	require.NotNil(t, vd.Applied[0].GetMainnetHeight(), "key pre-staging must not move the mandatory prelude")
	require.Len(t, vd.Applied, 2)
	require.Empty(t, sm.WarmKeyHistory(), "preview must roll back the derived authorization cache too")
	require.True(t, sm.CommitValidated(vd))
	require.True(t, sm.HostSignerAllowedAt(0, warm, 1))
	require.NoError(t, sm.inferenceStore.AppendDiff("height-test", types.DiffRecord{Diff: types.Diff{Nonce: 1, Txs: vd.Applied}, StateHash: vd.Root}))
	_, err = source.AdvanceOne()
	require.NoError(t, err)
	next, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	root, err := sm.ApplyLocal(2, []*types.DevshardTx{next})
	require.NoError(t, err, "cold-key work continues even if the gateway omitted a revoke entry")
	require.False(t, sm.HostSignerAllowedAt(0, warm, 2), "the nonce history alone must not authorize a revoked grant")
	require.True(t, sm.HostSignerAllowedAt(0, warm, 1), "late signatures use the height of the signed nonce")
	replay := fresh()
	replay.SetWarmKeyHeightChecker(checker)
	_, err = replay.ApplyLocalPersisted(1, vd.Applied)
	require.NoError(t, err)
	replayRoot, err := replay.ApplyLocalPersisted(2, []*types.DevshardTx{next})
	require.NoError(t, err)
	require.Equal(t, root, replayRoot)
	require.False(t, replay.HostSignerAllowedAt(0, warm, 2))
	// Persisted replay must not bypass grant validity as the legacy bind confirmer does.
	entry.RevokedAtHeight = 101
	invalid := fresh()
	invalid.SetWarmKeyHeightChecker(checker)
	_, err = invalid.ApplyLocalPersisted(1, vd.Applied)
	require.ErrorIs(t, err, types.ErrWarmKeyUnconfirmed)
	require.Zero(t, invalid.LatestNonce())
}

func TestWarmKeyHeightRejectsBehindViewsAndSameIDForAnotherAddress(t *testing.T) {
	sm, _, _, _ := heightFixture(t)
	warm := testutil.MustGenerateKey(t).Address()
	height, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	bind := testutil.WarmKeyBindTx(0, warm, 1)
	sm.SetWarmKeyHeightChecker(func(string, string, uint64, int64, time.Time) (bool, error) {
		return false, bridge.ErrWarmKeyViewBehind
	})
	_, err = sm.ApplyLocalPersisted(1, []*types.DevshardTx{height, bind})
	require.ErrorIs(t, err, types.ErrHeaderPending)
	require.Zero(t, sm.LatestNonce())
	require.Equal(t, int64(100), sm.MainnetCheckpoint().Height)
	sm.SetWarmKeyHeightChecker(func(_ string, w string, _ uint64, _ int64, _ time.Time) (bool, error) { return w == warm, nil })
	_, err = sm.ApplyLocal(1, []*types.DevshardTx{height, bind})
	require.NoError(t, err)
	repeated, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	attacker := testutil.MustGenerateKey(t).Address()
	_, err = sm.ApplyLocalPersisted(2, []*types.DevshardTx{repeated, testutil.WarmKeyBindTx(0, attacker, 1)})
	require.ErrorIs(t, err, types.ErrWarmKeyUnconfirmed, "authorization cache key must include the grantee")
	require.Equal(t, uint64(1), sm.LatestNonce())
	require.True(t, sm.HostSignerAllowedAt(0, warm, 1), "failed diff must restore the prior grant decisions")
}

func TestExpiredOwnGrantDoesNotOpenSiblingSlot(t *testing.T) {
	base, user, source, _ := heightFixture(t)
	cfg := base.SnapshotState().Config
	cold, a, b := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{cold, cold})
	checker := func(c, w string, id uint64, h int64, _ time.Time) (bool, error) {
		return c == cold.Address() && ((w == a.Address() && id == 1 && h < 102) || (w == b.Address() && id == 2)), nil
	}
	sm, err := NewStateMachine("height-test", cfg, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), testutil.MustMemoryStore(t, "height-test", user.Address(), cfg, group, 1_000_000), WithVersion("v7"), WithMainnetHeights(source), WithWarmKeyHeightChecker(checker))
	require.NoError(t, err)
	height, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	_, err = sm.ApplyLocal(1, []*types.DevshardTx{height, testutil.WarmKeyBindTx(0, a.Address(), 1), testutil.WarmKeyBindTx(1, b.Address(), 2)})
	require.NoError(t, err)
	_, err = source.AdvanceOne()
	require.NoError(t, err)
	height, err = sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	_, err = sm.ApplyLocal(2, []*types.DevshardTx{height})
	require.NoError(t, err)
	require.False(t, sm.HostSignerAllowedAt(0, a.Address(), 2), "expired own key is refused")
	require.False(t, sm.HostSignerAllowedAt(0, b.Address(), 2), "a still-occupied slot must not inherit its sibling's key")
	require.True(t, sm.HostSignerAllowedAt(1, b.Address(), 2))
	sm.mu.RLock()
	actors := sm.consensusActorsAtLocked(3)
	sm.mu.RUnlock()
	require.False(t, actors.Allows(0, b.Address()), "apply uses the same exclusive own-binding rule")
	require.True(t, actors.Allows(0, cold.Address()))
	height, err = sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	_, err = sm.ApplyLocal(3, []*types.DevshardTx{height, testutil.WarmKeyRevokeTx(0, a.Address(), 1)})
	require.NoError(t, err)
	require.True(t, sm.HostSignerAllowedAt(0, b.Address(), 3), "explicit nonce-revoke releases the slot for sibling fallback")
}
