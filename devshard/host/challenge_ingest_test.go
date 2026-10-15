package host

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

// Inference 1 is executed by slot 1; slot 2 verifies its refused timeout.
type ingestFixture struct {
	hosts []*signing.Secp256k1Signer
	user  *signing.Secp256k1Signer
	start types.Diff
}

func newIngestFixture(t *testing.T) *ingestFixture {
	t.Helper()
	f := &ingestFixture{
		hosts: []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)},
		user:  testutil.MustGenerateKey(t),
	}
	f.start = testutil.SignDiff(t, f.user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	return f
}

func (f *ingestFixture) host(t *testing.T, idx int) *Host {
	t.Helper()
	group := testutil.MakeGroup(f.hosts)
	config := testutil.DefaultConfig(len(f.hosts))
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, f.user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", f.user.Address(), config, group, 10000))
	require.NoError(t, err)
	h, err := NewHost(sm, f.hosts[idx], stub.NewInferenceEngine(), "escrow-1", group, nil)
	require.NoError(t, err)
	return h
}

// verifierAt returns slot 2 with the diffs applied.
func (f *ingestFixture) verifierAt(t *testing.T, diffs ...types.Diff) *Host {
	t.Helper()
	h := f.host(t, 2)
	_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: diffs, Nonce: diffs[len(diffs)-1].Nonce})
	require.NoError(t, err)
	return h
}

func (f *ingestFixture) executorConfirm(t *testing.T) *types.DevshardTx {
	t.Helper()
	executor := f.host(t, 1)
	_, _, err := executor.ChallengeReceipt(context.Background(), 1, defaultPayload(), []types.Diff{f.start})
	require.NoError(t, err)
	confirm := findMempoolConfirm(executor.MempoolTxs())
	require.NotNil(t, confirm)
	return confirm
}

// heldExecutorReply answers a refused-timeout challenge from a real executor host,
// then holds the reply until released.
type heldExecutorReply struct {
	executor *Host
	diffs    []types.Diff
	signed   chan *types.DevshardTx
	release  chan struct{}
}

func (c *heldExecutorReply) GetMempool(context.Context) ([]*types.DevshardTx, error) {
	return c.executor.MempoolTxs(), nil
}

func (c *heldExecutorReply) ChallengeReceipt(ctx context.Context, inferenceID uint64, payload *InferencePayload, _ []types.Diff) ([]byte, []*types.DevshardTx, error) {
	receipt, _, err := c.executor.ChallengeReceipt(ctx, inferenceID, payload, c.diffs)
	if err != nil {
		return nil, nil, err
	}
	mempool := c.executor.MempoolTxs()
	c.signed <- findMempoolConfirm(mempool)
	<-c.release
	return receipt, mempool, nil
}

func TestHost_RefusedChallengeAnsweredAfterItsConfirmAppliedLeavesNoCopy(t *testing.T) {
	f := newIngestFixture(t)
	verifier := f.verifierAt(t, f.start)
	ctx := context.Background()

	client := &heldExecutorReply{
		executor: f.host(t, 1),
		diffs:    []types.Diff{f.start},
		signed:   make(chan *types.DevshardTx, 1),
		release:  make(chan struct{}),
	}
	st := verifier.SnapshotState()
	type verdict struct {
		accept bool
		err    error
	}
	done := make(chan verdict, 1)
	go func() {
		accept, err := VerifyRefusedTimeout(ctx, st, 1, defaultPayload(), verifier.MempoolTxs(), client, verifier, due(verifier))
		done <- verdict{accept, err}
	}()

	// While the reply is in flight, the gateway sequences the same confirm
	// from the other verifier's reply, and this verifier applies it.
	confirm := <-client.signed
	_, err := verifier.HandleRequest(ctx, HostRequest{
		Diffs: []types.Diff{testutil.SignDiff(t, f.user, "escrow-1", 2, []*types.DevshardTx{confirm})}, Nonce: 2,
	})
	require.NoError(t, err)

	close(client.release)
	v := <-done
	require.NoError(t, v.err)
	require.False(t, v.accept, "the executor answered with a receipt")

	resp, err := verifier.HandleRequest(ctx, HostRequest{
		Diffs: []types.Diff{testutil.SignDiff(t, f.user, "escrow-1", 3, nil)}, Nonce: 3,
	})
	require.NoError(t, err)
	require.Nil(t, findMempoolConfirm(resp.Mempool), "a confirm whose diff is applied must not ride later responses")
}

func TestHost_AddTxKeepsRecoveryTxsThatCanStillApply(t *testing.T) {
	f := newIngestFixture(t)
	confirm := f.executorConfirm(t)

	pending := f.verifierAt(t, f.start)
	pending.AddTx(confirm)
	require.NotNil(t, findMempoolConfirm(pending.MempoolTxs()), "a pending record still needs its confirm")

	started := f.verifierAt(t, f.start, testutil.SignDiff(t, f.user, "escrow-1", 2, []*types.DevshardTx{confirm}))
	started.AddTx(confirm)
	require.Nil(t, findMempoolConfirm(started.MempoolTxs()), "a started record cannot take another confirm")

	finish := signedFinish(t, f.hosts[1], "escrow-1", 1, 1)
	started.AddTx(finish)
	require.NotNil(t, findMempoolFinish(started.MempoolTxs()), "a started record still needs its finish")
}
