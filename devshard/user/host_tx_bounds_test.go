package user

import (
	"bytes"
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/stub"
	"devshard/types"
)

const boundsProposer = 5

type boundsProposingClient struct {
	*InProcessClient
	extra atomic.Pointer[[]*types.DevshardTx]
}

func (c *boundsProposingClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	resp, err := c.InProcessClient.Send(ctx, req, stream, receiptHandler)
	if err != nil || resp == nil {
		return resp, err
	}
	if txs := c.extra.Swap(nil); txs != nil {
		resp.Mempool = append(resp.Mempool, *txs...)
	}
	return resp, nil
}

type boundsRecordingClient struct {
	*InProcessClient
	mu       sync.Mutex
	requests [][]types.Diff
}

func (c *boundsRecordingClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	c.mu.Lock()
	c.requests = append(c.requests, append([]types.Diff(nil), req.Diffs...))
	c.mu.Unlock()
	return c.InProcessClient.Send(ctx, req, stream, receiptHandler)
}

type boundsFixture struct {
	session  *Session
	hosts    []*signing.Secp256k1Signer
	proposer *boundsProposingClient
	recorded []*boundsRecordingClient
	diffsMu  sync.Mutex
	diffs    []types.Diff
}

func (f *boundsFixture) sealedDiffs() []types.Diff {
	f.diffsMu.Lock()
	defer f.diffsMu.Unlock()
	return append([]types.Diff(nil), f.diffs...)
}

func newBoundsFixture(t *testing.T) *boundsFixture {
	t.Helper()
	const numHosts = 16
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	f := &boundsFixture{hosts: hosts}
	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, 10_000_000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(10))
		require.NoError(t, err)
		if i == boundsProposer {
			f.proposer = &boundsProposingClient{InProcessClient: &InProcessClient{Host: h}}
			clients[i] = f.proposer
			continue
		}
		rc := &boundsRecordingClient{InProcessClient: &InProcessClient{Host: h}}
		f.recorded = append(f.recorded, rc)
		clients[i] = rc
	}
	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, 10_000_000, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier)
	require.NoError(t, err)
	f.session = session
	session.SetDiffObserver(func(d types.Diff) {
		f.diffsMu.Lock()
		f.diffs = append(f.diffs, d)
		f.diffsMu.Unlock()
	})
	return f
}

func boundsParams() InferenceParams {
	return InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
}

func (f *boundsFixture) send(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		_, err := f.session.SendInference(context.Background(), boundsParams())
		require.NoError(t, err)
	}
}

func (f *boundsFixture) finishedByOther(t *testing.T) uint64 {
	t.Helper()
	for id := uint64(1); id <= f.session.Nonce(); id++ {
		rec, ok := f.session.StateMachine().GetInference(id)
		if ok && rec.Status == types.StatusFinished && rec.ExecutorSlot != boundsProposer {
			return id
		}
	}
	t.Fatal("no finished inference served by another slot")
	return 0
}

func (f *boundsFixture) finalize(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	require.NoError(t, f.session.Finalize(ctx))
}

func unknownPad(n int) []byte {
	pad := protowire.AppendTag(nil, 1000, protowire.BytesType)
	return protowire.AppendBytes(pad, make([]byte, n))
}

func (f *boundsFixture) proposeAndWatch(t *testing.T, tx *types.DevshardTx, match func(*types.DevshardTx) bool) (sealed []*types.DevshardTx, largestDiff int) {
	t.Helper()
	f.proposer.extra.Store(&[]*types.DevshardTx{tx})
	f.send(t, 48)
	require.Nil(t, f.proposer.extra.Load(), "the proposer returned its tx")
	for _, diff := range f.sealedDiffs() {
		largestDiff = max(largestDiff, diffWireBytes(diff))
		for _, dtx := range diff.Txs {
			if match(dtx) {
				sealed = append(sealed, dtx)
			}
		}
	}
	return sealed, largestDiff
}

func proposerValidation(target uint64) func(*types.DevshardTx) bool {
	return func(tx *types.DevshardTx) bool {
		v := tx.GetValidation()
		return v != nil && v.InferenceId == target && v.ValidatorSlot == boundsProposer
	}
}

func TestCanonicalHostTx_StripsUnknownFieldsAtEveryLevel(t *testing.T) {
	msg := &types.MsgValidation{InferenceId: 7, ValidatorSlot: 2, Valid: true, EscrowId: "escrow-1"}
	msg.ProtoReflect().SetUnknown(unknownPad(64))
	tx := &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: msg}}
	tx.ProtoReflect().SetUnknown(unknownPad(32))
	before := proto.Size(tx)

	clean := canonicalHostTx(tx)

	require.NotSame(t, tx, clean)
	require.Empty(t, clean.ProtoReflect().GetUnknown())
	require.Empty(t, clean.GetValidation().ProtoReflect().GetUnknown())
	require.True(t, proto.Equal(&types.MsgValidation{InferenceId: 7, ValidatorSlot: 2, Valid: true, EscrowId: "escrow-1"}, clean.GetValidation()))
	require.Equal(t, before, proto.Size(tx), "the received tx is left as it was")

	plain := &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{InferenceId: 7}}}
	require.Same(t, plain, canonicalHostTx(plain))
	require.Nil(t, canonicalHostTx(nil))
}

func TestCanonicalHostTx_StripsUnknownFieldsInRepeatedMessages(t *testing.T) {
	vote := &types.TimeoutVote{VoterSlot: 1, Accept: true}
	vote.ProtoReflect().SetUnknown(unknownPad(16))
	tx := &types.DevshardTx{Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{
		InferenceId: 3, Votes: []*types.TimeoutVote{vote},
	}}}

	clean := canonicalHostTx(tx)

	require.Empty(t, clean.GetTimeoutInference().GetVotes()[0].ProtoReflect().GetUnknown())
	require.NotEmpty(t, vote.ProtoReflect().GetUnknown())
}

func TestHostMayProposeTx_RefusesRevealSeed(t *testing.T) {
	require.False(t, hostMayProposeTx(&types.DevshardTx{Tx: &types.DevshardTx_RevealSeed{
		RevealSeed: &types.MsgRevealSeed{SlotId: 2, EscrowId: "escrow-1"},
	}}))
	require.True(t, hostMayProposeTx(&types.DevshardTx{Tx: &types.DevshardTx_Validation{
		Validation: &types.MsgValidation{InferenceId: 1},
	}}))
}

func TestHostProposedTx_SignedOversizedUnknownFieldIsNotSealed(t *testing.T) {
	f := newBoundsFixture(t)
	f.send(t, 32)
	target := f.finishedByOther(t)
	msg := &types.MsgValidation{InferenceId: target, ValidatorSlot: boundsProposer, Valid: true, EscrowId: "escrow-1"}
	msg.ProtoReflect().SetUnknown(unknownPad(8 << 20))
	msg.ProposerSig = testutil.SignProposerTx(t, f.hosts[boundsProposer], msg)

	sealed, largest := f.proposeAndWatch(t, &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: msg}}, proposerValidation(target))

	require.Empty(t, sealed)
	require.Less(t, largest, maxCatchUpWireBytes)
	f.finalize(t)
}

func TestHostProposedTx_UnknownFieldAddedAfterSigningIsStrippedBeforeSealing(t *testing.T) {
	f := newBoundsFixture(t)
	f.send(t, 32)
	target := f.finishedByOther(t)
	msg := &types.MsgValidation{InferenceId: target, ValidatorSlot: boundsProposer, Valid: true, EscrowId: "escrow-1"}
	msg.ProposerSig = testutil.SignProposerTx(t, f.hosts[boundsProposer], msg)
	padded := proto.Clone(msg).(*types.MsgValidation)
	padded.ProtoReflect().SetUnknown(unknownPad(1 << 10))

	sealed, _ := f.proposeAndWatch(t, &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: padded}}, proposerValidation(target))

	require.Len(t, sealed, 1)
	require.Empty(t, sealed[0].GetValidation().ProtoReflect().GetUnknown())
	require.True(t, proto.Equal(msg, sealed[0].GetValidation()))
	f.finalize(t)
}

func TestHostProposedTx_SignatureOverUnknownFieldFailsAfterStripping(t *testing.T) {
	f := newBoundsFixture(t)
	f.send(t, 32)
	target := f.finishedByOther(t)
	msg := &types.MsgValidation{InferenceId: target, ValidatorSlot: boundsProposer, Valid: true, EscrowId: "escrow-1"}
	msg.ProtoReflect().SetUnknown(unknownPad(256))
	msg.ProposerSig = testutil.SignProposerTx(t, f.hosts[boundsProposer], msg)
	require.LessOrEqual(t, proto.Size(&types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: msg}}), maxHostTxBytes)

	sealed, _ := f.proposeAndWatch(t, &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: msg}}, proposerValidation(target))

	require.Empty(t, sealed)
	f.finalize(t)
}

func TestHostProposedTx_OversizedKnownFieldIsNotSealed(t *testing.T) {
	f := newBoundsFixture(t)
	f.send(t, 32)
	rs := &types.MsgRevealSeed{SlotId: boundsProposer, EscrowId: "escrow-1", Signature: bytes.Repeat([]byte{1}, 8<<20)}
	isRevealSeed := func(tx *types.DevshardTx) bool { return tx.GetRevealSeed() != nil }

	sealed, largest := f.proposeAndWatch(t, &types.DevshardTx{Tx: &types.DevshardTx_RevealSeed{RevealSeed: rs}}, isRevealSeed)

	require.Empty(t, sealed)
	require.Less(t, largest, maxCatchUpWireBytes)
	f.finalize(t)
}

func TestAddPendingFromHost_DropsTxOverSizeCap(t *testing.T) {
	f := newBoundsFixture(t)
	f.send(t, 32)
	target := f.finishedByOther(t)
	msg := &types.MsgValidation{InferenceId: target, ValidatorSlot: boundsProposer, Valid: true, EscrowId: "escrow-1", ProposerSig: bytes.Repeat([]byte{1}, maxHostTxBytes)}
	tx := &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: msg}}
	require.Greater(t, proto.Size(tx), maxHostTxBytes)

	s := f.session
	s.mu.Lock()
	before := len(s.pendingTxs)
	s.addPendingFromHostLocked(boundsProposer, nil, tx)
	after := len(s.pendingTxs)
	queued := s.txKeyQueuedOrApplied(devshardTxKey(tx))
	s.mu.Unlock()

	require.Equal(t, before, after)
	require.False(t, queued, "a dropped oversized copy must not hold the dedup key")
}

func TestBoundAppliedToBudget_CountsOnlyAppliedTxsAndHoldsFromFirstThatDoesNotFit(t *testing.T) {
	mk := func(id uint64, sigLen int) *types.DevshardTx {
		return &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{
			InferenceId: id, ProposerSig: make([]byte, sigLen),
		}}}
	}
	a, junk, b, c, junk2 := mk(1, 10), mk(9, 5000), mk(2, 100), mk(3, 1), mk(8, 5000)
	applied := map[*types.DevshardTx]struct{}{a: {}, b: {}, c: {}}
	budget := proto.Size(a) + proto.Size(c)

	kept, held := boundAppliedToBudget([]*types.DevshardTx{a, junk, b, c, junk2}, applied, nil, budget)

	require.Equal(t, []*types.DevshardTx{a, junk, junk2}, kept)
	require.Equal(t, []*types.DevshardTx{b, c}, held)

	kept, held = boundAppliedToBudget([]*types.DevshardTx{a, junk, c}, applied, nil, budget)
	require.Equal(t, []*types.DevshardTx{a, junk, c}, kept)
	require.Empty(t, held)

	mandatory := map[*types.DevshardTx]struct{}{b: {}}
	kept, held = boundAppliedToBudget([]*types.DevshardTx{a, b, c}, applied, mandatory, proto.Size(b)+proto.Size(a))
	require.Equal(t, []*types.DevshardTx{a, b}, kept, "a mandatory tx is always kept and counted first")
	require.Equal(t, []*types.DevshardTx{c}, held)
}

func TestComposeDiff_ErrorMissKeepsItsPinnedFinishUnderTightBudget(t *testing.T) {
	s, keys, _ := setupSession(t, 3, 1000000, 100)
	id, executor := startConfirmForErrorMiss(t, s, keys, finalizeTestParams())
	payload := errorMissPayload(t)
	finish, raw := signedErrorFinishTx(t, keys, id, executor, payload)
	require.NoError(t, s.ProcessResponse(executor, &host.HostResponse{Mempool: []*types.DevshardTx{finish}}, id))
	var votes []*types.ErrorMissVote
	for i := range keys {
		if i != executor {
			votes = append(votes, &types.ErrorMissVote{VoterSlot: uint32(i), Accept: true, Signature: make([]byte, 65)})
		}
	}
	old := maxDiffTxBytes
	maxDiffTxBytes = max(proto.Size(errorMissTx(id, votes)), proto.Size(finish))
	t.Cleanup(func() { maxDiffTxBytes = old })
	wrapApplyingErrorMissVerifiers(s, keys)

	result, err := s.HandleErrorMiss(context.Background(), id, raw, payload)

	require.ErrorIs(t, err, ErrInferenceMissed)
	require.True(t, result.Accepted)
}

func TestProcessResponse_FinishSignedOverUnknownFieldDoesNotMarkNonceFinished(t *testing.T) {
	s, hosts, _ := setupSession(t, 3, 1000000, 100)
	nonce, executor := startConfirmForErrorMiss(t, s, hosts, finalizeTestParams())
	require.Nil(t, pendingFinishFor(s, nonce))
	msg := finishTxFor(t, hosts[executor], nonce, uint32(executor), testutil.TestResponseHash).GetFinishInference()
	msg.ProposerSig = nil
	msg.ProtoReflect().SetUnknown(unknownPad(256))
	msg.ProposerSig = testutil.SignProposerTx(t, hosts[executor], msg)
	tx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}

	require.NoError(t, s.ProcessResponse(executor, &host.HostResponse{Mempool: []*types.DevshardTx{tx}}, nonce))

	require.Nil(t, pendingFinishFor(s, nonce))
	require.False(t, s.IsNonceFinished(nonce), "a Finish the gateway rejected must not close the nonce")
	require.NotEmpty(t, msg.ProtoReflect().GetUnknown(), "the response object is left as it was")
}

func TestProcessResponse_FinishWithUnknownFieldAddedAfterSigningIsQueuedClean(t *testing.T) {
	s, hosts, _ := setupSession(t, 3, 1000000, 100)
	nonce, executor := startConfirmForErrorMiss(t, s, hosts, finalizeTestParams())
	msg := finishTxFor(t, hosts[executor], nonce, uint32(executor), testutil.TestResponseHash).GetFinishInference()
	padded := proto.Clone(msg).(*types.MsgFinishInference)
	padded.ProtoReflect().SetUnknown(unknownPad(256))
	tx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: padded}}

	require.NoError(t, s.ProcessResponse(executor, &host.HostResponse{Mempool: []*types.DevshardTx{tx}}, nonce))

	queued := pendingFinishFor(s, nonce)
	require.NotNil(t, queued)
	require.Empty(t, queued.ProtoReflect().GetUnknown())
	require.Empty(t, queued.GetFinishInference().ProtoReflect().GetUnknown())
	require.True(t, proto.Equal(msg, queued.GetFinishInference()))
	require.True(t, s.IsNonceFinished(nonce))
}

func TestComposeDiff_InvalidHostTxsOverBudgetDoNotSpendExtraNonces(t *testing.T) {
	f := newTimeoutVoteFixture(t)
	s := f.session
	p, err := s.PrepareInference(context.Background(), finalizeTestParams())
	require.NoError(t, err)
	txs := make([]*types.DevshardTx, 1700)
	total := 0
	for i := range txs {
		txs[i] = &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{
			InferenceId: p.Nonce(), ValidatorSlot: uint32(100 + i), EscrowId: "escrow-1",
			ProposerSig: bytes.Repeat([]byte{1}, 1900),
		}}}
		require.LessOrEqual(t, proto.Size(txs[i]), maxHostTxBytes)
		total += proto.Size(txs[i])
	}
	require.Greater(t, total, maxDiffTxBytes)
	require.NoError(t, s.ProcessResponse(0, &host.HostResponse{Mempool: txs}, p.Nonce()))
	require.Len(t, s.PendingTxs(), len(txs))
	beforeNonce := s.Nonce()

	require.NoError(t, s.DrainUnpinnedPending(context.Background()))

	require.Equal(t, beforeNonce+1, s.Nonce(), "txs that fail apply must not push work into extra paid nonces")
	require.Empty(t, s.PendingTxs())
}

func TestComposeDiff_TxBytesStayWithinDiffBudget(t *testing.T) {
	old := maxDiffTxBytes
	maxDiffTxBytes = 600
	t.Cleanup(func() { maxDiffTxBytes = old })

	f := newBoundsFixture(t)
	f.send(t, 32)
	var txs []*types.DevshardTx
	targets := make(map[uint64]bool)
	for id := uint64(1); id <= f.session.Nonce() && len(txs) < 8; id++ {
		rec, ok := f.session.StateMachine().GetInference(id)
		if !ok || rec.Status != types.StatusFinished || rec.ExecutorSlot == boundsProposer {
			continue
		}
		msg := &types.MsgValidation{InferenceId: id, ValidatorSlot: boundsProposer, Valid: true, EscrowId: "escrow-1"}
		msg.ProposerSig = testutil.SignProposerTx(t, f.hosts[boundsProposer], msg)
		txs = append(txs, &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: msg}})
		targets[id] = true
	}
	require.Len(t, txs, 8)
	f.proposer.extra.Store(&txs)
	f.send(t, 48)
	require.Nil(t, f.proposer.extra.Load(), "the proposer returned its txs")

	sealed := make(map[uint64]bool)
	for _, diff := range f.sealedDiffs() {
		total := 0
		for _, tx := range diff.Txs {
			total += proto.Size(tx)
			if v := tx.GetValidation(); v != nil && v.ValidatorSlot == boundsProposer && targets[v.InferenceId] {
				sealed[v.InferenceId] = true
			}
		}
		require.LessOrEqual(t, total, maxDiffTxBytes, "diff %d", diff.Nonce)
	}
	require.Len(t, sealed, len(targets), "txs over the budget wait for a later diff instead of being lost")
	f.finalize(t)
}

func TestWireBudgetPrefix_KeepsAtLeastOneDiff(t *testing.T) {
	d := types.Diff{Nonce: 1, Txs: []*types.DevshardTx{{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{ProposerSig: make([]byte, 1000)}}}}}
	one := diffWireBytes(d)

	require.Equal(t, 1, wireBudgetPrefix([]types.Diff{d, d, d}, one-1))
	require.Equal(t, 2, wireBudgetPrefix([]types.Diff{d, d, d}, 2*one))
	require.Equal(t, 3, wireBudgetPrefix([]types.Diff{d, d, d}, 3*one))
	require.Equal(t, 0, wireBudgetPrefix(nil, one))
}

func TestCatchUp_RequestsStayWithinWireBudget(t *testing.T) {
	old := maxCatchUpWireBytes
	maxCatchUpWireBytes = 4 << 10
	t.Cleanup(func() { maxCatchUpWireBytes = old })

	f := newBoundsFixture(t)
	f.send(t, 64)
	f.finalize(t)

	multi := 0
	for _, rc := range f.recorded {
		rc.mu.Lock()
		for _, diffs := range rc.requests {
			if len(diffs) < 2 {
				continue
			}
			multi++
			total := 0
			for _, d := range diffs {
				total += diffWireBytes(d)
			}
			require.LessOrEqual(t, total, maxCatchUpWireBytes)
		}
		rc.mu.Unlock()
	}
	require.Positive(t, multi, "catch-up still batches diffs that fit together")

	final := f.session.Nonce()
	weight, _, _ := f.session.CollectSignatures(context.Background(), final)
	require.GreaterOrEqual(t, weight, f.session.StateMachine().QuorumThreshold())
}

func signedFinishWithObservedHash(t *testing.T, s *Session, key *signing.Secp256k1Signer, id uint64, executor int, hashLen int) *types.DevshardTx {
	t.Helper()
	msg := finishTxFor(t, key, id, uint32(executor), testutil.TestResponseHash).GetFinishInference()
	msg.ProposerSig = nil
	msg.ObservedHeight = 1
	msg.ObservedBlockHash = bytes.Repeat([]byte{1}, hashLen)
	msg.ProposerSig = testutil.SignProposerTx(t, key, msg)
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
}

func TestCheckEvidence_RejectsFinishWithOversizedObservedBlockHash(t *testing.T) {
	s, keys, _ := setupSession(t, 3, 1000000, 100)
	id, executor := startConfirmForErrorMiss(t, s, keys, finalizeTestParams())
	rec, ok := s.StateMachine().Inference(id)
	require.True(t, ok)

	for _, n := range []int{33, 3000} {
		err := s.StateMachine().CheckEvidence(rec, signedFinishWithObservedHash(t, s, keys[executor], id, executor, n))
		require.ErrorIs(t, err, types.ErrInvalidFinishHash, "observed hash of %d bytes", n)
	}
	err := s.StateMachine().CheckEvidence(rec, signedFinishWithObservedHash(t, s, keys[executor], id, executor, 32))
	require.NotErrorIs(t, err, types.ErrInvalidFinishHash)
}

func TestProcessResponse_FinishWithOversizedObservedBlockHashDoesNotMarkNonceFinished(t *testing.T) {
	s, keys, _ := setupSession(t, 3, 1000000, 100)
	id, executor := startConfirmForErrorMiss(t, s, keys, finalizeTestParams())
	tx := signedFinishWithObservedHash(t, s, keys[executor], id, executor, 3000)
	require.Greater(t, proto.Size(tx), maxHostTxBytes)

	require.NoError(t, s.ProcessResponse(executor, &host.HostResponse{Mempool: []*types.DevshardTx{tx}}, id))

	require.Nil(t, pendingFinishFor(s, id))
	require.False(t, s.IsNonceFinished(id))
}

func TestAdmissibleHostMempool_StripsAndDropsOversizedWithoutTouchingTheResponse(t *testing.T) {
	small := &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{InferenceId: 1}}}
	padded := &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{InferenceId: 2}}}
	padded.GetValidation().ProtoReflect().SetUnknown(unknownPad(64))
	big := &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{InferenceId: 3, ProposerSig: make([]byte, maxHostTxBytes)}}}
	resp := &host.HostResponse{Nonce: 7, Mempool: []*types.DevshardTx{small, padded, big, nil}}

	out := admissibleHostMempool(resp)

	require.NotSame(t, resp, out)
	require.Equal(t, uint64(7), out.Nonce)
	require.Len(t, out.Mempool, 3)
	require.Same(t, small, out.Mempool[0])
	require.Empty(t, out.Mempool[1].GetValidation().ProtoReflect().GetUnknown())
	require.Nil(t, out.Mempool[2])
	require.Len(t, resp.Mempool, 4)
	require.NotEmpty(t, padded.GetValidation().ProtoReflect().GetUnknown())

	clean := &host.HostResponse{Mempool: []*types.DevshardTx{small}}
	require.Same(t, clean, admissibleHostMempool(clean))
}
