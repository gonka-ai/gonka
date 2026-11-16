package host

import (
	"bytes"
	"context"
	"testing"
	"time"

	"common/chainoracle/blocks"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestMainnetFreshnessWithholdsSignatureWithoutAbortingReceiptOrRecovery(t *testing.T) {
	validator := testutil.MustGenerateKey(t)
	addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
	require.NoError(t, err)
	source, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "host-height", Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}}, InitialHeight: 100, Start: time.Now().Add(-10 * time.Second), BlockInterval: time.Second})
	require.NoError(t, err)
	anchor, err := source.AdvanceOne()
	require.NoError(t, err)
	cfg := testutil.DefaultConfig(1)
	cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, bytes.Clone(anchor.BlockHash)
	hostSigner, user := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostSigner})
	newSM := func() *state.StateMachine {
		sm, err := state.NewStateMachine("escrow-1", cfg, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), testutil.MustMemoryStore(t, "escrow-1", user.Address(), cfg, group, 1_000_000), state.WithVersion("v7"), state.WithMainnetHeights(source))
		require.NoError(t, err)
		return sm
	}
	producer := newSM()
	tx, err := producer.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	for i := 0; i < 4; i++ {
		_, err = source.AdvanceOne()
		require.NoError(t, err)
	}
	sm := newSM()
	h, err := NewHost(sm, hostSigner, stub.NewInferenceEngine(), "escrow-1", group, nil, WithChainOracle(source))
	require.NoError(t, err)
	// A delayed, authenticated Start still returns its execution receipt/job.
	// Withholding the state signature must not orphan the queued ConfirmStart.
	txs := []*types.DevshardTx{tx, testutil.StartTx(1)}
	root, err := producer.ApplyLocal(1, txs)
	require.NoError(t, err)
	diff := testutil.SignDiffWithRoot(t, user, "escrow-1", 1, txs, root)
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload()})
	require.NoError(t, err)
	require.Empty(t, resp.StateSig)
	require.NotEmpty(t, resp.Receipt)
	require.NotNil(t, resp.ExecutionJob)
	// Persisted catch-up crosses another old checkpoint without signing it.
	repeated := proto.Clone(tx).(*types.DevshardTx)
	repeated.GetMainnetHeight().LightBlocks = nil
	txs = []*types.DevshardTx{repeated}
	root, err = producer.ApplyLocal(2, txs)
	require.NoError(t, err)
	diff = testutil.SignDiffWithRoot(t, user, "escrow-1", 2, txs, root)
	sigs, err := h.ApplyRecoveredDiffs(context.Background(), []types.Diff{diff})
	require.NoError(t, err)
	require.Empty(t, sigs)
	require.Equal(t, uint64(2), h.LatestNonce())
	// Once the sequencer supplies a fresh checkpoint, signatures resume.
	tx, err = producer.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	txs = []*types.DevshardTx{tx}
	root, err = producer.ApplyLocal(3, txs)
	require.NoError(t, err)
	diff = testutil.SignDiffWithRoot(t, user, "escrow-1", 3, txs, root)
	resp, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
	require.NoError(t, err)
	require.NotEmpty(t, resp.StateSig)
}
