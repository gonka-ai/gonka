package rpcserver

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/gossip"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/types"
)

func newRetentionHost(t *testing.T, signer, user *signing.Secp256k1Signer) (*host.Host, *transport.Server) {
	t.Helper()
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{signer})
	cfg := testutil.DefaultConfig(1)
	store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), cfg, group, 1000000)
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", cfg, group, 1000000, user.Address(), verifier, store, state.WithVersion("v6"))
	require.NoError(t, err)
	h, err := host.NewHost(sm, signer, stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithStorage(store))
	require.NoError(t, err)
	t.Cleanup(h.Close)
	srv, err := transport.NewServer(h, store, verifier, user.Address())
	require.NoError(t, err)
	return h, srv
}

// Explicit wiring exercises gossip's retained storage. Production HostManager
// currently leaves gossip unwired; this fixture does not change that behavior.
func retentionRPC(t *testing.T, h *host.Host, srv *transport.Server, signer *signing.Secp256k1Signer) (sessionEnv, *gossip.Gossip) {
	t.Helper()
	g := gossip.NewGossip("escrow-1", 0, nil, h.HostMempool())
	srv.SetGossip(g)
	host.WithGossip(g)(h)
	lookup := AdaptLookup(func(string) (*transport.Server, error) { return srv, nil })
	return newSessionEnvWith(t, lookup, "escrow-1", signer, WithGossipService(NewGossipHandler(lookup))), g
}

func sendRetentionTxs(t *testing.T, env sessionEnv, txs []*types.DevshardTx) {
	t.Helper()
	wire := make([][]byte, len(txs))
	for i, tx := range txs {
		var err error
		wire[i], err = proto.Marshal(tx)
		require.NoError(t, err)
	}
	_, err := env.gossip.Txs(context.Background(), withSession(connect.NewRequest(env.signedEnvelope(t, "escrow-1", &rpcpb.GossipTxsRequest{Txs: wire})), env.token))
	require.NoError(t, err)
}

// These are authentic slot-owner signatures over future IDs, not completed
// inferences. A state-aware stale-tx filter alone cannot bound their retention.
func signedFutureFinishes(t *testing.T, signer *signing.Secp256k1Signer, first uint64, count int) []*types.DevshardTx {
	t.Helper()
	txs := make([]*types.DevshardTx, count)
	for i := range txs {
		msg := &types.MsgFinishInference{
			EscrowId: "escrow-1", InferenceId: first + uint64(i), ExecutorSlot: 0,
			InputTokens: 1, OutputTokens: 1,
			ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash,
		}
		msg.ProposerSig = testutil.SignProposerTx(t, signer, msg)
		txs[i] = &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
	}
	return txs
}

func TestGossipTxs_RetentionBoundAcrossMessages(t *testing.T) {
	signer, user := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	h, srv := newRetentionHost(t, signer, user)
	env, g := retentionRPC(t, h, srv, signer)
	const batchSize = host.MaxGossipMempoolTxs/2 + 1
	var last []*types.DevshardTx
	for batch := 0; batch < 3; batch++ {
		last = signedFutureFinishes(t, signer, uint64(100000+batch*batchSize), batchSize)
		sendRetentionTxs(t, env, last)
		require.Equal(t, min((batch+1)*batchSize, host.MaxGossipMempoolTxs), h.HostMempool().Len())
		if batch == 0 {
			sendRetentionTxs(t, env, last)
			require.Equal(t, batchSize, h.HostMempool().Len(), "replaying admitted entries must not consume the remaining capacity")
		}
	}
	sendRetentionTxs(t, env, last)
	g.PruneBelow(1000000)
	require.Equal(t, host.MaxGossipMempoolTxs, h.HostMempool().Len())
}

func executeRetentionInference(t *testing.T, h *host.Host, user *signing.Secp256k1Signer, nonce uint64) (*types.DevshardTx, *types.DevshardTx) {
	t.Helper()
	ctx := context.Background()
	resp, err := h.HandleRequest(ctx, host.HostRequest{
		Diffs: []types.Diff{testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{testutil.StartTx(nonce)})},
		Nonce: nonce,
		Payload: &host.InferencePayload{
			Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100,
			MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.ExecutionJob)
	_, err = h.RunExecution(ctx, resp.ExecutionJob)
	require.NoError(t, err)
	var confirm, finish *types.DevshardTx
	for _, tx := range h.MempoolTxs() {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == nonce {
			confirm = tx
		}
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == nonce {
			finish = tx
		}
	}
	require.NotNil(t, confirm)
	require.NotNil(t, finish)
	return confirm, finish
}

func applyRetentionDiff(t *testing.T, h *host.Host, user *signing.Secp256k1Signer, nonce uint64, txs ...*types.DevshardTx) {
	t.Helper()
	resp, err := h.HandleRequest(context.Background(), host.HostRequest{
		Diffs: []types.Diff{testutil.SignDiff(t, user, "escrow-1", nonce, txs)},
		Nonce: nonce,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.StateSig)
}

func TestGossipTxs_FullPoolPreservesExecutionAndRecovery(t *testing.T) {
	signer, user := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	producer, _ := newRetentionHost(t, signer, user)
	confirm, finish := executeRetentionInference(t, producer, user, 1)
	h, srv := newRetentionHost(t, signer, user)
	env, _ := retentionRPC(t, h, srv, signer)
	applyRetentionDiff(t, h, user, 1, testutil.StartTx(1))
	require.Zero(t, h.HostMempool().Len())

	// The first imported entry can actually be included by a signed diff.
	// Fill the rest with unresolved future IDs via the authenticated RPC.
	sendRetentionTxs(t, env, []*types.DevshardTx{confirm})
	sendRetentionTxs(t, env, signedFutureFinishes(t, signer, 100000, host.MaxGossipMempoolTxs))
	require.Equal(t, host.MaxGossipMempoolTxs, h.HostMempool().Len())

	// This is the post-verification challenge-recovery ingress. Its valid
	// Finish must survive even while untrusted gossip has exhausted its budget.
	h.AddTx(finish)
	require.True(t, h.HostMempool().HasFinish(1))
	require.Equal(t, host.MaxGossipMempoolTxs+1, h.HostMempool().Len())

	// Applying the imported Confirm releases one gossip slot. A new RPC
	// message can use it, while the protected recovery Finish stays queued.
	applyRetentionDiff(t, h, user, 2, confirm)
	require.Equal(t, host.MaxGossipMempoolTxs, h.HostMempool().Len())
	sendRetentionTxs(t, env, signedFutureFinishes(t, signer, 500000, 1))
	require.True(t, h.HostMempool().HasFinish(500000))
	require.Equal(t, host.MaxGossipMempoolTxs+1, h.HostMempool().Len())
	applyRetentionDiff(t, h, user, 3, finish)
	require.Equal(t, types.StatusFinished, h.SnapshotState().Inferences[1].Status)
	require.Equal(t, host.MaxGossipMempoolTxs, h.HostMempool().Len())

	// Actual local execution also publishes its receipt and result at capacity.
	localConfirm, localFinish := executeRetentionInference(t, h, user, 4)
	require.Equal(t, host.MaxGossipMempoolTxs+2, h.HostMempool().Len())
	sendRetentionTxs(t, env, []*types.DevshardTx{localConfirm, localFinish})
	require.NotNil(t, h.HostMempool().QueuedConfirmStart(4), "gossip replay must not overwrite the local receipt marker")
	stale := h.HostMempool().StaleFinishes(10, 0)
	require.Len(t, stale, 1)
	require.Equal(t, uint64(4), stale[0].GetFinishInference().InferenceId)
	applyRetentionDiff(t, h, user, 5, localConfirm)
	applyRetentionDiff(t, h, user, 6, localFinish)
	require.Equal(t, types.StatusFinished, h.SnapshotState().Inferences[4].Status)
	require.Equal(t, host.MaxGossipMempoolTxs, h.HostMempool().Len())
}
