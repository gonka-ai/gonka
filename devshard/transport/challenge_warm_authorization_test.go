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
	for _, tx := range h.MempoolTxs() {
		if tx.GetFinishInference() != nil {
			copy := proto.Clone(tx).(*types.DevshardTx)
			copy.GetFinishInference().ProposerSig = nil
			copy.GetFinishInference().ProposerSig = testutil.SignProposerTx(t, cold, copy.GetFinishInference())
			h.AddTx(copy)
			break
		}
	}
	srv, err := NewServer(h, store, signing.NewSecp256k1Verifier(), user.Address())
	require.NoError(t, err)
	down.Store(true)
	reply, err := srv.ServeChallengeReceipt(context.Background(), ChallengeReceiptRequest{InferenceID: 1})
	require.ErrorIs(t, err, types.ErrHeaderPending, "withheld completed work must not become empty success on the actual handler")
	require.Nil(t, reply)
	down.Store(false)
	reply, err = srv.ServeChallengeReceipt(context.Background(), ChallengeReceiptRequest{InferenceID: 1})
	require.NoError(t, err)
	require.NotEmpty(t, reply.Mempool)
}
