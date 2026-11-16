package state

import (
	"context"
	"errors"
	"testing"
	"time"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestLiveRevokeConsentIsScopedAndVerifiedOnReplay(t *testing.T) {
	base, user, oracle, _ := heightFixture(t)
	cold, warm, attacker := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{cold, cold})
	cfg := base.SnapshotState().Config
	var unavailable bool
	var endedAt int64
	newSM := func() *StateMachine {
		sm, err := NewStateMachine("consent", cfg, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), testutil.MustMemoryStore(t, "consent", user.Address(), cfg, group, 1_000_000), WithVersion("v7"), WithMainnetHeights(oracle), WithWarmKeyHeightChecker(func(c, w string, id uint64, h int64, _ time.Time) (bool, error) {
			if unavailable {
				return false, errors.New("grant RPC down")
			}
			return c == cold.Address() && w == warm.Address() && id == 1 && (endedAt == 0 || h < endedAt), nil
		}))
		require.NoError(t, err)
		return sm
	}
	sm := newSM()
	height, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	first := []*types.DevshardTx{height, testutil.WarmKeyBindTx(0, warm.Address(), 1), testutil.WarmKeyBindTx(1, warm.Address(), 1)}
	_, err = sm.ApplyLocal(1, first)
	require.NoError(t, err)
	next, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	good := testutil.SignedWarmKeyRevokeTx(t, 0, warm, 1, "consent")
	otherSlot := proto.Clone(good).(*types.DevshardTx)
	otherSlot.GetWarmKeyRevoke().SlotId = 1
	for name, tx := range map[string]*types.DevshardTx{
		"unsigned":         testutil.WarmKeyRevokeTx(0, warm.Address(), 1),
		"other escrow":     testutil.SignedWarmKeyRevokeTx(t, 0, warm, 1, "other"),
		"wrong grant":      testutil.SignedWarmKeyRevokeTx(t, 0, warm, 2, "consent"),
		"other bound slot": otherSlot,
	} {
		t.Run(name, func(t *testing.T) {
			for _, persisted := range []bool{false, true} {
				if persisted {
					_, err = sm.ApplyLocalPersisted(2, []*types.DevshardTx{next, tx})
				} else {
					_, err = sm.ApplyLocal(2, []*types.DevshardTx{next, tx})
				}
				require.ErrorIs(t, err, types.ErrInvalidWarmKeyEntry)
				require.Equal(t, uint64(1), sm.LatestNonce())
			}
		})
	}
	for _, signer := range []*signing.Secp256k1Signer{attacker, user} {
		tx := proto.Clone(good).(*types.DevshardTx)
		data, err := types.WarmKeyRevokeSignBytes("consent", tx.GetWarmKeyRevoke())
		require.NoError(t, err)
		tx.GetWarmKeyRevoke().ParticipantSig, err = signer.Sign(data)
		require.NoError(t, err)
		_, err = sm.ApplyLocal(2, []*types.DevshardTx{next, tx})
		require.ErrorIs(t, err, types.ErrInvalidWarmKeyEntry)
	}
	root, err := sm.ApplyLocal(2, []*types.DevshardTx{next, good})
	require.NoError(t, err)
	replay := newSM()
	_, err = replay.ApplyLocalPersisted(1, first)
	require.NoError(t, err)
	replayed, err := replay.ApplyLocalPersisted(2, []*types.DevshardTx{next, good})
	require.NoError(t, err)
	require.Equal(t, root, replayed)

	// The cold key may also consent while the grant is live.
	sm = newSM()
	_, err = sm.ApplyLocal(1, first)
	require.NoError(t, err)
	coldConsent := proto.Clone(good).(*types.DevshardTx)
	data, err := types.WarmKeyRevokeSignBytes("consent", coldConsent.GetWarmKeyRevoke())
	require.NoError(t, err)
	coldConsent.GetWarmKeyRevoke().ParticipantSig, err = cold.Sign(data)
	require.NoError(t, err)
	_, err = sm.ApplyLocal(2, []*types.DevshardTx{next, coldConsent})
	require.NoError(t, err)

	// An outage cannot be mistaken for an ended grant. A later authenticated
	// end permits unsigned synchronization, while the earlier H still refuses it.
	sm = newSM()
	_, err = sm.ApplyLocal(1, first)
	require.NoError(t, err)
	unsigned := testutil.WarmKeyRevokeTx(0, warm.Address(), 1)
	unavailable = true
	_, err = sm.ApplyLocal(2, []*types.DevshardTx{next, unsigned})
	require.ErrorIs(t, err, types.ErrHeaderPending)
	unavailable = false
	endedAt = sm.MainnetCheckpoint().Height + 1
	_, err = sm.ApplyLocal(2, []*types.DevshardTx{next, unsigned})
	require.ErrorIs(t, err, types.ErrInvalidWarmKeyEntry)
	_, err = oracle.AdvanceOne()
	require.NoError(t, err)
	latest, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	_, err = sm.ApplyLocal(2, []*types.DevshardTx{latest, unsigned})
	require.NoError(t, err)
}
