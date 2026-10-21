package state

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func txChallenge(msg *types.MsgChallenge) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_Challenge{Challenge: msg}}
}

type challengeVoteSpec struct {
	slot  uint32
	fault types.ChallengeFault
}

func challengeVotes(t *testing.T, hosts []*signing.Secp256k1Signer, inferenceID uint64, responseHash []byte, specs []challengeVoteSpec) []*types.ChallengeVote {
	t.Helper()
	votes := make([]*types.ChallengeVote, 0, len(specs))
	for _, spec := range specs {
		check := types.ChallengeCheck_CHALLENGE_CHECK_UNSPECIFIED
		if spec.fault == types.ChallengeFault_CHALLENGE_FAULT_HASH_MISMATCH {
			check = types.ChallengeCheck_CHALLENGE_CHECK_HASH
		}
		votes = append(votes, testutil.SignChallengeVote(t, hosts[spec.slot], "escrow-1", inferenceID, spec.slot, spec.fault, check, 0, responseHash))
	}
	return votes
}

// finishedForChallenge applies start, confirm and finish for inference 1 on
// five single-slot hosts (executor slot 1, threshold 2) and returns the
// state after the finish.
func finishedForChallenge(t *testing.T) (*StateMachine, *signing.Secp256k1Signer, []*signing.Secp256k1Signer, types.EscrowState) {
	t.Helper()
	hosts := errorTimeoutHosts(t)
	sm, user := newTestSM(t, hosts, 10000)
	applyStartConfirmFinish(t, sm, user, hosts, 1)
	before := sm.SnapshotState()
	require.Equal(t, types.StatusFinished, before.Inferences[1].Status)
	require.Equal(t, uint32(2), before.Config.VoteThreshold)
	require.Equal(t, uint64(120), before.HostStats[1].Cost)
	return sm, user, hosts, before
}

func applyChallengeDiff(t *testing.T, sm *StateMachine, user *signing.Secp256k1Signer, msg *types.MsgChallenge) error {
	t.Helper()
	nonce := sm.SnapshotState().LatestNonce + 1
	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txChallenge(msg)}))
	return err
}

func TestApplyDiff_Challenge_FaultsAboveThresholdInvalidate(t *testing.T) {
	sm, user, hosts, before := finishedForChallenge(t)
	// Faults of different kinds count in one tally.
	votes := challengeVotes(t, hosts, 1, testutil.TestResponseHash, []challengeVoteSpec{
		{0, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
		{2, types.ChallengeFault_CHALLENGE_FAULT_HASH_MISMATCH},
		{3, types.ChallengeFault_CHALLENGE_FAULT_EMPTY_STREAM},
		{4, types.ChallengeFault_CHALLENGE_FAULT_NONE},
	})
	require.NoError(t, applyChallengeDiff(t, sm, user, &types.MsgChallenge{InferenceId: 1, Votes: votes}))

	after := sm.SnapshotState()
	rec := after.Inferences[1]
	require.Equal(t, types.StatusInvalidated, rec.Status)
	require.True(t, rec.UsageChallenged)
	require.Equal(t, before.Balance+rec.ActualCost, after.Balance)
	require.Equal(t, before.HostStats[1].Invalid+1, after.HostStats[1].Invalid)
	require.Equal(t, before.HostStats[1].Cost-rec.ActualCost, after.HostStats[1].Cost)
	require.Equal(t, before.HostStats[1].Missed, after.HostStats[1].Missed)
	require.Equal(t, uint64(10000), after.Balance, "the escrow gets back every unit reserved")
}

func TestApplyDiff_Challenge_InvalidateWithdrawsSampledPasses(t *testing.T) {
	sm, user, hosts, _ := finishedForChallenge(t)
	nonce := sm.SnapshotState().LatestNonce + 1
	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{
		validationTx(t, hosts, "escrow-1", 1, 0, true),
		validationTx(t, hosts, "escrow-1", 1, 2, true),
	}))
	require.NoError(t, err)
	before := sm.SnapshotState()
	require.Equal(t, types.StatusFinished, before.Inferences[1].Status)
	require.Equal(t, uint32(2), before.HostStats[1].Validated)

	votes := challengeVotes(t, hosts, 1, testutil.TestResponseHash, []challengeVoteSpec{
		{0, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
		{2, types.ChallengeFault_CHALLENGE_FAULT_HASH_MISMATCH},
		{3, types.ChallengeFault_CHALLENGE_FAULT_EMPTY_STREAM},
	})
	require.NoError(t, applyChallengeDiff(t, sm, user, &types.MsgChallenge{InferenceId: 1, Votes: votes}))

	after := sm.SnapshotState()
	require.Equal(t, types.StatusInvalidated, after.Inferences[1].Status)
	require.Zero(t, after.HostStats[1].Validated, "a pass credited before the challenge must not survive it")
	require.Equal(t, before.HostStats[1].Invalid+1, after.HostStats[1].Invalid)
	require.Equal(t, before.HostStats[1].Finished, after.HostStats[1].Finished, "invalidation does not undo the finish count")
}

func TestApplyDiff_Challenge_BelowThresholdStaysFinishedAndPaid(t *testing.T) {
	sm, user, hosts, before := finishedForChallenge(t)
	votes := challengeVotes(t, hosts, 1, testutil.TestResponseHash, []challengeVoteSpec{
		{0, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
		{2, types.ChallengeFault_CHALLENGE_FAULT_HASH_MISMATCH},
		{3, types.ChallengeFault_CHALLENGE_FAULT_NONE},
		{4, types.ChallengeFault_CHALLENGE_FAULT_NONE},
	})
	require.NoError(t, applyChallengeDiff(t, sm, user, &types.MsgChallenge{InferenceId: 1, Votes: votes}),
		"a challenge below the threshold is applied, not rejected")

	after := sm.SnapshotState()
	rec := after.Inferences[1]
	require.Equal(t, types.StatusFinished, rec.Status)
	require.True(t, rec.UsageChallenged)
	require.Equal(t, before.Balance, after.Balance)
	require.Equal(t, *before.HostStats[1], *after.HostStats[1], "NONE and a minority of faults change no host stat")
	require.Zero(t, rec.VotesValid, "NONE votes are not replay passes")
	require.Zero(t, rec.VotesInvalid)
	require.Equal(t, before.Inferences[1].ValidatedBy, rec.ValidatedBy)
	require.Equal(t, before.LatestNonce+1, after.LatestNonce, "the diff carrying the votes is applied")
}

func TestApplyDiff_Challenge_ExecutorVoteRejected(t *testing.T) {
	signers := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	// signer 1 owns slots 1 and 2 and executes inference 1 on slot 1.
	group := testutil.MakeMultiSlotGroup(signers, []int{1, 2, 2})
	config := testutil.DefaultConfig(len(group))
	verifier := signing.NewSecp256k1Verifier()
	store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000)
	sm, err := NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, store)
	require.NoError(t, err)
	applyStartConfirmFinishMultiSlot(t, sm, user, signers, group, 1)
	require.Equal(t, uint32(1), sm.SnapshotState().Inferences[1].ExecutorSlot)
	before := sm.SnapshotState()

	fault := types.ChallengeFault_CHALLENGE_FAULT_NONE
	for name, voterSlot := range map[string]uint32{"executor slot": 1, "another slot of the executor": 2} {
		t.Run(name, func(t *testing.T) {
			votes := []*types.ChallengeVote{
				testutil.SignChallengeVote(t, signers[0], "escrow-1", 1, 0, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE, 0, 0, testutil.TestResponseHash),
				testutil.SignChallengeVote(t, signers[1], "escrow-1", 1, voterSlot, fault, 0, 0, testutil.TestResponseHash),
			}
			err := applyChallengeDiff(t, sm, user, &types.MsgChallenge{InferenceId: 1, Votes: votes})
			require.ErrorIs(t, err, types.ErrExecutorVote)
			after := sm.SnapshotState()
			require.Equal(t, types.StatusFinished, after.Inferences[1].Status)
			require.False(t, after.Inferences[1].UsageChallenged)
			require.Equal(t, before.Balance, after.Balance)
			require.Equal(t, before.LatestNonce, after.LatestNonce)
		})
	}
}

func TestApplyDiff_Challenge_SecondChallengeIsNoop(t *testing.T) {
	sm, user, hosts, before := finishedForChallenge(t)
	first := challengeVotes(t, hosts, 1, testutil.TestResponseHash, []challengeVoteSpec{
		{0, types.ChallengeFault_CHALLENGE_FAULT_NONE},
		{2, types.ChallengeFault_CHALLENGE_FAULT_NONE},
		{3, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
	})
	require.NoError(t, applyChallengeDiff(t, sm, user, &types.MsgChallenge{InferenceId: 1, Votes: first}))
	marked := sm.SnapshotState()
	require.True(t, marked.Inferences[1].UsageChallenged)

	// A second vote set that would cross the threshold changes nothing.
	second := challengeVotes(t, hosts, 1, testutil.TestResponseHash, []challengeVoteSpec{
		{0, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
		{2, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
		{3, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
	})
	require.NoError(t, applyChallengeDiff(t, sm, user, &types.MsgChallenge{InferenceId: 1, Votes: second}))
	after := sm.SnapshotState()
	require.Equal(t, types.StatusFinished, after.Inferences[1].Status)
	require.Equal(t, before.Balance, after.Balance)
	require.Equal(t, *before.HostStats[1], *after.HostStats[1])
}

func TestApplyDiff_Challenge_RequiresFinished(t *testing.T) {
	hosts := errorTimeoutHosts(t)

	t.Run("started", func(t *testing.T) {
		sm, user := newTestSM(t, hosts, 10000)
		applyStartConfirm(t, sm, user, hosts, 1)
		votes := challengeVotes(t, hosts, 1, nil, []challengeVoteSpec{
			{0, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
			{2, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
			{3, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
		})
		err := applyChallengeDiff(t, sm, user, &types.MsgChallenge{InferenceId: 1, Votes: votes})
		require.ErrorIs(t, err, types.ErrInvalidTransition)
		require.Equal(t, types.StatusStarted, sm.SnapshotState().Inferences[1].Status)
	})

	t.Run("challenged by replay", func(t *testing.T) {
		sm, user := newTestSM(t, hosts, 10000)
		applyStartConfirmFinish(t, sm, user, hosts, 1)
		invalid := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: false, EscrowId: "escrow-1"}
		invalid.ProposerSig = testutil.SignProposerTx(t, hosts[0], invalid)
		nonce := sm.SnapshotState().LatestNonce + 1
		_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(invalid)}))
		require.NoError(t, err)
		require.Equal(t, types.StatusChallenged, sm.SnapshotState().Inferences[1].Status)

		votes := challengeVotes(t, hosts, 1, testutil.TestResponseHash, []challengeVoteSpec{
			{2, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
			{3, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
			{4, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
		})
		err = applyChallengeDiff(t, sm, user, &types.MsgChallenge{InferenceId: 1, Votes: votes})
		require.ErrorIs(t, err, types.ErrInvalidTransition)
		require.Equal(t, types.StatusChallenged, sm.SnapshotState().Inferences[1].Status)
	})
}

func TestApplyDiff_Challenge_SealedRejected(t *testing.T) {
	sm, user, hosts, _ := finishedForChallenge(t)
	require.NoError(t, sm.SealInference(1))
	before := sm.SnapshotState()

	votes := challengeVotes(t, hosts, 1, testutil.TestResponseHash, []challengeVoteSpec{
		{0, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
		{2, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
		{3, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
	})
	err := applyChallengeDiff(t, sm, user, &types.MsgChallenge{InferenceId: 1, Votes: votes})
	require.ErrorIs(t, err, types.ErrInvalidTransition)
	require.Contains(t, err.Error(), "sealed")
	after := sm.SnapshotState()
	require.Nil(t, after.Inferences[1])
	require.Equal(t, before.Balance, after.Balance)
	require.Equal(t, *before.HostStats[1], *after.HostStats[1])
}

func TestApplyDiff_Challenge_VoteRejections(t *testing.T) {
	cases := map[string]struct {
		votes func(t *testing.T, hosts []*signing.Secp256k1Signer) []*types.ChallengeVote
		want  error
	}{
		"vote for another response hash": {
			votes: func(t *testing.T, hosts []*signing.Secp256k1Signer) []*types.ChallengeVote {
				return challengeVotes(t, hosts, 1, []byte("another-response-hash-32-bytes!!"), []challengeVoteSpec{
					{0, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
				})
			},
			want: types.ErrInvalidVoteSig,
		},
		"vote for another inference": {
			votes: func(t *testing.T, hosts []*signing.Secp256k1Signer) []*types.ChallengeVote {
				return challengeVotes(t, hosts, 2, testutil.TestResponseHash, []challengeVoteSpec{
					{0, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
				})
			},
			want: types.ErrInvalidVoteSig,
		},
		"fault the voter did not sign": {
			votes: func(t *testing.T, hosts []*signing.Secp256k1Signer) []*types.ChallengeVote {
				votes := challengeVotes(t, hosts, 1, testutil.TestResponseHash, []challengeVoteSpec{
					{0, types.ChallengeFault_CHALLENGE_FAULT_NONE},
				})
				votes[0].Fault = types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE
				return votes
			},
			want: types.ErrInvalidVoteSig,
		},
		"vote signed by another host": {
			votes: func(t *testing.T, hosts []*signing.Secp256k1Signer) []*types.ChallengeVote {
				vote := testutil.SignChallengeVote(t, hosts[2], "escrow-1", 1, 0, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE, 0, 0, testutil.TestResponseHash)
				return []*types.ChallengeVote{vote}
			},
			want: types.ErrInvalidVoteSig,
		},
		"two votes from one address": {
			votes: func(t *testing.T, hosts []*signing.Secp256k1Signer) []*types.ChallengeVote {
				return challengeVotes(t, hosts, 1, testutil.TestResponseHash, []challengeVoteSpec{
					{0, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
					{0, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
				})
			},
			want: types.ErrDuplicateVote,
		},
		"slot outside the group": {
			votes: func(t *testing.T, hosts []*signing.Secp256k1Signer) []*types.ChallengeVote {
				vote := testutil.SignChallengeVote(t, hosts[0], "escrow-1", 1, 9, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE, 0, 0, testutil.TestResponseHash)
				return []*types.ChallengeVote{vote}
			},
			want: types.ErrSlotNotInGroup,
		},
		"unknown fault": {
			votes: func(t *testing.T, hosts []*signing.Secp256k1Signer) []*types.ChallengeVote {
				vote := testutil.SignChallengeVote(t, hosts[0], "escrow-1", 1, 0, types.ChallengeFault(99), 0, 0, testutil.TestResponseHash)
				return []*types.ChallengeVote{vote}
			},
			want: types.ErrUnknownChallengeFault,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sm, user, hosts, before := finishedForChallenge(t)
			err := applyChallengeDiff(t, sm, user, &types.MsgChallenge{InferenceId: 1, Votes: tc.votes(t, hosts)})
			require.ErrorIs(t, err, tc.want)
			after := sm.SnapshotState()
			require.False(t, after.Inferences[1].UsageChallenged)
			require.Equal(t, before.Balance, after.Balance)
			require.Equal(t, before.LatestNonce, after.LatestNonce)
		})
	}
}

// A binary that predates MsgChallenge sees DevshardTx field 15 as an unknown
// field, so the tx has no message set and the whole diff is refused. That is
// why MsgChallenge ships under a new protocol version.
func TestApplyDiff_Challenge_UnknownToOlderBinaryRejectsTheDiff(t *testing.T) {
	sm, user, hosts, before := finishedForChallenge(t)
	votes := challengeVotes(t, hosts, 1, testutil.TestResponseHash, []challengeVoteSpec{
		{0, types.ChallengeFault_CHALLENGE_FAULT_NONE},
	})
	wire, err := proto.Marshal(txChallenge(&types.MsgChallenge{InferenceId: 1, Votes: votes}))
	require.NoError(t, err)
	num, typ, n := protowire.ConsumeTag(wire)
	require.Equal(t, protowire.Number(15), num)
	value := wire[n:]
	// Renumber field 15 to one no DevshardTx field uses: the same bytes as
	// a binary without field 15 decodes them.
	older := protowire.AppendTag(nil, protowire.Number(1000), typ)
	older = append(older, value...)
	var seen types.DevshardTx
	require.NoError(t, proto.Unmarshal(older, &seen))
	require.Nil(t, seen.GetTx())

	nonce := sm.SnapshotState().LatestNonce + 1
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{&seen}))
	require.ErrorIs(t, err, types.ErrEmptyTx)
	after := sm.SnapshotState()
	require.Equal(t, before.LatestNonce, after.LatestNonce)
	require.NotEqual(t, "v6", after.StateRootAndProtocolVersion)
}

func TestFoldInferenceRecords_ChallengeMatchesLive(t *testing.T) {
	for name, faults := range map[string][]types.ChallengeFault{
		"invalidated": {
			types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE,
			types.ChallengeFault_CHALLENGE_FAULT_HASH_MISMATCH,
			types.ChallengeFault_CHALLENGE_FAULT_EMPTY_STREAM,
		},
		"below the threshold": {
			types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE,
			types.ChallengeFault_CHALLENGE_FAULT_NONE,
			types.ChallengeFault_CHALLENGE_FAULT_NONE,
		},
	} {
		t.Run(name, func(t *testing.T) {
			hosts := errorTimeoutHosts(t)
			sm, user := newTestSM(t, hosts, 10000)
			var journal []types.DiffRecord
			apply := func(txs []*types.DevshardTx) {
				t.Helper()
				nonce := sm.SnapshotState().LatestNonce + 1
				diff := testutil.SignDiff(t, user, "escrow-1", nonce, txs)
				_, err := sm.ApplyDiff(diff)
				require.NoError(t, err)
				journal = append(journal, types.DiffRecord{Diff: diff})
			}
			apply([]*types.DevshardTx{txStart(&types.MsgStartInference{
				InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
				InputLength: 100, MaxTokens: testutil.TestMaxTokens,
			})})
			execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, testutil.TestMaxTokens)
			apply([]*types.DevshardTx{txConfirm(&types.MsgConfirmStart{InferenceId: 1, ExecutorSig: execSig})})
			apply([]*types.DevshardTx{txFinish(signedFinish(t, hosts, 1, 1, 80, 40, testutil.TestResponseHash))})
			specs := []challengeVoteSpec{{0, faults[0]}, {2, faults[1]}, {3, faults[2]}}
			apply([]*types.DevshardTx{txChallenge(&types.MsgChallenge{InferenceId: 1, Votes: challengeVotes(t, hosts, 1, testutil.TestResponseHash, specs)})})
			// A second challenge is a no-op live, and must be one in the fold.
			apply([]*types.DevshardTx{txChallenge(&types.MsgChallenge{InferenceId: 1, Votes: challengeVotes(t, hosts, 1, testutil.TestResponseHash, []challengeVoteSpec{
				{0, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
				{2, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
				{3, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
			})})})

			st := sm.SnapshotState()
			live := st.Inferences[1]
			folded := sm.foldInferenceRecordsFromDiffs(st.Group, st.Config.TokenPrice, st.Config.VoteThreshold, journal)
			require.NotNil(t, folded[1])
			require.Equal(t, live.Status, folded[1].Status)
			require.Equal(t, live.UsageChallenged, folded[1].UsageChallenged)
		})
	}
}

func TestInferenceEntry_ChallengedRoundTrips(t *testing.T) {
	rec := &types.InferenceRecord{Status: types.StatusFinished, PromptHash: []byte("p"), UsageChallenged: true}
	data, err := marshalInferenceEntry(7, rec)
	require.NoError(t, err)
	id, got, err := unmarshalInferenceEntry(data)
	require.NoError(t, err)
	require.Equal(t, uint64(7), id)
	require.True(t, got.UsageChallenged)

	rec.UsageChallenged = false
	plain, err := marshalInferenceEntry(7, rec)
	require.NoError(t, err)
	require.NotEqual(t, data, plain, "the mark is part of the committed entry, so it moves the state root")
}
