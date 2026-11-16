package session

import (
	"common/chainoracle/blocks"
	"context"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestValidationRetrySelectsRemainingWarmSlot(t *testing.T) {
	validator := testutil.MustGenerateKey(t)
	addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
	require.NoError(t, err)
	source, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "live-slots", Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}}, InitialHeight: 100, Start: time.Now().Add(-10 * time.Second), BlockInterval: time.Second})
	require.NoError(t, err)
	anchor, err := source.AdvanceOne()
	require.NoError(t, err)
	cfg := testutil.DefaultConfig(3)
	cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, anchor.BlockHash
	cold, peer, warm, user := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{cold, cold, peer})
	shared := testutil.MustMemoryStore(t, "live-slots", user.Address(), cfg, group, 1_000_000)
	sm, err := state.NewStateMachine("live-slots", cfg, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), shared, state.WithVersion("v7"), state.WithMainnetHeights(source), state.WithWarmKeyHeightChecker(func(c, w string, id uint64, _ int64, _ time.Time) (bool, error) {
		return c == cold.Address() && w == warm.Address() && (id == 1 || id == 2), nil
	}))
	require.NoError(t, err)
	apply := func(extra ...*types.DevshardTx) {
		height, err := sm.BuildMainnetHeightTx(context.Background())
		require.NoError(t, err)
		nonce := sm.LatestNonce() + 1
		txs := append([]*types.DevshardTx{height}, extra...)
		root, err := sm.ApplyLocal(nonce, txs)
		require.NoError(t, err)
		require.NoError(t, shared.AppendDiff("live-slots", types.DiffRecord{Diff: testutil.SignDiffWithRoot(t, user, "live-slots", nonce, txs, root), StateHash: root}))
	}
	apply(testutil.WarmKeyBindTx(0, warm.Address(), 1), testutil.WarmKeyBindTx(1, warm.Address(), 1))
	h, err := host.NewHost(sm, warm, stub.NewInferenceEngine(), "live-slots", group, nil, host.WithStorage(shared), host.WithVerifier(signing.NewSecp256k1Verifier()), host.WithChainOracle(source))
	require.NoError(t, err)
	t.Cleanup(h.Close)

	require.NoError(t, submitValidationToMempool(h, 99, true))
	require.True(t, validationAlreadyInMempool(h, 99))
	apply(testutil.SignedWarmKeyRevokeTx(t, 0, warm, 1, "live-slots"))
	require.False(t, validationAlreadyInMempool(h, 99), "revoked queued validation must not suppress a replacement")

	require.NoError(t, submitValidationToMempool(h, 99, true))
	txs := h.MempoolTxs()
	require.Len(t, txs, 1)
	msg := txs[0].GetValidation()
	require.NotNil(t, msg)
	require.Equal(t, uint32(1), msg.ValidatorSlot)
	copy := *msg
	copy.ProposerSig = nil
	data, err := types.CanonicalSignedBytes(&copy)
	require.NoError(t, err)
	recovered, err := signing.NewSecp256k1Verifier().RecoverAddress(data, msg.ProposerSig)
	require.NoError(t, err)
	require.True(t, h.SlotActors().Allows(msg.ValidatorSlot, recovered))
	apply(testutil.SignedWarmKeyRevokeTx(t, 1, warm, 1, "live-slots"))
	require.ErrorIs(t, submitValidationToMempool(h, 100, true), types.ErrWarmKeyUnconfirmed)
	require.Empty(t, h.MempoolTxs(), "no unauthorized retry or stale validation is queued")
}
