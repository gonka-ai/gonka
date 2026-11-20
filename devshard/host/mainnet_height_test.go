package host

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/lightproof"
	"devshard"
	"devshard/chainoracle/blocks/headertime"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type countingHeightTip struct {
	*mockoracle.Mock
	calls atomic.Int64
}

func (o *countingHeightTip) LatestForProof(ctx context.Context) (*blocks.Header, error) {
	o.calls.Add(1)
	return o.Mock.Latest(ctx)
}

func (o *countingHeightTip) LatestForProofObservation(ctx context.Context) (*blocks.Header, time.Time, error) {
	h, err := o.LatestForProof(ctx)
	return h, time.Now().Add(time.Second), err
}

func (o *countingHeightTip) Latest(ctx context.Context) (*blocks.Header, error) {
	return o.LatestForProof(ctx)
}

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
	newSM := func(heights state.MainnetHeightSource) *state.StateMachine {
		sm, err := state.NewStateMachine("escrow-1", cfg, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), testutil.MustMemoryStore(t, "escrow-1", user.Address(), cfg, group, 1_000_000), state.WithVersion("v7"), state.WithMainnetHeights(heights))
		require.NoError(t, err)
		return sm
	}
	producer := newSM(source)
	tx, err := producer.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	txs := []*types.DevshardTx{tx, testutil.StartTx(1)}
	root, err := producer.ApplyLocal(1, txs)
	require.NoError(t, err)
	diff := testutil.SignDiffWithRoot(t, user, "escrow-1", 1, txs, root)

	observed := &countingHeightTip{Mock: source}
	freshHost, err := NewHost(newSM(observed), hostSigner, stub.NewInferenceEngine(), "escrow-1", group, nil, WithChainOracle(observed))
	require.NoError(t, err)
	request := HostRequest{Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload()}
	for i := range 2 {
		fresh, err := freshHost.HandleRequest(t.Context(), request)
		require.NoError(t, err)
		require.NotEmpty(t, fresh.StateSig)
		require.NotEmpty(t, fresh.Receipt)
		require.Equal(t, int64(i+1), observed.calls.Load(), "each request fetches a tip once")
	}

	checkpoint := freshHost.sm.MainnetCheckpoint()
	now := time.Now()
	earlier := mainnetTipObservation{
		header:     blocks.HashOnlyHeader(checkpoint.Height, checkpoint.Time, checkpoint.ChainID, checkpoint.Hash),
		receivedAt: now, validUntil: now.Add(100 * time.Millisecond),
		checkpointHeight: checkpoint.Height, checkpointHash: checkpoint.Hash,
	}
	require.Same(t, earlier.header, freshHost.reusableMainnetTip(earlier, earlier.validUntil.Add(-time.Nanosecond)))
	require.Nil(t, freshHost.reusableMainnetTip(earlier, earlier.validUntil))
	earlier.validUntil = now.Add(time.Hour)
	require.Nil(t, freshHost.reusableMainnetTip(earlier, now.Add(time.Second)))

	for i := 0; i < 4; i++ {
		_, err = source.AdvanceOne()
		require.NoError(t, err)
	}
	sm := newSM(source)
	h, err := NewHost(sm, hostSigner, stub.NewInferenceEngine(), "escrow-1", group, nil, WithChainOracle(source))
	require.NoError(t, err)
	// A delayed, authenticated Start still returns its execution receipt/job.
	// Withholding the state signature must not orphan the queued ConfirmStart.
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

func TestMainnetTerminalHistoryWithoutFreshEvidenceCannotSign(t *testing.T) {
	validator := testutil.MustGenerateKey(t)
	addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
	require.NoError(t, err)
	source, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "terminal-height", Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}}, InitialHeight: 100, Start: time.Now().Add(-10 * time.Minute), BlockInterval: time.Second})
	require.NoError(t, err)
	anchor, err := source.AdvanceOne()
	require.NoError(t, err)
	proof, cp, err := lightproof.Build(context.Background(), lightproof.Checkpoint{Height: anchor.Height, Hash: anchor.BlockHash}, anchor.Height, source)
	require.NoError(t, err)
	cfg := testutil.DefaultConfig(1)
	cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, bytes.Clone(anchor.BlockHash)
	hostSigner, user := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostSigner})
	times := headertime.New(headertime.WithFiller(headertime.OracleFiller(source, headertime.OriginChain, false)), headertime.WithLatestOracle(source), headertime.WithLightBlockSource(source))
	times.Observe(anchor, headertime.OriginComet, true)
	newSM := func() *state.StateMachine {
		sm, err := state.NewStateMachine("terminal-height", cfg, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), testutil.MustMemoryStore(t, "terminal-height", user.Address(), cfg, group, 1_000_000), state.WithVersion("v7"), state.WithMainnetHeights(source), state.WithHeaderTimes(times))
		require.NoError(t, err)
		return sm
	}
	producer := newSM()
	prelude := &types.DevshardTx{Tx: &types.DevshardTx_MainnetHeight{MainnetHeight: &types.MsgMainnetHeight{Height: cp.Height, TimeUnixNano: cp.Time.UnixNano(), LightBlocks: proof}}}
	finalize := &types.DevshardTx{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}}
	root, err := producer.ApplyLocal(1, []*types.DevshardTx{prelude, finalize})
	require.NoError(t, err)
	first := testutil.SignDiffWithRoot(t, user, "terminal-height", 1, []*types.DevshardTx{prelude, finalize}, root)
	repeated := proto.Clone(prelude).(*types.DevshardTx)
	repeated.GetMainnetHeight().LightBlocks = nil
	root, err = producer.ApplyLocal(2, []*types.DevshardTx{repeated})
	require.NoError(t, err)
	require.Equal(t, types.PhaseSettlement, producer.Phase())
	last := testutil.SignDiffWithRoot(t, user, "terminal-height", 2, []*types.DevshardTx{repeated}, root)
	hostStore := testutil.MustMemoryStore(t, "terminal-height", user.Address(), cfg, group, 1_000_000)
	h, err := NewHost(newSM(), hostSigner, stub.NewInferenceEngine(), "terminal-height", group, nil, WithStorage(hostStore), WithChainOracle(source), WithMaxNonceProvider(devshard.StaticMaxNonce(2)))
	require.NoError(t, err)
	sigs, err := h.ApplyRecoveredDiffs(context.Background(), []types.Diff{first, last})
	require.NoError(t, err)
	require.Empty(t, sigs, "a terminal phase and admitted journal still require fresh authenticated evidence")
	_, _, err = h.signState(2, root)
	require.ErrorIs(t, err, types.ErrHeaderPending)
}
