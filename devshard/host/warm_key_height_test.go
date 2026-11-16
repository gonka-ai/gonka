package host

import (
	"context"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"devshard/bridge"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
	"github.com/stretchr/testify/require"
)

func TestHostGossipActorsUseTheSignedNoncesGrantHeight(t *testing.T) {
	validator := testutil.MustGenerateKey(t)
	addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
	require.NoError(t, err)
	source, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "warm-host", Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}}, InitialHeight: 100, Start: time.Now().Add(-10 * time.Second), BlockInterval: time.Second})
	require.NoError(t, err)
	anchor, err := source.AdvanceOne()
	require.NoError(t, err)
	cfg := testutil.DefaultConfig(1)
	cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, anchor.BlockHash
	cold, warm, user := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{cold})
	store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), cfg, group, 1_000_000)
	entry := bridge.WarmKeyEntry{Granter: cold.Address(), Grantee: warm.Address(), ID: 1, GrantedAtHeight: 100, RevokedAtHeight: 101, End: "REVOKED"}
	liveNow := true
	sm, err := state.NewStateMachine("escrow-1", cfg, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), store, state.WithVersion("v7"), state.WithMainnetHeights(source), state.WithWarmKeyResolver(func(string, string) (bool, error) { return liveNow, nil }), state.WithWarmKeyHeightChecker(func(c, w string, id uint64, height int64, at time.Time) (bool, error) {
		return c == entry.Granter && w == entry.Grantee && id == 1 && entry.LiveAtHeight(height, at), nil
	}))
	require.NoError(t, err)
	h, err := NewHost(sm, cold, stub.NewInferenceEngine(), "escrow-1", group, nil)
	require.NoError(t, err)
	height, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	txs := []*types.DevshardTx{height, testutil.WarmKeyBindTx(0, warm.Address(), 1)}
	root, err := sm.ApplyLocal(1, txs)
	require.NoError(t, err)
	require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{Diff: types.Diff{Nonce: 1, Txs: txs}, StateHash: root}))
	require.True(t, h.SlotActorsAt(1).Allows(0, warm.Address()))
	liveNow = false // chain has revoked the grant after the signed checkpoint
	require.False(t, h.SlotActors().Allows(0, warm.Address()), "live admission rejects the revoked key immediately")
	_, err = source.AdvanceOne()
	require.NoError(t, err)
	height, err = sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	_, err = sm.ApplyLocal(2, []*types.DevshardTx{height}) // gateway omits a revoke
	require.NoError(t, err)
	require.False(t, h.SlotActorsAt(2).Allows(0, warm.Address()), "nonce history cannot extend a revoked grant")
	require.True(t, h.SlotActorsAt(1).Allows(0, warm.Address()), "late gossip remains valid at its historical height")
}
