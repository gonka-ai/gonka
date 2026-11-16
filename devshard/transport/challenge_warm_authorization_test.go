package transport

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestChallengeResponseDoesNotHideUnresolvedCompletedWarmWork(t *testing.T) {
	validator := testutil.MustGenerateKey(t)
	addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
	require.NoError(t, err)
	oracle, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "warm-response", Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}}, InitialHeight: 100, Start: time.Now().Add(-10 * time.Second), BlockInterval: time.Second})
	require.NoError(t, err)
	anchor, err := oracle.AdvanceOne()
	require.NoError(t, err)
	cold, warm, user := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{cold})
	cfg := testutil.DefaultConfig(1)
	cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, anchor.BlockHash
	store := testutil.MustMemoryStore(t, "warm-response", user.Address(), cfg, group, 1_000_000)
	var down atomic.Bool
	sm, err := state.NewStateMachine("warm-response", cfg, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), store, state.WithVersion("v7"), state.WithMainnetHeights(oracle), state.WithWarmKeyHeightChecker(func(string, string, uint64, int64, time.Time) (bool, error) {
		if down.Load() {
			return false, fmt.Errorf("reset and unavailable grants")
		}
		return true, nil
	}))
	require.NoError(t, err)
	height, err := sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	txs := []*types.DevshardTx{height, testutil.WarmKeyBindTx(0, warm.Address(), 1), testutil.StartTx(1)}
	root, err := sm.ApplyLocal(1, txs)
	require.NoError(t, err)
	diff := testutil.SignDiffWithRoot(t, user, "warm-response", 1, txs, root)
	require.NoError(t, store.AppendDiff("warm-response", types.DiffRecord{Diff: diff, StateHash: root}))
	h, err := host.NewHost(sm, warm, stub.NewInferenceEngine(), "warm-response", group, nil, host.WithStorage(store), host.WithVerifier(signing.NewSecp256k1Verifier()), host.WithChainOracle(oracle))
	require.NoError(t, err)
	t.Cleanup(h.Close)
	response, err := h.HandleRequest(context.Background(), host.HostRequest{Diffs: []types.Diff{diff}, Nonce: 1, Payload: &host.InferencePayload{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens}})
	require.NoError(t, err)
	require.NotNil(t, response.ExecutionJob)
	_, err = h.RunExecution(context.Background(), response.ExecutionJob)
	require.NoError(t, err)
	// A cold Finish alone cannot replace its missing warm ConfirmStart while
	// the record is still pending. Partial evidence must also report pending.
	var coldFinish *types.DevshardTx
	var warmConfirm *types.DevshardTx
	for _, tx := range h.MempoolTxs() {
		if tx.GetConfirmStart() != nil {
			warmConfirm = proto.Clone(tx).(*types.DevshardTx)
		}
	}
	for _, tx := range h.MempoolTxs() {
		if tx.GetFinishInference() != nil {
			copy := proto.Clone(tx).(*types.DevshardTx)
			copy.GetFinishInference().ProposerSig = nil
			copy.GetFinishInference().ProposerSig = testutil.SignProposerTx(t, cold, copy.GetFinishInference())
			h.AddTx(copy)
			coldFinish = copy
			break
		}
	}
	srv, err := NewServer(h, store, signing.NewSecp256k1Verifier(), user.Address())
	require.NoError(t, err)
	down.Store(true)
	reply, err := srv.ServeChallengeReceipt(context.Background(), ChallengeReceiptRequest{InferenceID: 1})
	require.NoError(t, err)
	require.True(t, reply.EvidencePending)
	require.Empty(t, reply.Receipt)
	require.NotEmpty(t, reply.Mempool, "known cold Finish is retained alongside unresolved receipt evidence")
	// Reverse the phases: a cold receipt is ready while the warm Finish is
	// unresolved. The response must carry both that receipt and pending status.
	require.NotNil(t, coldFinish)
	require.NotNil(t, warmConfirm)
	h.HostMempool().RemoveIncluded([]*types.DevshardTx{coldFinish})
	rec, ok := sm.GetInference(1)
	require.True(t, ok)
	msg := warmConfirm.GetConfirmStart()
	msg.ExecutorSig = testutil.SignExecutorReceipt(t, cold, "warm-response", 1, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, testutil.ReceiptStamp{Height: msg.ObservedHeight, Hash: msg.ObservedBlockHash})
	h.AddTx(warmConfirm)
	reply, err = srv.ServeChallengeReceipt(context.Background(), ChallengeReceiptRequest{InferenceID: 1})
	require.NoError(t, err)
	require.True(t, reply.EvidencePending, "executor-local pending phase cannot hide an unresolved Finish from a started verifier")
	require.Equal(t, msg.ExecutorSig, reply.Receipt, "Receipt must match a surviving authorized ConfirmStart")
	decoded, err := DevshardTxsFromBytes(reply.Mempool)
	require.NoError(t, err)
	require.Len(t, decoded, 1)
	require.Equal(t, msg.ExecutorSig, decoded[0].GetConfirmStart().ExecutorSig)
	down.Store(false)
	reply, err = srv.ServeChallengeReceipt(context.Background(), ChallengeReceiptRequest{InferenceID: 1})
	require.NoError(t, err)
	require.NotEmpty(t, reply.Mempool)
}
