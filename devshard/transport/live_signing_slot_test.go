package transport

import (
	"common/chainoracle/blocks"
	"context"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestTimeoutVoteSelectsRemainingWarmSlot(t *testing.T) {
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
	sm, err := state.NewStateMachine("live-slots", cfg, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), testutil.MustMemoryStore(t, "live-slots", user.Address(), cfg, group, 1_000_000), state.WithVersion("v7"), state.WithMainnetHeights(source), state.WithWarmKeyHeightChecker(func(c, w string, id uint64, _ int64, _ time.Time) (bool, error) {
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

	apply(testutil.SignedWarmKeyRevokeTx(t, 0, warm, 1, "live-slots"), testutil.StartTx(2))
	statetest.ArmRefusalDue(t, sm, 2)
	srv, err := NewServer(h, shared, signing.NewSecp256k1Verifier(), user.Address())
	require.NoError(t, err)
	srv.SetPeerClients(map[int]HostPeerClient{2: &countingPeer{}})
	req := refusedVerifyRequest()
	req.InferenceID = 2
	response, err := srv.ServeVerifyTimeout(context.Background(), req)
	require.NoError(t, err)
	require.True(t, response.Accept)
	require.Equal(t, uint32(1), response.VoterSlot)
	content, err := types.CanonicalSignedBytes(&types.TimeoutVoteContent{EscrowId: "live-slots", InferenceId: 2, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED, Accept: true})
	require.NoError(t, err)
	recovered, err := signing.NewSecp256k1Verifier().RecoverAddress(content, response.Signature)
	require.NoError(t, err)
	require.True(t, h.SlotActors().Allows(response.VoterSlot, recovered))
}
