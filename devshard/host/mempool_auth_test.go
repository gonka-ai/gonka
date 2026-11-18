package host

import (
	"common/chainoracle/blocks"
	"context"
	"devshard/bridge"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
	"errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"sync/atomic"
	"testing"
	"time"
)

type resetGrantFetcher struct {
	entry bridge.WarmKeyEntry
	down  atomic.Bool
}

func (f *resetGrantFetcher) FetchWarmKeyLog(context.Context, string) ([]bridge.WarmKeyEntry, error) {
	if f.down.Load() {
		return nil, errors.New("chain RPC unavailable")
	}
	return []bridge.WarmKeyEntry{f.entry}, nil
}
func (f *resetGrantFetcher) FetchWarmKeyLogWithHeight(ctx context.Context, cold string) ([]bridge.WarmKeyEntry, int64, error) {
	entries, err := f.FetchWarmKeyLog(ctx, cold)
	return entries, 100, err
}
func (f *resetGrantFetcher) FetchWarmKeyEntry(ctx context.Context, cold string, id uint64) (bridge.WarmKeyEntry, bool, error) {
	_, err := f.FetchWarmKeyLog(ctx, cold)
	return f.entry, id == f.entry.ID, err
}
func TestEventsResetRetainsUnresolvedQueuedWarmProposals(t *testing.T) {

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
	fetch := &resetGrantFetcher{entry: bridge.WarmKeyEntry{Granter: cold.Address(), Grantee: warm.Address(), ID: 1, GrantedAtHeight: 100}}
	grants := bridge.NewWarmKeyLogCopy(fetch, time.Hour)
	sm, err := state.NewStateMachine("live-slots", cfg, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), shared, state.WithVersion("v7"), state.WithMainnetHeights(source), state.WithWarmKeyHeightChecker(func(c, w string, id uint64, h int64, at time.Time) (bool, error) {
		return bridge.CheckWarmKeyAtHeight(grants, c, w, id, h, at)
	}), state.WithWarmKeyResolver(func(w, c string) (bool, error) { return bridge.IsWarmKeyLive(grants, c, w, time.Now()) }))
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
	h, err := NewHost(sm, warm, stub.NewInferenceEngine(), "live-slots", group, nil, WithStorage(shared), WithVerifier(signing.NewSecp256k1Verifier()), WithChainOracle(source))
	require.NoError(t, err)
	t.Cleanup(h.Close)

	finish := &types.MsgFinishInference{InferenceId: 99, ExecutorSlot: 1, EscrowId: "live-slots", ResponseHash: []byte("completed result"), InputTokens: 1, OutputTokens: 1}
	finish.ProposerSig, err = h.signProposer(finish)
	require.NoError(t, err)
	validation := &types.MsgValidation{InferenceId: 99, ValidatorSlot: 0, Valid: true, EscrowId: "live-slots"}
	validation.ProposerSig, err = h.signProposer(validation)
	require.NoError(t, err)
	h.HostMempool().Add(MempoolEntry{Tx: &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: finish}}, ProposedAt: 1})
	h.HostMempool().Add(MempoolEntry{Tx: &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: validation}}, ProposedAt: 1})
	originals := make(map[uint64][]byte)
	for _, tx := range h.MempoolTxs() {
		wire, err := proto.Marshal(tx)
		require.NoError(t, err)
		originals[types.TxHash(tx)] = wire
	}
	require.Len(t, originals, 2)
	fetch.down.Store(true)
	grants.Reset()
	require.Empty(t, h.MempoolTxs(), "unknown grant evidence must not authorize a live proposal")
	require.Len(t, h.HostMempool().Txs(), 2, "RPC failure must retain the original signed results for retry")
	fetch.down.Store(false)
	recovered := h.MempoolTxs()
	require.Len(t, recovered, 2)
	for _, tx := range recovered {
		wire, err := proto.Marshal(tx)
		require.NoError(t, err)
		require.Equal(t, originals[types.TxHash(tx)], wire)
	}
	futureID := sm.LatestNonce() + 2
	future := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: futureID, ExecutorSig: testutil.SignExecutorReceipt(t, warm, "live-slots", futureID, testutil.TestPromptHash[:], "llama", 100, testutil.TestMaxTokens)}}}
	h.HostMempool().Add(MempoolEntry{Tx: future})
	require.Len(t, h.MempoolTxs(), 2, "a receipt without its future start is not usable yet")
	require.Len(t, h.HostMempool().Txs(), 3, "future context must be retained rather than declared invalid")
	apply()
	apply(testutil.StartTx(futureID))
	require.Len(t, h.MempoolTxs(), 3, "the same receipt becomes usable after its start is applied")
	apply(testutil.SignedWarmKeyRevokeTx(t, 0, warm, 1, "live-slots"))
	fetch.down.Store(true)
	grants.Reset()
	require.Empty(t, h.MempoolTxs())
	remaining := h.HostMempool().Txs()
	require.Len(t, remaining, 1, "a known nonce revoke must not be confused with unavailable sibling evidence")
	require.NotNil(t, remaining[0].GetFinishInference(), "the unresolved slot-1 Finish is retained")
	fetch.down.Store(false)
	require.Len(t, h.MempoolTxs(), 1)
	apply(testutil.SignedWarmKeyRevokeTx(t, 1, warm, 1, "live-slots"))
	require.Empty(t, h.MempoolTxs(), "definitive nonce revocation still discards invalid proposals")
	require.Empty(t, h.HostMempool().Txs())
}
