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
	"google.golang.org/protobuf/proto"
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

func TestFutureGossipCannotUsePreRevokeHAState(t *testing.T) {
	validator := testutil.MustGenerateKey(t)
	addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
	require.NoError(t, err)
	source, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "future-warm-host", Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}}, InitialHeight: 100, Start: time.Now().Add(-10 * time.Second), BlockInterval: time.Second})
	require.NoError(t, err)
	anchor, err := source.AdvanceOne()
	require.NoError(t, err)
	cfg := testutil.DefaultConfig(1)
	cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, anchor.BlockHash
	cold, warm, user := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{cold})
	shared := testutil.MustMemoryStore(t, "future-warm", user.Address(), cfg, group, 1_000_000)
	newSM := func() *state.StateMachine {
		sm, err := state.NewStateMachine("future-warm", cfg, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), shared, state.WithVersion("v7"), state.WithMainnetHeights(source), state.WithWarmKeyHeightChecker(func(c, w string, id uint64, _ int64, _ time.Time) (bool, error) {
			return c == cold.Address() && w == warm.Address() && id == 1, nil
		}))
		require.NoError(t, err)
		return sm
	}
	a, b := newSM(), newSM()
	height, err := a.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	first := []*types.DevshardTx{height, testutil.WarmKeyBindTx(0, warm.Address(), 1)}
	root, err := a.ApplyLocal(1, first)
	require.NoError(t, err)
	require.NoError(t, shared.AppendDiff("future-warm", types.DiffRecord{Diff: types.Diff{Nonce: 1, Txs: first}, StateHash: root}))
	_, err = b.ApplyLocalPersisted(1, first)
	require.NoError(t, err)
	height, err = b.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	second := []*types.DevshardTx{height, testutil.SignedWarmKeyRevokeTx(t, 0, warm, 1, "future-warm")}
	root, err = b.ApplyLocal(2, second)
	require.NoError(t, err)
	require.NoError(t, shared.AppendDiff("future-warm", types.DiffRecord{Diff: types.Diff{Nonce: 2, Txs: second}, StateHash: root}))
	content, err := proto.Marshal(&types.StateSignatureContent{StateRoot: root, EscrowId: "future-warm", Nonce: 2})
	require.NoError(t, err)
	good, err := cold.Sign(content)
	require.NoError(t, err)
	require.NoError(t, shared.AddSignature("future-warm", 2, 0, good))
	bad, err := warm.Sign(content)
	require.NoError(t, err)
	h, err := NewHost(a, cold, stub.NewInferenceEngine(), "future-warm", group, nil, WithStorage(shared), WithVerifier(signing.NewSecp256k1Verifier()))
	require.NoError(t, err)
	require.Error(t, h.AccumulateGossipSig(2, root, bad, 0), "shared durable root is not a witness that this replica has applied its revocation")
	sigs, err := shared.GetSignatures("future-warm", 2)
	require.NoError(t, err)
	require.Equal(t, good, sigs[0], "an unverified future signer cannot overwrite another replica's valid signature")
	require.False(t, h.SlotActorsAt(2).Allows(0, warm.Address()), "future state authorization cannot borrow the old checkpoint/history")
	_, err = a.ApplyLocalPersisted(2, second)
	require.NoError(t, err)
	require.Error(t, h.AccumulateGossipSig(2, root, bad, 0))
	require.NoError(t, h.AccumulateGossipSig(2, root, good, 0))
}

func TestRevokedOwnWarmSignerCannotOverwriteHAStateSignature(t *testing.T) {
	for _, nonceRevoke := range []bool{false, true} {
		name := "expired-chain-grant"
		if nonceRevoke {
			name = "nonce-revoke"
		}
		t.Run(name, func(t *testing.T) {
			validator := testutil.MustGenerateKey(t)
			addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
			require.NoError(t, err)
			source, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "own-warm", Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}}, InitialHeight: 100, Start: time.Now().Add(-10 * time.Second), BlockInterval: time.Second})
			require.NoError(t, err)
			anchor, err := source.AdvanceOne()
			require.NoError(t, err)
			cfg := testutil.DefaultConfig(1)
			cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, anchor.BlockHash
			cold, warm, user := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
			group := testutil.MakeGroup([]*signing.Secp256k1Signer{cold})
			shared := testutil.MustMemoryStore(t, "own-warm", user.Address(), cfg, group, 1_000_000)
			sm, err := state.NewStateMachine("own-warm", cfg, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), shared, state.WithVersion("v7"), state.WithMainnetHeights(source), state.WithWarmKeyHeightChecker(func(c, w string, id uint64, height int64, _ time.Time) (bool, error) {
				return c == cold.Address() && w == warm.Address() && id == 1 && (nonceRevoke || height < 101), nil
			}))
			require.NoError(t, err)
			height, err := sm.BuildMainnetHeightTx(context.Background())
			require.NoError(t, err)
			first := []*types.DevshardTx{height, testutil.WarmKeyBindTx(0, warm.Address(), 1)}
			root, err := sm.ApplyLocal(1, first)
			require.NoError(t, err)
			require.NoError(t, shared.AppendDiff("own-warm", types.DiffRecord{Diff: types.Diff{Nonce: 1, Txs: first}, StateHash: root}))
			h, err := NewHost(sm, warm, stub.NewInferenceEngine(), "own-warm", group, nil, WithStorage(shared), WithVerifier(signing.NewSecp256k1Verifier()), WithChainOracle(source))
			require.NoError(t, err)
			_, err = source.AdvanceOne()
			require.NoError(t, err)
			height, err = sm.BuildMainnetHeightTx(context.Background())
			require.NoError(t, err)
			second := []*types.DevshardTx{height}
			if nonceRevoke {
				second = append(second, testutil.SignedWarmKeyRevokeTx(t, 0, warm, 1, "own-warm"))
			}
			root, err = sm.ApplyLocal(2, second)
			require.NoError(t, err)
			diff := testutil.SignDiffWithRoot(t, user, "own-warm", 2, second, root)
			require.NoError(t, shared.AppendDiff("own-warm", types.DiffRecord{Diff: diff, StateHash: root}))
			content, err := proto.Marshal(&types.StateSignatureContent{StateRoot: root, EscrowId: "own-warm", Nonce: 2})
			require.NoError(t, err)
			good, err := cold.Sign(content)
			require.NoError(t, err)
			require.NoError(t, shared.AddSignature("own-warm", 2, 0, good))
			response, err := h.HandleRequest(context.Background(), HostRequest{})
			require.NoError(t, err)
			require.Empty(t, response.StateSig, "an honest replica must stop signing when its cached startup slot ownership is no longer authorized")
			sigs, err := shared.GetSignatures("own-warm", 2)
			require.NoError(t, err)
			require.Equal(t, good, sigs[0])
			recovered, err := h.ApplyRecoveredDiffs(context.Background(), []types.Diff{diff})
			require.NoError(t, err)
			require.Empty(t, recovered, "recovery must not emit the revoked replica's signature either")
		})
	}
}

func TestGossipSignatureFanoutPreservesSiblingBindingsAndValidatesQuorum(t *testing.T) {
	validator := testutil.MustGenerateKey(t)
	addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
	require.NoError(t, err)
	source, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "fanout-warm", Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}}, InitialHeight: 100, Start: time.Now().Add(-10 * time.Second), BlockInterval: time.Second})
	require.NoError(t, err)
	anchor, err := source.AdvanceOne()
	require.NoError(t, err)
	cfg := testutil.DefaultConfig(3)
	cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, anchor.BlockHash
	cold, other, a, b, user := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{cold, cold, other})
	shared := testutil.MustMemoryStore(t, "fanout-warm", user.Address(), cfg, group, 1_000_000)
	sm, err := state.NewStateMachine("fanout-warm", cfg, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), shared, state.WithVersion("v7"), state.WithMainnetHeights(source), state.WithWarmKeyHeightChecker(func(c, w string, id uint64, _ int64, _ time.Time) (bool, error) {
		return c == cold.Address() && ((w == a.Address() && id == 1) || (w == b.Address() && id == 2)), nil
	}))
	require.NoError(t, err)
	height, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	txs := []*types.DevshardTx{height, testutil.WarmKeyBindTx(0, a.Address(), 1), testutil.WarmKeyBindTx(1, b.Address(), 2)}
	root, err := sm.ApplyLocal(1, txs)
	require.NoError(t, err)
	require.NoError(t, shared.AppendDiff("fanout-warm", types.DiffRecord{Diff: types.Diff{Nonce: 1, Txs: txs}, StateHash: root}))
	content, err := proto.Marshal(&types.StateSignatureContent{StateRoot: root, EscrowId: "fanout-warm", Nonce: 1})
	require.NoError(t, err)
	sign := func(key *signing.Secp256k1Signer) []byte {
		sig, err := key.Sign(content)
		require.NoError(t, err)
		return sig
	}
	bsig, asig := sign(b), sign(a)
	require.NoError(t, shared.AddSignature("fanout-warm", 1, 1, bsig))
	h, err := NewHost(sm, cold, stub.NewInferenceEngine(), "fanout-warm", group, nil, WithStorage(shared), WithVerifier(signing.NewSecp256k1Verifier()))
	require.NoError(t, err)
	require.NoError(t, h.AccumulateGossipSig(1, root, asig, 0))
	sigs, err := shared.GetSignatures("fanout-warm", 1)
	require.NoError(t, err)
	require.Equal(t, bsig, sigs[1], "signature fanout cannot replace a sibling's differently bound signer")
	require.NoError(t, shared.AddSignature("fanout-warm", 1, 2, asig)) // model an invalid row from a stale replica
	h.mu.Lock()
	h.checkFinalization(1)
	h.mu.Unlock()
	last, err := h.LastFinalized()
	require.NoError(t, err)
	require.Zero(t, last, "three rows are not a quorum when one signer does not own that slot")
	require.NoError(t, h.AccumulateGossipSig(1, root, sign(other), 2))
	last, err = h.LastFinalized()
	require.NoError(t, err)
	require.Equal(t, uint64(1), last, "one valid signature represents all slots owned by its cold participant")
}
