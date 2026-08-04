package user

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

type slotClaimingVerifier struct {
	signer      *signing.Secp256k1Signer
	claimedSlot uint32
	escrowID    string
}

func (m *slotClaimingVerifier) VerifyTimeout(_ context.Context, inferenceID uint64, reason types.TimeoutReason, _ *host.InferencePayload, _ []types.Diff) (bool, []byte, uint32, error) {
	data, err := proto.Marshal(&types.TimeoutVoteContent{
		EscrowId:    m.escrowID,
		InferenceId: inferenceID,
		Reason:      reason,
		Accept:      true,
	})
	if err != nil {
		return false, nil, 0, err
	}
	sig, err := m.signer.Sign(data)
	if err != nil {
		return false, nil, 0, err
	}
	return true, sig, m.claimedSlot, nil
}

type timeoutVoteFixture struct {
	session *Session
	userSM  *state.StateMachine
	signers []*signing.Secp256k1Signer
	group   []types.SlotAssignment
	config  types.SessionConfig
}

func newTimeoutVoteFixture(t *testing.T) *timeoutVoteFixture {
	t.Helper()

	signers := make([]*signing.Secp256k1Signer, 4)
	for i := range signers {
		signers[i] = testutil.MustGenerateKey(t)
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeMultiSlotGroup(signers, []int{1, 1, 4, 1})
	config := types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		FeePerNonce:      5,
		VoteThreshold:    uint32(len(group)) / 2, // 7/2 = 3, need >3
	}
	verifier := signing.NewSecp256k1Verifier()

	clients := make([]HostClient, len(group))
	for i, slot := range group {
		var slotSigner *signing.Secp256k1Signer
		for _, s := range signers {
			if s.Address() == slot.ValidatorAddress {
				slotSigner = s
				break
			}
		}
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, userKey.Address(), verifier)
		h, err := host.NewHost(sm, slotSigner, stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, userKey.Address(), verifier)
	session, err := NewSession(userSM, userKey, "escrow-1", group, clients, verifier)
	require.NoError(t, err)

	_, err = session.SendInference(context.Background(), InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})
	require.NoError(t, err)
	require.Equal(t, types.StatusPending, userSM.SnapshotState().Inferences[1].Status)

	return &timeoutVoteFixture{session: session, userSM: userSM, signers: signers, group: group, config: config}
}

func (f *timeoutVoteFixture) dropHostReceipts() {
	f.session.mu.Lock()
	defer f.session.mu.Unlock()
	f.session.clearPendingTxs()
}

func (f *timeoutVoteFixture) collect(t *testing.T, verifiers map[int]TimeoutVerifier) []*types.TimeoutVote {
	t.Helper()
	votes, err := f.session.CollectTimeoutVotes(context.Background(), 1,
		types.TimeoutReason_TIMEOUT_REASON_REFUSED,
		&host.InferencePayload{
			Prompt: testutil.TestPrompt, Model: "llama",
			InputLength: 100, MaxTokens: 50, StartedAt: 1000,
		}, verifiers, nil)
	require.NoError(t, err)
	return votes
}

func timeoutTxFor(inferenceID uint64, votes []*types.TimeoutVote) *types.DevshardTx {
	return &types.DevshardTx{
		Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{
			InferenceId: inferenceID,
			Reason:      types.TimeoutReason_TIMEOUT_REASON_REFUSED,
			Votes:       votes,
		}},
	}
}

func TestCollectTimeoutVotes_RejectsSpoofedVoterSlot(t *testing.T) {
	f := newTimeoutVoteFixture(t)

	const spoofedSlot = uint32(2)
	require.Equal(t, f.signers[2].Address(), f.userSM.SlotAddress(spoofedSlot))
	require.Equal(t, uint32(4), f.userSM.AddressSlotCount(f.signers[2].Address()))

	votes := f.collect(t, map[int]TimeoutVerifier{
		0: &slotClaimingVerifier{signer: f.signers[0], claimedSlot: spoofedSlot, escrowID: "escrow-1"},
	})

	require.Empty(t, votes, "vote claiming another validator's slot must not be collected")
	require.False(t, f.session.HasSufficientTimeoutVotes(votes))
}

type delayedVerifier struct {
	inner TimeoutVerifier
	delay time.Duration
}

func (d *delayedVerifier) VerifyTimeout(ctx context.Context, inferenceID uint64, reason types.TimeoutReason, p *host.InferencePayload, diffs []types.Diff) (bool, []byte, uint32, error) {
	select {
	case <-time.After(d.delay):
	case <-ctx.Done():
		return false, nil, 0, ctx.Err()
	}
	return d.inner.VerifyTimeout(ctx, inferenceID, reason, p, diffs)
}

func TestCollectTimeoutVotes_SpoofedVoteDoesNotDisplaceHonestQuorum(t *testing.T) {
	f := newTimeoutVoteFixture(t)

	honest := func(signer *signing.Secp256k1Signer, slotIdx int) TimeoutVerifier {
		return &delayedVerifier{
			inner: &mockTimeoutVerifier{accept: true, signer: signer, group: f.group, slotIdx: slotIdx},
			delay: 50 * time.Millisecond,
		}
	}

	// Executor is group[1 % 7] = signers[1], which CollectTimeoutVotes excludes.
	votes := f.collect(t, map[int]TimeoutVerifier{
		0: &slotClaimingVerifier{signer: f.signers[0], claimedSlot: 2, escrowID: "escrow-1"}, // spoofer
		2: honest(f.signers[2], 2),
		6: honest(f.signers[3], 6),
	})

	require.NotEmpty(t, votes, "honest votes are still collected after the spoofed one is dropped")
	for _, v := range votes {
		require.NotEqual(t, f.signers[0].Address(), f.userSM.SlotAddress(v.VoterSlot),
			"no vote may be credited to the spoofing responder")
	}
	require.True(t, f.session.HasSufficientTimeoutVotes(votes), "honest weight exceeds threshold 3")

	before := f.userSM.SnapshotState()
	_, applied, err := f.userSM.ApplyLocalBestEffort(before.LatestNonce+1, []*types.DevshardTx{timeoutTxFor(1, votes)})
	require.NoError(t, err)
	require.Len(t, applied, 1, "timeout tx applies")
	require.Equal(t, types.StatusTimedOut, f.userSM.SnapshotState().Inferences[1].Status)
}

func TestCollectTimeoutVotes_AcceptsPrimarySlotOfMultiSlotHost(t *testing.T) {
	f := newTimeoutVoteFixture(t)

	// signers[2] owns slots 2..5. Contact it at index 3 while it reports slot 2.
	require.Equal(t, uint32(3), f.group[3].SlotID)
	require.Equal(t, f.signers[2].Address(), f.group[3].ValidatorAddress)

	votes := f.collect(t, map[int]TimeoutVerifier{
		3: &slotClaimingVerifier{signer: f.signers[2], claimedSlot: 2, escrowID: "escrow-1"},
	})

	require.Len(t, votes, 1, "a host signing for another of its own slots is valid")
	require.Equal(t, uint32(2), votes[0].VoterSlot)
	require.True(t, f.session.HasSufficientTimeoutVotes(votes), "weight 4 exceeds threshold 3")
}

func TestCollectTimeoutVotes_RejectsForeignEscrowSignature(t *testing.T) {
	f := newTimeoutVoteFixture(t)

	votes := f.collect(t, map[int]TimeoutVerifier{
		2: &slotClaimingVerifier{signer: f.signers[2], claimedSlot: 2, escrowID: "escrow-other"},
	})

	require.Empty(t, votes, "vote signed over a different escrow id must not be collected")
}

func TestHasSufficientTimeoutVotes_DedupesByAddress(t *testing.T) {
	signers := make([]*signing.Secp256k1Signer, 4)
	for i := range signers {
		signers[i] = testutil.MustGenerateKey(t)
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeMultiSlotGroup(signers, []int{2, 2, 2, 1})
	config := types.SessionConfig{
		RefusalTimeout: 60, ExecutionTimeout: 1200, TokenPrice: 1,
		VoteThreshold: uint32(len(group)) / 2,
	}
	verifier := signing.NewSecp256k1Verifier()
	sm := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, userKey.Address(), verifier)
	clients := make([]HostClient, len(group))
	session, err := NewSession(sm, userKey, "escrow-1", group, clients, verifier)
	require.NoError(t, err)

	// Slots 0 and 1 are both owned by signers[0].
	require.Equal(t, sm.SlotAddress(0), sm.SlotAddress(1))
	votes := []*types.TimeoutVote{
		{VoterSlot: 0, Accept: true},
		{VoterSlot: 1, Accept: true},
	}
	require.False(t, session.HasSufficientTimeoutVotes(votes),
		"one address must contribute weight 2 once, not 2 twice")
}

func TestApplyTimeout_SingleSpoofedVoteRejectsWholeTx(t *testing.T) {
	signers := make([]*signing.Secp256k1Signer, 4)
	for i := range signers {
		signers[i] = testutil.MustGenerateKey(t)
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeMultiSlotGroup(signers, []int{1, 1, 4, 1})
	config := types.SessionConfig{
		RefusalTimeout: 60, ExecutionTimeout: 1200, TokenPrice: 1, FeePerNonce: 5,
		VoteThreshold: uint32(len(group)) / 2, // 3
	}
	verifier := signing.NewSecp256k1Verifier()

	signVote := func(s *signing.Secp256k1Signer, slot uint32) *types.TimeoutVote {
		data, err := proto.Marshal(&types.TimeoutVoteContent{
			EscrowId: "escrow-1", InferenceId: 1,
			Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED, Accept: true,
		})
		require.NoError(t, err)
		sig, err := s.Sign(data)
		require.NoError(t, err)
		return &types.TimeoutVote{VoterSlot: slot, Accept: true, Signature: sig}
	}

	seed := func(sm *state.StateMachine) {
		_, applied, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{{
			Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
				InferenceId: 1, Model: "llama", InputLength: 100, MaxTokens: 50, StartedAt: 1000,
			}},
		}})
		require.NoError(t, err)
		require.Len(t, applied, 1)
	}

	// signers[2] (slot 2, weight 4) + signers[3] (slot 6, weight 1) = 5 > 3.
	honest := []*types.TimeoutVote{signVote(signers[2], 2), signVote(signers[3], 6)}
	spoofed := signVote(signers[0], 2) // own key, someone else's slot

	clean := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, userKey.Address(), verifier)
	seed(clean)
	_, applied, err := clean.ApplyLocalBestEffort(2, []*types.DevshardTx{timeoutTxFor(1, honest)})
	require.NoError(t, err)
	require.Len(t, applied, 1, "honest quorum alone applies")
	require.Equal(t, types.StatusTimedOut, clean.SnapshotState().Inferences[1].Status)

	poisoned := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, userKey.Address(), verifier)
	seed(poisoned)
	before := poisoned.SnapshotState()
	all := append(append([]*types.TimeoutVote{}, honest...), spoofed)
	_, applied, err = poisoned.ApplyLocalBestEffort(2, []*types.DevshardTx{timeoutTxFor(1, all)})
	require.NoError(t, err, "the surrounding best-effort diff still commits")
	require.Empty(t, applied, "one spoofed vote rejects the entire timeout tx")

	after := poisoned.SnapshotState()
	require.Equal(t, types.StatusPending, after.Inferences[1].Status, "inference not timed out")
	require.Equal(t, before.LatestNonce+1, after.LatestNonce, "nonce still consumed")
	require.Equal(t, before.Balance-config.FeePerNonce, after.Balance, "fee still charged")
}

func TestSendPendingDiff_ReportsDroppedTimeoutTx(t *testing.T) {
	t.Run("dropped", func(t *testing.T) {
		f := newTimeoutVoteFixture(t)
		f.dropHostReceipts()
		data, err := proto.Marshal(&types.TimeoutVoteContent{
			EscrowId: "escrow-1", InferenceId: 1,
			Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED, Accept: true,
		})
		require.NoError(t, err)
		sig, err := f.signers[0].Sign(data)
		require.NoError(t, err)

		f.session.AddPendingTimeoutTx(1, types.TimeoutReason_TIMEOUT_REASON_REFUSED,
			[]*types.TimeoutVote{{VoterSlot: 2, Accept: true, Signature: sig}})

		before := f.userSM.SnapshotState()
		diff, err := f.session.sendPendingDiff(context.Background())
		require.NoError(t, err)
		require.False(t, diffAppliedTimeout(diff, 1), "rejected timeout tx is not in the diff")

		after := f.userSM.SnapshotState()
		require.Equal(t, types.StatusPending, after.Inferences[1].Status)
		require.Equal(t, before.LatestNonce+1, after.LatestNonce, "nonce consumed anyway")
	})

	t.Run("applied", func(t *testing.T) {
		f := newTimeoutVoteFixture(t)
		f.dropHostReceipts()
		votes := f.collect(t, map[int]TimeoutVerifier{
			2: &mockTimeoutVerifier{accept: true, signer: f.signers[2], group: f.group, slotIdx: 2},
			6: &mockTimeoutVerifier{accept: true, signer: f.signers[3], group: f.group, slotIdx: 6},
		})
		require.True(t, f.session.HasSufficientTimeoutVotes(votes))

		f.session.AddPendingTimeoutTx(1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, votes)
		diff, err := f.session.sendPendingDiff(context.Background())
		require.NoError(t, err)
		require.True(t, diffAppliedTimeout(diff, 1))
		require.Equal(t, types.StatusTimedOut, f.userSM.SnapshotState().Inferences[1].Status)
	})
}

func TestCollectTimeoutVotes_WarmKeySignedVotesAccepted(t *testing.T) {
	session, coldKeys, warmKeys := setupWarmKeySession(t, 3)

	ctx := context.Background()
	_, err := session.SendInference(ctx, defaultParams)
	require.NoError(t, err)

	group := session.group
	executorIdx := int(1 % uint64(len(group)))
	verifiers := make(map[int]TimeoutVerifier, len(group))
	for i := range group {
		if i == executorIdx {
			continue
		}
		verifiers[i] = &mockTimeoutVerifier{accept: true, signer: warmKeys[i], group: group, slotIdx: i}
	}

	votes, err := session.CollectTimeoutVotes(ctx, 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED,
		&host.InferencePayload{
			Prompt: testutil.TestPrompt, Model: "llama",
			InputLength: 100, MaxTokens: 50, StartedAt: 1000,
		}, verifiers, nil)
	require.NoError(t, err)

	require.NotEmpty(t, votes, "votes signed by an authorized warm key must be collected")
	for _, v := range votes {
		require.Equal(t, coldKeys[v.VoterSlot].Address(), session.StateMachine().SlotAddress(v.VoterSlot))
	}
	require.True(t, session.HasSufficientTimeoutVotes(votes))
}

func TestCollectTimeoutVotes_UnauthorizedWarmKeyRejected(t *testing.T) {
	session, _, _ := setupWarmKeySession(t, 3)

	ctx := context.Background()
	_, err := session.SendInference(ctx, defaultParams)
	require.NoError(t, err)

	group := session.group
	stranger := testutil.MustGenerateKey(t)
	executorIdx := int(1 % uint64(len(group)))
	verifiers := make(map[int]TimeoutVerifier, len(group))
	for i := range group {
		if i == executorIdx {
			continue
		}
		verifiers[i] = &mockTimeoutVerifier{accept: true, signer: stranger, group: group, slotIdx: i}
	}

	votes, err := session.CollectTimeoutVotes(ctx, 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED,
		&host.InferencePayload{
			Prompt: testutil.TestPrompt, Model: "llama",
			InputLength: 100, MaxTokens: 50, StartedAt: 1000,
		}, verifiers, nil)
	require.NoError(t, err)
	require.Empty(t, votes, "a key the resolver does not authorize must not be counted")
}

func TestCollectTimeoutVotes_WarmKeyFromCachedBindingWhenResolverFails(t *testing.T) {
	coldKeys := makeKeys(t, 3)
	warmKeys := makeKeys(t, 3)
	accept := acceptResolver(warmKeys, coldKeys)
	resolverFails := false
	resolver := state.WarmKeyResolver(func(warmAddr, coldAddr string) (bool, error) {
		if resolverFails {
			return false, context.DeadlineExceeded
		}
		return accept(warmAddr, coldAddr)
	})

	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(coldKeys)
	config := testutil.DefaultConfig(3)
	verifier := signing.NewSecp256k1Verifier()
	smOpts := []state.SMOption{state.WithWarmKeyResolver(resolver)}

	clients := make([]HostClient, 3)
	for i := range coldKeys {
		sm, err := state.NewStateMachine("escrow-1", config, group, 100000, userKey.Address(), verifier,
			testutil.MustMemoryStore(t, "escrow-1", userKey.Address(), config, group, 100000), smOpts...)
		require.NoError(t, err)
		h, err := host.NewHost(sm, warmKeys[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(10))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}
	userSM, err := state.NewStateMachine("escrow-1", config, group, 100000, userKey.Address(), verifier,
		testutil.MustMemoryStore(t, "escrow-1", userKey.Address(), config, group, 100000), smOpts...)
	require.NoError(t, err)
	session, err := NewSession(userSM, userKey, "escrow-1", group, clients, verifier)
	require.NoError(t, err)

	ctx := context.Background()
	for i := 0; i < 4; i++ {
		_, err = session.SendInference(ctx, defaultParams)
		require.NoError(t, err)
	}
	require.Len(t, userSM.WarmKeys(), 3, "warm bindings cached by the applied receipts")

	resolverFails = true

	executorIdx := int(1 % uint64(len(group)))
	verifiers := make(map[int]TimeoutVerifier, len(group))
	for i := range group {
		if i == executorIdx {
			continue
		}
		verifiers[i] = &mockTimeoutVerifier{accept: true, signer: warmKeys[i], group: group, slotIdx: i}
	}

	votes, err := session.CollectTimeoutVotes(ctx, 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED,
		&host.InferencePayload{
			Prompt: testutil.TestPrompt, Model: "llama",
			InputLength: 100, MaxTokens: 50, StartedAt: 1000,
		}, verifiers, nil)
	require.NoError(t, err)
	require.True(t, session.HasSufficientTimeoutVotes(votes),
		"cached warm bindings must verify without reaching the resolver")
}
