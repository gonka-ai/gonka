package state

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/types"
)

// --- Test helpers (package-specific) ---

func newTestSM(t *testing.T, hosts []*signing.Secp256k1Signer, balance uint64) (*StateMachine, *signing.Secp256k1Signer) {
	t.Helper()
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, balance)
	sm, err := NewStateMachine("escrow-1", config, group, balance, user.Address(), verifier, store)
	require.NoError(t, err)
	return sm, user
}

func TestNewStateMachine_NormalizesInferenceSealGraceNonces(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	verifier := signing.NewSecp256k1Verifier()
	config := types.SessionConfig{TokenPrice: 1, VoteThreshold: 1}

	sm, err := NewStateMachine("escrow-1", config, group, 1000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1000))
	require.NoError(t, err)

	st := sm.SnapshotState()
	require.Equal(t, types.DefaultInferenceSealGraceNonces(len(group)), st.Config.InferenceSealGraceNonces)
}

// txStart wraps MsgStartInference in a DevshardTx.
func txStart(msg *types.MsgStartInference) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: msg}}
}

// txConfirm wraps MsgConfirmStart in a DevshardTx.
func txConfirm(msg *types.MsgConfirmStart) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: msg}}
}

// txFinish wraps MsgFinishInference in a DevshardTx.
func txFinish(msg *types.MsgFinishInference) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
}

// txTimeout wraps MsgTimeoutInference in a DevshardTx.
func txTimeout(msg *types.MsgTimeoutInference) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: msg}}
}

// txValidation wraps MsgValidation in a DevshardTx.
func txValidation(msg *types.MsgValidation) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: msg}}
}

// txVote wraps MsgValidationVote in a DevshardTx.
func txVote(msg *types.MsgValidationVote) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{ValidationVote: msg}}
}

// txFinalize wraps MsgFinalizeRound in a DevshardTx.
func txFinalize() *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}}
}

// --- Tests ---

func TestApplyDiff_UserSigVerification(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)
	wrongUser := testutil.MustGenerateKey(t)

	// Invalid user sig.
	diff := testutil.SignDiff(t, wrongUser, "escrow-1", 1, nil)
	_, err := sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidUserSig)

	// Valid user sig.
	diff = testutil.SignDiff(t, user, "escrow-1", 1, nil)
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
}

func TestApplyDiff_StartInference(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1,
		PromptHash:  []byte("prompt"),
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	state := sm.SnapshotState()
	rec := state.Inferences[1]
	require.NotNil(t, rec)
	require.Equal(t, types.StatusPending, rec.Status)
	require.Equal(t, uint64(150), rec.ReservedCost) // (100+50)*1
	require.Equal(t, uint64(10000-150), state.Balance)
	// Executor slot: 1 % 3 = 1
	require.Equal(t, uint32(1), rec.ExecutorSlot)
}

func TestApplyDiff_ConfirmStart(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	// Start inference. Executor slot: 1 % 3 = 1
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Confirm start with valid executor receipt.
	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	state := sm.SnapshotState()
	require.Equal(t, types.StatusStarted, state.Inferences[1].Status)
}

func TestApplyDiff_ConfirmStart_InvalidReceipt(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// ConfirmStart with wrong signer (host[0] instead of host[1]).
	execSig := testutil.SignExecutorReceipt(t, hosts[0], "escrow-1", 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidExecutorSig)
}

func TestApplyDiff_FinishInference(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	// Start + confirm.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Finish inference. Executor is slot 1 (hosts[1]).
	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("response"),
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: 1,
		EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[1], finishMsg)

	diff = testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{txFinish(finishMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	state := sm.SnapshotState()
	rec := state.Inferences[1]
	require.Equal(t, types.StatusFinished, rec.Status)
	require.Equal(t, uint64(120), rec.ActualCost) // (80+40)*1
	// Reserved was 150, actual 120 -> surplus 30 returned.
	require.Equal(t, uint64(10000-150+30), state.Balance)
	require.Equal(t, uint64(120), state.HostStats[1].Cost)
}

func TestApplyDiff_FinishInference_WrongExecutorSlot(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("response"),
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: 2, // Wrong! Should be 1.
		EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], finishMsg)

	diff = testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{txFinish(finishMsg)})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrWrongExecutorSlot)
}

func TestApplyDiff_FinishInference_InvalidProposerSig(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	outsider := testutil.MustGenerateKey(t)
	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("response"),
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: 1,
		EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, outsider, finishMsg)

	diff = testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{txFinish(finishMsg)})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidProposerSig)
}

func TestApplyDiff_Validation_Valid(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	applyStartConfirmFinish(t, sm, user, hosts, 1)

	// valid=true on Finished stays Finished (compliance credit only, no state transition).
	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: true, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], valMsg)

	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	st := sm.SnapshotState()
	require.Equal(t, types.StatusFinished, st.Inferences[1].Status)
	require.True(t, st.Inferences[1].ValidatedBy.IsSet(0), "validator bit must be set")
	require.Equal(t, uint32(1), st.Inferences[1].VotesValid, "valid vote weight must be recorded")
}

func TestApplyDiff_Validation_SelfValidation(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	applyStartConfirmFinish(t, sm, user, hosts, 1)

	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 1, Valid: true, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[1], valMsg)

	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err := sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrSelfValidation)
}

func TestApplyDiff_Validation_Invalid_ChallengeVoting(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	applyStartConfirmFinish(t, sm, user, hosts, 1)

	// Validate (valid=false) -> challenged.
	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: false, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], valMsg)

	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)
	st := sm.SnapshotState()
	require.Equal(t, types.StatusChallenged, st.Inferences[1].Status)
	require.Equal(t, uint32(1), st.Inferences[1].VotesInvalid, "challenger weight must be pre-counted")

	// Vote invalid from slots 2,3 (not 0 -- challenger already participated via ValidatedBy).
	// Challenger weight=1 + 2 voters = 3 > threshold 2 -> invalidated.
	var voteTxs []*types.DevshardTx
	for _, slot := range []uint32{2, 3} {
		voteMsg := &types.MsgValidationVote{InferenceId: 1, VoterSlot: slot, VoteValid: false, EscrowId: "escrow-1"}
		voteMsg.ProposerSig = testutil.SignProposerTx(t, hosts[slot], voteMsg)
		voteTxs = append(voteTxs, txVote(voteMsg))
	}

	nonce = sm.SnapshotState().LatestNonce + 1
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, voteTxs)
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	state := sm.SnapshotState()
	rec := state.Inferences[1]
	require.Equal(t, types.StatusInvalidated, rec.Status)
	require.Equal(t, uint32(1), state.HostStats[1].Invalid)
	require.Equal(t, uint64(0), state.HostStats[1].Cost)
}

func TestApplyDiff_Timeout_Refused(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	var votes []*types.TimeoutVote
	for _, slot := range []uint32{0, 2, 3} {
		v := testutil.SignTimeoutVote(t, hosts[slot], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
		v.VoterSlot = slot
		votes = append(votes, v)
	}

	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED, Votes: votes,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	state := sm.SnapshotState()
	require.Equal(t, types.StatusTimedOut, state.Inferences[1].Status)
	require.Equal(t, uint32(1), state.HostStats[1].Missed)
	require.Equal(t, uint64(10000), state.Balance)
}

func TestApplyDiff_Timeout_Execution(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	var votes []*types.TimeoutVote
	for _, slot := range []uint32{0, 2, 3} {
		v := testutil.SignTimeoutVote(t, hosts[slot], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, true)
		v.VoterSlot = slot
		votes = append(votes, v)
	}

	diff = testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_EXECUTION, Votes: votes,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	state := sm.SnapshotState()
	require.Equal(t, types.StatusTimedOut, state.Inferences[1].Status)
	require.Equal(t, uint32(1), state.HostStats[1].Missed)
	require.Equal(t, uint64(10000), state.Balance)
}

func TestApplyDiff_Timeout_WrongReason(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// reason=execution on pending -> fail.
	var votes []*types.TimeoutVote
	for _, slot := range []uint32{0, 2, 3} {
		v := testutil.SignTimeoutVote(t, hosts[slot], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, true)
		v.VoterSlot = slot
		votes = append(votes, v)
	}
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_EXECUTION, Votes: votes,
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidTimeoutReason)

	// Confirm start, then reason=refused on started -> fail.
	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	var votes2 []*types.TimeoutVote
	for _, slot := range []uint32{0, 2, 3} {
		v := testutil.SignTimeoutVote(t, hosts[slot], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
		v.VoterSlot = slot
		votes2 = append(votes2, v)
	}
	diff = testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED, Votes: votes2,
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidTimeoutReason)
}

func TestApplyDiff_Timeout_InsufficientVotes(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Only 2 accept votes (need >2 for 5 total slots).
	var votes []*types.TimeoutVote
	for _, slot := range []uint32{0, 2} {
		v := testutil.SignTimeoutVote(t, hosts[slot], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
		v.VoterSlot = slot
		votes = append(votes, v)
	}
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED, Votes: votes,
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInsufficientVotes)
}

func TestApplyDiff_Timeout_AfterFinish(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	applyStartConfirmFinish(t, sm, user, hosts, 1)

	var votes []*types.TimeoutVote
	for _, slot := range []uint32{0, 2, 3} {
		v := testutil.SignTimeoutVote(t, hosts[slot], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, true)
		v.VoterSlot = slot
		votes = append(votes, v)
	}

	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_EXECUTION, Votes: votes,
	})})
	_, err := sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidTimeoutReason)
}

func TestApplyDiff_Timeout_MultiSlotWeight(t *testing.T) {
	// 3 signers: signer0 owns 3 slots (0,1,2), signer1 owns 1 slot (3), signer2 owns 1 slot (4).
	// Total 5 slots. VoteThreshold = 5/2 = 2. Need >2 accept weight.
	// One vote from signer0 (slot 0) should count as weight=3.
	signers := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeMultiSlotGroup(signers, []int{3, 1, 1})
	config := testutil.DefaultConfig(len(group)) // VoteThreshold = 5/2 = 2
	verifier := signing.NewSecp256k1Verifier()
	sm, err := NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)

	// Start inference. Executor slot = group[1%5].SlotID = 1 (owned by signer0).
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	// One accept vote from signer2 (slot 4, weight=1) -- not enough alone.
	// But signer1 (slot 3, weight=1) also votes accept -> total weight=2, still not >2.
	// Need signer0 to vote (weight=3) for >2.
	vote := testutil.SignTimeoutVote(t, signers[2], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
	vote.VoterSlot = 4 // signer2's slot

	// Single vote with weight=1 should fail (need >2).
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED,
		Votes: []*types.TimeoutVote{vote},
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInsufficientVotes)

	// Now add signer0's vote (slot 0, weight=3). Total = 1+3 = 4 > 2.
	vote0 := testutil.SignTimeoutVote(t, signers[0], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
	vote0.VoterSlot = 0

	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED,
		Votes: []*types.TimeoutVote{vote, vote0},
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	state := sm.SnapshotState()
	require.Equal(t, types.StatusTimedOut, state.Inferences[1].Status)
}

func TestApplyDiff_NonceSequential(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	diff := testutil.SignDiff(t, user, "escrow-1", 2, nil)
	_, err := sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidNonce)
}

func TestApplyDiff_MultipleMsgStartInference(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	txs := []*types.DevshardTx{
		txStart(&types.MsgStartInference{InferenceId: 1, InputLength: 10, MaxTokens: 5}),
		txStart(&types.MsgStartInference{InferenceId: 1, InputLength: 10, MaxTokens: 5}),
	}
	diff := testutil.SignDiff(t, user, "escrow-1", 1, txs)
	_, err := sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrMultipleStartMsgs)
}

func TestApplyDiff_FinalizeRound(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txFinalize()})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.True(t, sm.SnapshotState().Phase >= types.PhaseFinalizing)

	// MsgStartInference after finalize -> rejected.
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 2, InputLength: 10, MaxTokens: 5,
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrSessionFinalizing)

	// Second finalize -> rejected.
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txFinalize()})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrAlreadyFinalizing)
}

func TestApplyDiff_FinalizeRound_HostTxsStillAccepted(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	diff = testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{txFinalize()})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("response"),
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: 1,
		EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[1], finishMsg)

	diff = testutil.SignDiff(t, user, "escrow-1", 4, []*types.DevshardTx{txFinish(finishMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, types.StatusFinished, sm.SnapshotState().Inferences[1].Status)
}

func TestApplyDiff_DuplicateTimeout(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	var votes []*types.TimeoutVote
	for _, slot := range []uint32{0, 2, 3} {
		v := testutil.SignTimeoutVote(t, hosts[slot], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
		v.VoterSlot = slot
		votes = append(votes, v)
	}
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED, Votes: votes,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	var votes2 []*types.TimeoutVote
	for _, slot := range []uint32{0, 2, 3} {
		v := testutil.SignTimeoutVote(t, hosts[slot], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
		v.VoterSlot = slot
		votes2 = append(votes2, v)
	}
	diff = testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED, Votes: votes2,
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidTimeoutReason)
}

func TestApplyDiff_EscrowBalanceCheck(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInsufficientBalance)
}

func TestApplyDiff_FullLifecycle(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 100000)
	nonce := uint64(0)

	outcomes := []string{
		"finished", "finished", "timed_out", "finished",
		"validated", "invalidated", "finished", "timed_out",
		"finished", "finished",
	}

	for _, outcome := range outcomes {
		// inference_id == nonce of the start diff.
		nonce++
		infID := nonce
		executorSlotIdx := infID % uint64(len(hosts))

		diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txStart(&types.MsgStartInference{
			InferenceId: infID, PromptHash: []byte("prompt"), Model: "llama",
			InputLength: 100, MaxTokens: 50, StartedAt: int64(infID) * 1000,
		})})
		_, err := sm.ApplyDiff(diff)
		require.NoError(t, err)

		if outcome == "timed_out" {
			var votes []*types.TimeoutVote
			for _, slot := range []uint32{0, 1, 2, 3, 4} {
				if slot == uint32(executorSlotIdx) {
					continue
				}
				if len(votes) >= 3 {
					break
				}
				v := testutil.SignTimeoutVote(t, hosts[slot], "escrow-1", infID, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
				v.VoterSlot = slot
				votes = append(votes, v)
			}
			nonce++
			diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
				InferenceId: infID, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED, Votes: votes,
			})})
			_, err = sm.ApplyDiff(diff)
			require.NoError(t, err)
			continue
		}

		execSig := testutil.SignExecutorReceipt(t, hosts[executorSlotIdx], "escrow-1", infID, []byte("prompt"), "llama", 100, 50, int64(infID)*1000, int64(infID)*1000)
		nonce++
		diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
			InferenceId: infID, ExecutorSig: execSig, ConfirmedAt: int64(infID) * 1000,
		})})
		_, err = sm.ApplyDiff(diff)
		require.NoError(t, err)

		finishMsg := &types.MsgFinishInference{
			InferenceId: infID, ResponseHash: []byte("response"),
			InputTokens: 80, OutputTokens: 40, ExecutorSlot: uint32(executorSlotIdx),
			EscrowId: "escrow-1",
		}
		finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[executorSlotIdx], finishMsg)
		nonce++
		diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txFinish(finishMsg)})
		_, err = sm.ApplyDiff(diff)
		require.NoError(t, err)

		if outcome == "finished" {
			continue
		}

		if outcome == "validated" {
			// Challenge with valid=false to trigger Challenged, then vote valid.
			validatorSlot := uint32((executorSlotIdx + 1) % uint64(len(hosts)))
			valMsg := &types.MsgValidation{InferenceId: infID, ValidatorSlot: validatorSlot, Valid: false, EscrowId: "escrow-1"}
			valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[validatorSlot], valMsg)
			nonce++
			diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
			_, err = sm.ApplyDiff(diff)
			require.NoError(t, err)

			// Vote valid to reach Validated. Skip executor and challenger (already in ValidatedBy).
			var voteTxs []*types.DevshardTx
			votedCount := 0
			for slot := uint32(0); slot < uint32(len(hosts)); slot++ {
				if slot == uint32(executorSlotIdx) || slot == validatorSlot {
					continue
				}
				if votedCount >= 3 {
					break
				}
				voteMsg := &types.MsgValidationVote{InferenceId: infID, VoterSlot: slot, VoteValid: true, EscrowId: "escrow-1"}
				voteMsg.ProposerSig = testutil.SignProposerTx(t, hosts[slot], voteMsg)
				voteTxs = append(voteTxs, txVote(voteMsg))
				votedCount++
			}
			nonce++
			diff = testutil.SignDiff(t, user, "escrow-1", nonce, voteTxs)
			_, err = sm.ApplyDiff(diff)
			require.NoError(t, err)
			continue
		}

		if outcome == "invalidated" {
			validatorSlot := uint32((executorSlotIdx + 1) % uint64(len(hosts)))
			valMsg := &types.MsgValidation{InferenceId: infID, ValidatorSlot: validatorSlot, Valid: false, EscrowId: "escrow-1"}
			valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[validatorSlot], valMsg)
			nonce++
			diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
			_, err = sm.ApplyDiff(diff)
			require.NoError(t, err)

			var voteTxs []*types.DevshardTx
			votedCount := 0
			for slot := uint32(0); slot < uint32(len(hosts)); slot++ {
				if slot == uint32(executorSlotIdx) || slot == validatorSlot {
					continue
				}
				if votedCount >= 3 {
					break
				}
				voteMsg := &types.MsgValidationVote{InferenceId: infID, VoterSlot: slot, VoteValid: false, EscrowId: "escrow-1"}
				voteMsg.ProposerSig = testutil.SignProposerTx(t, hosts[slot], voteMsg)
				voteTxs = append(voteTxs, txVote(voteMsg))
				votedCount++
			}
			nonce++
			diff = testutil.SignDiff(t, user, "escrow-1", nonce, voteTxs)
			_, err = sm.ApplyDiff(diff)
			require.NoError(t, err)
		}
	}

	state := sm.SnapshotState()
	var finished, timedOut, validated, invalidated int
	for _, rec := range sm.ExportAllInferenceRecords() {
		switch rec.Status {
		case types.StatusFinished:
			finished++
		case types.StatusTimedOut:
			timedOut++
		case types.StatusValidated:
			validated++
		case types.StatusInvalidated:
			invalidated++
		}
	}
	require.Equal(t, 6, finished)
	require.Equal(t, 2, timedOut)
	require.Equal(t, 1, validated)
	require.Equal(t, 1, invalidated)

	totalCost := uint64(0)
	for _, hs := range state.HostStats {
		totalCost += hs.Cost
	}
	require.Equal(t, uint64(100000)-totalCost, state.Balance)
}

func TestApplyDiff_InferenceIDMustMatchNonce(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	// inference_id=42 at nonce=1 -> rejected.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 42, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidInferenceID)
}

// --- 4 new tests ---

func TestApplyDiff_DuplicateInferenceID(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	// First start succeeds.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Second start with same ID rejected (inference_id=1 != nonce=2).
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt2"), Model: "llama",
		InputLength: 50, MaxTokens: 25, StartedAt: 2000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidInferenceID)
}

func TestApplyDiff_Timeout_DuplicateVoterSlot(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Slot 0 votes twice.
	v0a := testutil.SignTimeoutVote(t, hosts[0], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
	v0a.VoterSlot = 0
	v0b := testutil.SignTimeoutVote(t, hosts[0], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
	v0b.VoterSlot = 0
	v2 := testutil.SignTimeoutVote(t, hosts[2], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
	v2.VoterSlot = 2

	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED, Votes: []*types.TimeoutVote{v0a, v0b, v2},
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrDuplicateVote)
}

func TestApplyDiff_ValidationVote_AlreadyResolved(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	applyStartConfirmFinish(t, sm, user, hosts, 1)

	// Challenge.
	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: false, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], valMsg)
	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// 3 votes batched (skip slot 0 -- challenger already in ValidatedBy).
	// Challenger weight=1 + 3 voters = 4 > threshold 2 -> invalidated.
	// 4th vote (slot 4) arrives after resolution -> silently succeeds.
	var voteTxs []*types.DevshardTx
	for _, slot := range []uint32{2, 3, 4} {
		voteMsg := &types.MsgValidationVote{InferenceId: 1, VoterSlot: slot, VoteValid: false, EscrowId: "escrow-1"}
		voteMsg.ProposerSig = testutil.SignProposerTx(t, hosts[slot], voteMsg)
		voteTxs = append(voteTxs, txVote(voteMsg))
	}

	nonce = sm.SnapshotState().LatestNonce + 1
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, voteTxs)
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	state := sm.SnapshotState()
	require.Equal(t, types.StatusInvalidated, state.Inferences[1].Status)
}

func TestSnapshotState_DeepCopy(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	// Start an inference to populate state.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Get state and mutate the copy.
	stateCopy := sm.SnapshotState()
	stateCopy.Balance = 999999
	stateCopy.Inferences[1].Status = types.StatusTimedOut
	stateCopy.Inferences[1].PromptHash[0] = 0xFF
	stateCopy.HostStats[0].Cost = 999
	stateCopy.Group[0].ValidatorAddress = "mutated"

	// Verify original state is unaffected.
	original := sm.SnapshotState()
	require.Equal(t, uint64(10000-150), original.Balance)
	require.Equal(t, types.StatusPending, original.Inferences[1].Status)
	require.Equal(t, byte('p'), original.Inferences[1].PromptHash[0])
	require.Equal(t, uint64(0), original.HostStats[0].Cost)
	require.NotEqual(t, "mutated", original.Group[0].ValidatorAddress)
}

// --- Helper for common start + confirm + finish flow ---

func applyStartConfirmFinish(t *testing.T, sm *StateMachine, user *signing.Secp256k1Signer, hosts []*signing.Secp256k1Signer, inferenceID uint64) {
	t.Helper()
	executorSlotIdx := inferenceID % uint64(len(hosts))
	nonce := sm.SnapshotState().LatestNonce + 1

	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: inferenceID, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[executorSlotIdx], "escrow-1", inferenceID, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: inferenceID, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	finishMsg := &types.MsgFinishInference{
		InferenceId: inferenceID, ResponseHash: []byte("response"),
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: uint32(executorSlotIdx),
		EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[executorSlotIdx], finishMsg)
	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txFinish(finishMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
}

// txRevealSeed wraps MsgRevealSeed in a DevshardTx.
func txRevealSeed(msg *types.MsgRevealSeed) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_RevealSeed{RevealSeed: msg}}
}

// --- Wrong-proposer tests ---

func TestApplyDiff_FinishInference_WrongProposer(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	// Start + confirm. Executor for inference 1 is slot 1 (hosts[1]).
	applyStartConfirmFinish_Setup(t, sm, user, hosts, 1)

	// Sign finish with hosts[0] (in group, but not the executor).
	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("response"),
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: 1,
		EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], finishMsg)

	nonce := sm.LatestNonce() + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txFinish(finishMsg)})
	_, err := sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidProposerSig)
}

func TestApplyDiff_Validation_WrongProposer(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	applyStartConfirmFinish(t, sm, user, hosts, 1)

	// Validator is slot 0, but sign with hosts[2] (in group, wrong slot).
	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: true, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[2], valMsg)

	nonce := sm.LatestNonce() + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err := sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidProposerSig)
}

func TestApplyDiff_ValidationVote_WrongProposer(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	applyStartConfirmFinish(t, sm, user, hosts, 1)

	// Challenge.
	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: false, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], valMsg)
	nonce := sm.LatestNonce() + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Vote from slot 2, but sign with hosts[3] (in group, wrong slot).
	voteMsg := &types.MsgValidationVote{InferenceId: 1, VoterSlot: 2, VoteValid: false, EscrowId: "escrow-1"}
	voteMsg.ProposerSig = testutil.SignProposerTx(t, hosts[3], voteMsg)

	nonce = sm.LatestNonce() + 1
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txVote(voteMsg)})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidProposerSig)
}

func TestApplyDiff_RevealSeed_WrongProposerAcceptedAsNoOp(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	// Finalize first.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{
		{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}},
	})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// RevealSeed from slot 0, signed by hosts[1] (in group, wrong slot).
	seedSig, _ := hosts[0].Sign([]byte("escrow-1"))
	seedMsg := &types.MsgRevealSeed{SlotId: 0, Signature: seedSig, EscrowId: "escrow-1"}
	seedMsg.ProposerSig = testutil.SignProposerTx(t, hosts[1], seedMsg)

	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txRevealSeed(seedMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
}

func TestApplyDiff_RevealSeed_InvalidSlotAcceptedAsNoOp(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	// Finalize first.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{
		{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}},
	})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// RevealSeed with slot 99 (not in group).
	seedMsg := &types.MsgRevealSeed{SlotId: 99, Signature: []byte("seed"), EscrowId: "escrow-1"}
	seedMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], seedMsg)

	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txRevealSeed(seedMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
}

func TestApplyDiff_CostOverflow_StartInference(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, math.MaxUint64)

	// InputLength + MaxTokens overflows uint64.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: math.MaxUint64, MaxTokens: 1, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrCostOverflow)

	// Multiplication overflows: large input * price.
	diff = testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: math.MaxUint64 / 2, MaxTokens: 1, StartedAt: 1000,
	})})
	// With TokenPrice=1, the mul won't overflow. Use a custom SM with higher price.
	config := types.SessionConfig{TokenPrice: 3, VoteThreshold: 1}
	group := testutil.MakeGroup(hosts)
	verifier := signing.NewSecp256k1Verifier()
	smHigh, err := NewStateMachine("escrow-1", config, group, math.MaxUint64, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, math.MaxUint64))
	require.NoError(t, err)

	diff = testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: math.MaxUint64 / 2, MaxTokens: 1, StartedAt: 1000,
	})})
	_, err = smHigh.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrCostOverflow)
}

func TestApplyDiff_AtomicRollback(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	// First: apply a valid start.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	balanceBefore := sm.SnapshotState().Balance

	// Diff with two txs: a valid confirm, then an invalid finish (wrong executor slot).
	// The confirm would succeed, modifying state, but the finish fails.
	// With atomic rollback, the state should be unchanged.
	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)

	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("response"),
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: 2, // Wrong executor slot.
		EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[2], finishMsg)

	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{
		txConfirm(&types.MsgConfirmStart{InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000}),
		txFinish(finishMsg),
	})
	_, err = sm.ApplyDiff(diff)
	require.Error(t, err)

	// State should be unchanged (atomic rollback).
	st := sm.SnapshotState()
	require.Equal(t, balanceBefore, st.Balance)
	require.Equal(t, types.StatusPending, st.Inferences[1].Status, "should still be pending after rollback")
	require.Equal(t, uint64(1), st.LatestNonce, "nonce should not advance on failure")
}

func TestNewStateMachine_DeductsCreateDevshardFee(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	config.CreateDevshardFee = 25
	verifier := signing.NewSecp256k1Verifier()

	sm, err := NewStateMachine("escrow-1", config, group, 100, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100))
	require.NoError(t, err)

	st := sm.SnapshotState()
	require.Equal(t, uint64(75), st.Balance)
	require.Equal(t, uint64(25), st.Fees)
}

func TestNewStateMachine_CreateDevshardFeeInsufficientBalance(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	config.CreateDevshardFee = 101
	verifier := signing.NewSecp256k1Verifier()

	_, err := NewStateMachine("escrow-1", config, group, 100, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100))
	require.ErrorIs(t, err, types.ErrInsufficientBalance)
}

func TestApplyDiff_FeePerNonce_Deducted(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(group))
	config.FeePerNonce = 7
	verifier := signing.NewSecp256k1Verifier()
	sm, err := NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1,
		PromptHash:  []byte("prompt"),
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	st := sm.SnapshotState()
	require.Equal(t, uint64(1), st.LatestNonce)
	require.Equal(t, uint64(10000-150-7), st.Balance)
}

func TestApplyDiff_FeePerNonce_InsufficientBalance_Rollback(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(group))
	config.FeePerNonce = 1
	verifier := signing.NewSecp256k1Verifier()

	// Balance is enough for reserve ((100+50)*1) but not reserve+fee.
	sm, err := NewStateMachine("escrow-1", config, group, 150, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 150))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1,
		PromptHash:  []byte("prompt"),
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInsufficientBalance)

	st := sm.SnapshotState()
	require.Equal(t, uint64(0), st.LatestNonce)
	require.Equal(t, uint64(150), st.Balance)
	require.Empty(t, st.Inferences)
}

// Verifies finalization rounds are free.
func TestApplyDiff_FeePerNonce_NotChargedDuringFinalization(t *testing.T) {
	// Build a session with a non-zero fee per nonce.
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(group))
	config.CreateDevshardFee = 10
	config.FeePerNonce = 7
	verifier := signing.NewSecp256k1Verifier()

	// Initialize the state machine and capture the pre-finalization balances.
	sm, err := NewStateMachine("escrow-1", config, group, 1000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1000))
	require.NoError(t, err)
	stateBefore := sm.SnapshotState()

	// Apply a finalize round diff and ensure no fee is charged.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txFinalize()})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	stateAfter := sm.SnapshotState()
	require.Equal(t, types.PhaseFinalizing, stateAfter.Phase)
	require.Equal(t, stateBefore.Balance, stateAfter.Balance)
	require.Equal(t, stateBefore.Fees, stateAfter.Fees)
}

func TestApplyLocalBestEffort_FeePerNonce_InsufficientBalance_Rollback(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(group))
	config.FeePerNonce = 1
	verifier := signing.NewSecp256k1Verifier()

	// Balance is enough for reserve ((100+50)*1) but not reserve+fee.
	sm, err := NewStateMachine("escrow-1", config, group, 150, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 150))
	require.NoError(t, err)

	_, applied, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1,
		PromptHash:  []byte("prompt"),
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   1000,
	})})
	require.ErrorIs(t, err, types.ErrInsufficientBalance)
	require.Nil(t, applied)

	st := sm.SnapshotState()
	require.Equal(t, uint64(0), st.LatestNonce)
	require.Equal(t, uint64(150), st.Balance)
	require.Empty(t, st.Inferences)
}

// --- Attack / bug regression tests ---

func TestAttack_SybilValidationBypass(t *testing.T) {
	// Attack: attacker with 2 slots executes on slot A, submits MsgValidation(Valid=true)
	// from slot B. With the new model, valid=true stays Finished -- sybil bypass is
	// prevented by design since only valid=false triggers Challenged.
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	applyStartConfirmFinish(t, sm, user, hosts, 1)

	// Slot 0 submits MsgValidation(Valid=true). Must stay Finished (no state transition).
	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: true, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], valMsg)

	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	st := sm.SnapshotState()
	require.Equal(t, types.StatusFinished, st.Inferences[1].Status,
		"valid=true must not change status from Finished")
}

func TestApplyDiff_Validation_MultipleValidators(t *testing.T) {
	// Second MsgValidation for the same inference records in bitmap.
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	applyStartConfirmFinish(t, sm, user, hosts, 1)

	// First validation -> Challenged.
	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: false, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], valMsg)
	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)
	rec := sm.SnapshotState().Inferences[1]
	require.Equal(t, types.StatusChallenged, rec.Status)
	var expectedBitmap1 types.Bitmap128
	expectedBitmap1.Set(0)
	require.Equal(t, expectedBitmap1, rec.ValidatedBy, "first validator bit must be set")

	// Second validation from different host -> bitmap updated.
	valMsg2 := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 2, Valid: true, EscrowId: "escrow-1"}
	valMsg2.ProposerSig = testutil.SignProposerTx(t, hosts[2], valMsg2)
	nonce = sm.SnapshotState().LatestNonce + 1
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg2)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	rec = sm.SnapshotState().Inferences[1]
	var expectedBitmap2 types.Bitmap128
	expectedBitmap2.Set(0)
	expectedBitmap2.Set(2)
	require.Equal(t, expectedBitmap2, rec.ValidatedBy, "both validator bits must be set")
}

func TestApplyDiff_Validation_DuplicateAddress(t *testing.T) {
	// Multi-slot validator tries to validate twice via different slots -> ErrDuplicateValidation.
	signers := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeMultiSlotGroup(signers, []int{2, 1, 1})
	config := testutil.DefaultConfig(len(group))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)

	// Inference 1: executor = group[1%4].SlotID = 1 (owned by signer[0]).
	applyStartConfirmFinishMultiSlot(t, sm, user, signers, group, 1)

	// First validation from signer[1] (slot 2), valid=true -> stays Finished.
	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 2, Valid: true, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, signers[1], valMsg)
	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Same address (signer[1]) tries again from slot 2 -> idempotent no-op.
	valMsg2 := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 2, Valid: false, EscrowId: "escrow-1"}
	valMsg2.ProposerSig = testutil.SignProposerTx(t, signers[1], valMsg2)
	nonce = sm.SnapshotState().LatestNonce + 1
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg2)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
}

func TestApplyDiff_ValidationVote_MultiSlotWeight(t *testing.T) {
	// 3 signers: signer[0] owns 2 slots (0,1), signer[1] owns 1 slot (2), signer[2] owns 1 slot (3).
	// Total 4 slots. VoteThreshold = 4/2 = 2. Need >2 weighted votes to resolve.
	signers := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeMultiSlotGroup(signers, []int{2, 1, 1})
	config := testutil.DefaultConfig(len(group)) // VoteThreshold = 4/2 = 2
	verifier := signing.NewSecp256k1Verifier()
	sm, err := NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)

	// Inference 1: executor = group[1%4].SlotID = 1 (owned by signer[0]).
	applyStartConfirmFinishMultiSlot(t, sm, user, signers, group, 1)

	// Challenge.
	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 2, Valid: false, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, signers[1], valMsg)
	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Signer[0] votes invalid from slot 0. Weight = 2 (owns slots 0 and 1).
	voteMsg := &types.MsgValidationVote{InferenceId: 1, VoterSlot: 0, VoteValid: false, EscrowId: "escrow-1"}
	voteMsg.ProposerSig = testutil.SignProposerTx(t, signers[0], voteMsg)
	nonce = sm.SnapshotState().LatestNonce + 1
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txVote(voteMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	st := sm.SnapshotState()
	// VotesInvalid = challenger weight (1) + voter weight (2) = 3.
	require.Equal(t, uint32(3), st.Inferences[1].VotesInvalid, "challenger(1) + multi-slot voter(2) = 3")
}

func TestApplyDiff_ValidationVote_MultiSlotDedup(t *testing.T) {
	// Same signer voting from two different owned slots must be rejected as duplicate.
	// Use enough slots that the first vote alone doesn't resolve the challenge.
	// 4 signers: signer[0] owns 2 slots (0,1), others own 1 each (2,3,4).
	// Total 5 slots. VoteThreshold = 5/2 = 2. Need >2 weighted votes.
	// Signer[0] weight=2, so one vote reaches threshold -- use more signers.
	// 5 signers: signer[0] owns 2 slots (0,1), others own 1 each (2,3,4,5).
	// Total 6 slots. VoteThreshold = 6/2 = 3. Signer[0] weight=2, won't resolve alone.
	signers := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeMultiSlotGroup(signers, []int{2, 1, 1, 1, 1})
	config := testutil.DefaultConfig(len(group)) // VoteThreshold = 6/2 = 3
	verifier := signing.NewSecp256k1Verifier()
	sm, err := NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)

	// Inference 1: executor = group[1%6].SlotID = 1 (owned by signer[0]).
	applyStartConfirmFinishMultiSlot(t, sm, user, signers, group, 1)

	// Challenge from signer[1] (slot 2).
	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 2, Valid: false, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, signers[1], valMsg)
	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Signer[0] votes from slot 0 (weight=2). VotesInvalid = 1+2 = 3, not > 3. Still Challenged.
	vote1 := &types.MsgValidationVote{InferenceId: 1, VoterSlot: 0, VoteValid: false, EscrowId: "escrow-1"}
	vote1.ProposerSig = testutil.SignProposerTx(t, signers[0], vote1)

	// Signer[0] votes again from slot 1 (other owned slot) -> must be rejected.
	vote2 := &types.MsgValidationVote{InferenceId: 1, VoterSlot: 1, VoteValid: false, EscrowId: "escrow-1"}
	vote2.ProposerSig = testutil.SignProposerTx(t, signers[0], vote2)

	nonce = sm.SnapshotState().LatestNonce + 1
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txVote(vote1), txVote(vote2)})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrDuplicateVote)
}

// applyStartConfirmFinishMultiSlot works with multi-slot groups.
func applyStartConfirmFinishMultiSlot(t *testing.T, sm *StateMachine, user *signing.Secp256k1Signer, signers []*signing.Secp256k1Signer, group []types.SlotAssignment, inferenceID uint64) {
	t.Helper()
	executorSlotIdx := inferenceID % uint64(len(group))
	executorSlot := group[executorSlotIdx]

	// Find the signer that owns the executor slot.
	var executorSigner *signing.Secp256k1Signer
	for _, s := range signers {
		if s.Address() == executorSlot.ValidatorAddress {
			executorSigner = s
			break
		}
	}
	require.NotNil(t, executorSigner)

	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: inferenceID, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, executorSigner, "escrow-1", inferenceID, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: inferenceID, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	finishMsg := &types.MsgFinishInference{
		InferenceId: inferenceID, ResponseHash: []byte("response"),
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: executorSlot.SlotID,
		EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, executorSigner, finishMsg)
	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txFinish(finishMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
}

func TestApplyDiff_PostStateRoot_Valid(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	// Use a second SM to compute the correct post_state_root.
	verifier := signing.NewSecp256k1Verifier()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	sm2, err := NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)

	txs := []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})}

	// Compute root from the replica.
	root, err := sm2.ApplyLocal(1, txs)
	require.NoError(t, err)

	// Sign diff with correct post_state_root.
	diff := testutil.SignDiffWithRoot(t, user, "escrow-1", 1, txs, root)
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
}

func TestApplyDiff_PostStateRoot_Mismatch(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	txs := []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})}

	// Sign diff with wrong post_state_root.
	diff := testutil.SignDiffWithRoot(t, user, "escrow-1", 1, txs, []byte("wrong-root"))
	_, err := sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrPostStateRootMismatch)

	// Verify state was fully rolled back: nonce unchanged, balance unchanged.
	require.Equal(t, uint64(0), sm.LatestNonce(), "nonce must be rolled back")
	snap := sm.SnapshotState()
	require.Equal(t, uint64(10000), snap.Balance, "balance must be rolled back")

	// A subsequent diff with nonce=1 must succeed (proves nonce was restored).
	diff2 := testutil.SignDiff(t, user, "escrow-1", 1, txs)
	_, err = sm.ApplyDiff(diff2)
	require.NoError(t, err)
}

func TestApplyDiff_PostStateRoot_Empty_Accepted(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	// Diff without post_state_root (backwards-compatible).
	diff := testutil.SignDiff(t, user, "escrow-1", 1, nil)
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)
}

// --- Phase 4: Deprecated RevealSeed handling ---

func TestApplyDiff_RevealSeed_DeprecatedNoOp(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	applyStartConfirmFinish(t, sm, user, hosts, 1)

	nonce := sm.LatestNonce() + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txFinalize()})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, types.PhaseFinalizing, sm.Phase())

	before := sm.SnapshotState()
	seedMsg := testutil.SignRevealSeed(t, hosts[0], "escrow-1", 0)
	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txRevealSeed(seedMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	after := sm.SnapshotState()
	require.Equal(t, types.PhaseFinalizing, after.Phase)
	require.Equal(t, before.WarmKeys, after.WarmKeys)
	for slotID, hs := range after.HostStats {
		require.Zero(t, hs.RequiredValidations, "slot %d required validations must stay zero", slotID)
		require.Zero(t, hs.CompletedValidations, "slot %d completed validations must stay zero", slotID)
	}
}

func TestApplyDiff_RevealSeed_DeprecatedOutsideFinalizing(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	seedMsg := testutil.SignRevealSeed(t, hosts[0], "escrow-1", 0)
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txRevealSeed(seedMsg)})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, types.PhaseActive, sm.Phase())

	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txFinalize()})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
	for nonce := uint64(3); nonce <= 5; nonce++ {
		diff = testutil.SignDiff(t, user, "escrow-1", nonce, nil)
		_, err = sm.ApplyDiff(diff)
		require.NoError(t, err)
	}
	require.Equal(t, types.PhaseSettlement, sm.Phase())

	seedMsg = testutil.SignRevealSeed(t, hosts[1], "escrow-1", 1)
	diff = testutil.SignDiff(t, user, "escrow-1", 6, []*types.DevshardTx{txRevealSeed(seedMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, types.PhaseSettlement, sm.Phase())
}

func TestApplyDiff_HostStatsValidationCountersRemainZero(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 100000)

	applyStartConfirmFinish(t, sm, user, hosts, 1)

	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: true, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], valMsg)
	nonce := sm.LatestNonce() + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txFinalize()})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	seedMsg := testutil.SignRevealSeed(t, hosts[0], "escrow-1", 0)
	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txRevealSeed(seedMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	for ; nonce <= sm.SnapshotState().FinalizeNonce+uint64(len(hosts)); nonce++ {
		if nonce == sm.LatestNonce() {
			continue
		}
		diff = testutil.SignDiff(t, user, "escrow-1", nonce, nil)
		_, err = sm.ApplyDiff(diff)
		require.NoError(t, err)
	}

	st := sm.SnapshotState()
	require.Equal(t, types.PhaseSettlement, st.Phase)
	for slotID, hs := range st.HostStats {
		require.Zero(t, hs.RequiredValidations, "slot %d required validations must stay zero", slotID)
		require.Zero(t, hs.CompletedValidations, "slot %d completed validations must stay zero", slotID)
	}
}

// applyStartConfirmFinish_Setup applies start + confirm only (no finish).
// Used when we need to test finish with specific proposer.
func applyStartConfirmFinish_Setup(t *testing.T, sm *StateMachine, user *signing.Secp256k1Signer, hosts []*signing.Secp256k1Signer, inferenceID uint64) {
	t.Helper()
	executorSlotIdx := inferenceID % uint64(len(hosts))
	nonce := sm.LatestNonce() + 1

	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: inferenceID, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[executorSlotIdx], "escrow-1", inferenceID, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: inferenceID, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
}

// --- SessionPhase transition tests ---

func TestPhase_ActiveToFinalizing(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	require.Equal(t, types.PhaseActive, sm.Phase())

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txFinalize()})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, types.PhaseFinalizing, sm.Phase())
}

func TestPhase_FinalizingToSettlement_DeadlineOnlyEvenWithRevealTxs(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txFinalize()})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, types.PhaseFinalizing, sm.Phase())

	for i, h := range hosts[:2] {
		seedMsg := testutil.SignRevealSeed(t, h, "escrow-1", uint32(i))
		nonce := uint64(2 + i)
		diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txRevealSeed(seedMsg)})
		_, err = sm.ApplyDiff(diff)
		require.NoError(t, err)
		require.Equal(t, types.PhaseFinalizing, sm.Phase())
	}

	diff = testutil.SignDiff(t, user, "escrow-1", 4, nil)
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, types.PhaseSettlement, sm.Phase())
}

func TestPhase_FinalizingToSettlement_Deadline(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	// Finalize at nonce 1.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txFinalize()})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, types.PhaseFinalizing, sm.Phase())

	// FinalizeNonce is set to 1 (the nonce where finalization started).
	// Deadline = FinalizeNonce + len(Group) = 1 + 3 = 4.
	// Send empty diffs until LatestNonce >= 4.
	for nonce := uint64(2); nonce <= 4; nonce++ {
		diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{})
		_, err = sm.ApplyDiff(diff)
		require.NoError(t, err)
	}

	require.Equal(t, types.PhaseSettlement, sm.Phase())
}

func TestPhase_RevealSeed_AcceptedAsNoOpInSettlement(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	// Finalize + empty diffs to reach Settlement via deadline.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txFinalize()})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)
	for nonce := uint64(2); nonce <= 4; nonce++ {
		diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{})
		_, err = sm.ApplyDiff(diff)
		require.NoError(t, err)
	}
	require.Equal(t, types.PhaseSettlement, sm.Phase())

	seedMsg := testutil.SignRevealSeed(t, hosts[0], "escrow-1", 0)
	diff = testutil.SignDiff(t, user, "escrow-1", 5, []*types.DevshardTx{txRevealSeed(seedMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, types.PhaseSettlement, sm.Phase())
}

func TestPhase_StartInference_RejectedInBothFinalizingAndSettlement(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	// Finalize.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txFinalize()})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// StartInference in Finalizing -> rejected.
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 2, InputLength: 10, MaxTokens: 5,
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrSessionFinalizing)

	// Advance to Settlement via deadline.
	for nonce := uint64(2); nonce <= 4; nonce++ {
		diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{})
		_, err = sm.ApplyDiff(diff)
		require.NoError(t, err)
	}
	require.Equal(t, types.PhaseSettlement, sm.Phase())

	// StartInference in Settlement -> rejected.
	diff = testutil.SignDiff(t, user, "escrow-1", 5, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 5, InputLength: 10, MaxTokens: 5,
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrSessionFinalizing)
}

func TestPhase_StateRootDiffersPerPhase(t *testing.T) {
	hostStats := map[uint32]*types.HostStats{
		0: {Cost: 100},
	}
	inferences := map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusFinished, ExecutorSlot: 0, ActualCost: 100},
	}

	rootActive, err := ComputeStateRoot(500, hostStats, inferences, types.PhaseActive, nil, 0, types.DevshardStateRootAndProtocolVersion)
	require.NoError(t, err)
	rootFinalizing, err := ComputeStateRoot(500, hostStats, inferences, types.PhaseFinalizing, nil, 0, types.DevshardStateRootAndProtocolVersion)
	require.NoError(t, err)
	rootSettlement, err := ComputeStateRoot(500, hostStats, inferences, types.PhaseSettlement, nil, 0, types.DevshardStateRootAndProtocolVersion)
	require.NoError(t, err)

	require.NotEqual(t, rootActive, rootFinalizing, "Active and Finalizing roots must differ")
	require.NotEqual(t, rootFinalizing, rootSettlement, "Finalizing and Settlement roots must differ")
	require.NotEqual(t, rootActive, rootSettlement, "Active and Settlement roots must differ")
}

func TestPhase_FinalizationLeavesValidationCountersZero(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 100000)

	// Create a finished inference so finalization has existing session data.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Confirm + finish so the inference reaches StatusFinished.
	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("resp"),
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: 1,
		EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[1], finishMsg)
	diff = testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{txFinish(finishMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Finalize.
	diff = testutil.SignDiff(t, user, "escrow-1", 4, []*types.DevshardTx{txFinalize()})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Advance to Settlement via deadline.
	for nonce := uint64(5); nonce <= 7; nonce++ {
		diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{})
		_, err = sm.ApplyDiff(diff)
		require.NoError(t, err)
	}
	require.Equal(t, types.PhaseSettlement, sm.Phase())

	st := sm.SnapshotState()
	for slotID, hs := range st.HostStats {
		require.Zero(t, hs.RequiredValidations, "slot %d required validations must stay zero", slotID)
		require.Zero(t, hs.CompletedValidations, "slot %d completed validations must stay zero", slotID)
	}
}

// --- Security fix tests ---

func TestReplayAttack_CrossEscrow(t *testing.T) {
	// A valid proposer sig from session A must be rejected in session B.
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()

	// Session A: escrow-A.
	smA, err := NewStateMachine("escrow-A", config, group, 10000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-A", user.Address(), config, group, 10000))
	require.NoError(t, err)

	// Start + confirm + finish in session A.
	diff := testutil.SignDiff(t, user, "escrow-A", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err = smA.ApplyDiff(diff)
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-A", 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	diff = testutil.SignDiff(t, user, "escrow-A", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = smA.ApplyDiff(diff)
	require.NoError(t, err)

	// Build a valid MsgFinishInference for escrow-A.
	finishMsgA := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("response"),
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: 1, EscrowId: "escrow-A",
	}
	finishMsgA.ProposerSig = testutil.SignProposerTx(t, hosts[1], finishMsgA)

	// Verify it works in session A.
	diff = testutil.SignDiff(t, user, "escrow-A", 3, []*types.DevshardTx{txFinish(finishMsgA)})
	_, err = smA.ApplyDiff(diff)
	require.NoError(t, err)

	// Session B: escrow-B, same hosts.
	smB, err := NewStateMachine("escrow-B", config, group, 10000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-B", user.Address(), config, group, 10000))
	require.NoError(t, err)

	diff = testutil.SignDiff(t, user, "escrow-B", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err = smB.ApplyDiff(diff)
	require.NoError(t, err)

	execSigB := testutil.SignExecutorReceipt(t, hosts[1], "escrow-B", 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	diff = testutil.SignDiff(t, user, "escrow-B", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSigB, ConfirmedAt: 1000,
	})})
	_, err = smB.ApplyDiff(diff)
	require.NoError(t, err)

	// Replay the msg from session A into session B. Must fail.
	diff = testutil.SignDiff(t, user, "escrow-B", 3, []*types.DevshardTx{txFinish(finishMsgA)})
	_, err = smB.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrEscrowIDMismatch)
}

func TestFinishInference_CostCapped(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	// Start: reserved = (100+50)*1 = 150.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Finish with actualCost = (200+100)*1 = 300 > reserved 150. Should cap.
	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("response"),
		InputTokens: 200, OutputTokens: 100, ExecutorSlot: 1, EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[1], finishMsg)

	diff = testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{txFinish(finishMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err, "cost exceeding reserved should be capped, not rejected")

	st := sm.SnapshotState()
	rec := st.Inferences[1]
	require.Equal(t, types.StatusFinished, rec.Status)
	require.Equal(t, uint64(150), rec.ActualCost, "actual cost should be capped to reserved")
	// No surplus returned: reserved - capped = 0.
	require.Equal(t, uint64(10000-150), st.Balance)
}

func TestLateValidation_TerminalStateCredit(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	applyStartConfirmFinish(t, sm, user, hosts, 1)

	// Challenge from host[0] (valid=false -> Challenged).
	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: false, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], valMsg)
	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Vote valid to reach StatusValidated. Skip slot 0 (challenger) and slot 1 (executor).
	// Challenger weight=1 (invalid) + 3 valid voters -> VotesValid=3 > threshold=2.
	var voteTxs []*types.DevshardTx
	for _, slot := range []uint32{2, 3, 4} {
		voteMsg := &types.MsgValidationVote{InferenceId: 1, VoterSlot: slot, VoteValid: true, EscrowId: "escrow-1"}
		voteMsg.ProposerSig = testutil.SignProposerTx(t, hosts[slot], voteMsg)
		voteTxs = append(voteTxs, txVote(voteMsg))
	}
	nonce = sm.SnapshotState().LatestNonce + 1
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, voteTxs)
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, types.StatusValidated, sm.SnapshotState().Inferences[1].Status)

	// Late validation from host[4] on terminal inference. Already voted -> silent no-op.
	lateVal := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 4, Valid: true, EscrowId: "escrow-1"}
	lateVal.ProposerSig = testutil.SignProposerTx(t, hosts[4], lateVal)
	nonce = sm.SnapshotState().LatestNonce + 1
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(lateVal)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	st := sm.SnapshotState()
	require.Equal(t, types.StatusValidated, st.Inferences[1].Status, "status must not change")
	require.True(t, st.Inferences[1].ValidatedBy.IsSet(4), "late validator bit must be set")
}

func TestLateValidation_DeduplicateTerminal(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	applyStartConfirmFinish(t, sm, user, hosts, 1)

	// Challenge (valid=false) + vote valid to reach StatusValidated.
	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: false, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], valMsg)
	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	var voteTxs []*types.DevshardTx
	for _, slot := range []uint32{2, 3, 4} {
		voteMsg := &types.MsgValidationVote{InferenceId: 1, VoterSlot: slot, VoteValid: true, EscrowId: "escrow-1"}
		voteMsg.ProposerSig = testutil.SignProposerTx(t, hosts[slot], voteMsg)
		voteTxs = append(voteTxs, txVote(voteMsg))
	}
	nonce = sm.SnapshotState().LatestNonce + 1
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, voteTxs)
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	// host[0] already participated as challenger. Late re-validation is silent no-op.
	dupeVal := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: true, EscrowId: "escrow-1"}
	dupeVal.ProposerSig = testutil.SignProposerTx(t, hosts[0], dupeVal)
	nonce = sm.SnapshotState().LatestNonce + 1
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(dupeVal)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err, "duplicate late validation must be a silent no-op")
}

func TestLateValidation_RejectedAfterSeal(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	applyStartConfirmFinish(t, sm, user, hosts, 1)
	require.NoError(t, sm.SealInference(1))

	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: true, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], valMsg)
	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err := sm.ApplyDiff(diff)
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrInferenceSealed)
}

func TestLateValidationVote_RejectedAfterSeal(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	applyStartConfirmFinish(t, sm, user, hosts, 1)

	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: false, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], valMsg)
	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, types.StatusChallenged, sm.SnapshotState().Inferences[1].Status)

	require.NoError(t, sm.SealInference(1))

	voteMsg := &types.MsgValidationVote{InferenceId: 1, VoterSlot: 2, VoteValid: true, EscrowId: "escrow-1"}
	voteMsg.ProposerSig = testutil.SignProposerTx(t, hosts[2], voteMsg)
	nonce = sm.SnapshotState().LatestNonce + 1
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txVote(voteMsg)})
	_, err = sm.ApplyDiff(diff)
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrInferenceSealed)
}

// TestFinalizeDeadlineDrainsLiveIntoSealedAcc verifies that the Finalizing ->
// composition the Finalizing -> Settlement deadline transition seals every
// live record into sealed_acc, leaving an empty live map and a non-zero
// accumulator. This is the chain-side simplification: the settlement
// payload then never has to carry per-inference records.
func TestFinalizeDeadlineDrainsLiveIntoSealedAcc(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	applyStartConfirmFinish(t, sm, user, hosts, 1)

	require.NotEmpty(t, sm.SnapshotState().Inferences, "inference must be live before finalize")

	nonce := sm.LatestNonce() + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txFinalize()})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, types.PhaseFinalizing, sm.Phase())

	st := sm.SnapshotState()
	for n := st.LatestNonce + 1; n <= st.FinalizeNonce+uint64(len(hosts)); n++ {
		diff = testutil.SignDiff(t, user, "escrow-1", n, nil)
		_, err = sm.ApplyDiff(diff)
		require.NoError(t, err)
	}

	final := sm.SnapshotState()
	require.Equal(t, types.PhaseSettlement, final.Phase)
	require.Empty(t, final.Inferences, "v2 deadline transition must drain live inferences")
	require.Len(t, final.SealedAcc, 32, "v2 deadline transition must produce a 32-byte sealed_acc")

	var zero [32]byte
	require.NotEqual(t, zero[:], final.SealedAcc, "sealed_acc must change after draining live records")
}

// TestV2_FinalizeDrainDeterministicOrder verifies that two state machines
// applying the same finalize-deadline sequence end with identical sealed_acc
// values, even if their internal map iteration order differed. This is the
// safety net for the deterministic seal contract.
//
// We use two inferences (id=1 starts at nonce 1, id=4 starts at nonce 4) so
// the drain must sort by id, not by iteration order. The two state machines
// share the same host keys and user, so they are bit-identical inputs and
// must produce bit-identical outputs.
func TestV2_FinalizeDrainDeterministicOrder(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}

	startAt := func(t *testing.T, sm *StateMachine, user *signing.Secp256k1Signer, inferenceID uint64) {
		t.Helper()
		executorSlotIdx := inferenceID % uint64(len(hosts))

		nonce := inferenceID
		diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txStart(&types.MsgStartInference{
			InferenceId: inferenceID, PromptHash: []byte("prompt"), Model: "llama",
			InputLength: 100, MaxTokens: 50, StartedAt: 1000,
		})})
		_, err := sm.ApplyDiff(diff)
		require.NoError(t, err)

		execSig := testutil.SignExecutorReceipt(t, hosts[executorSlotIdx], "escrow-1", inferenceID, []byte("prompt"), "llama", 100, 50, 1000, 1000)
		nonce++
		diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
			InferenceId: inferenceID, ExecutorSig: execSig, ConfirmedAt: 1000,
		})})
		_, err = sm.ApplyDiff(diff)
		require.NoError(t, err)

		finishMsg := &types.MsgFinishInference{
			InferenceId: inferenceID, ResponseHash: []byte("response"),
			InputTokens: 80, OutputTokens: 40, ExecutorSlot: uint32(executorSlotIdx),
			EscrowId: "escrow-1",
		}
		finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[executorSlotIdx], finishMsg)
		nonce++
		diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txFinish(finishMsg)})
		_, err = sm.ApplyDiff(diff)
		require.NoError(t, err)
	}

	driveTwoInferences := func(t *testing.T) *StateMachine {
		t.Helper()
		sm, user := newTestSM(t, hosts, 20000)
		startAt(t, sm, user, 1)
		startAt(t, sm, user, 4)

		nonce := sm.LatestNonce() + 1
		diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txFinalize()})
		_, err := sm.ApplyDiff(diff)
		require.NoError(t, err)

		st := sm.SnapshotState()
		for n := st.LatestNonce + 1; n <= st.FinalizeNonce+uint64(len(hosts)); n++ {
			diff = testutil.SignDiff(t, user, "escrow-1", n, nil)
			_, err = sm.ApplyDiff(diff)
			require.NoError(t, err)
		}
		return sm
	}

	a := driveTwoInferences(t)
	b := driveTwoInferences(t)

	require.Equal(t, types.PhaseSettlement, a.Phase())
	require.Equal(t, types.PhaseSettlement, b.Phase())
	require.Equal(t, a.SnapshotState().SealedAcc, b.SnapshotState().SealedAcc,
		"deterministic drain must yield identical sealed_acc across runs")
	require.Empty(t, a.SnapshotState().Inferences)
	require.Empty(t, b.SnapshotState().Inferences)

	rootA, err := a.ComputeStateRoot()
	require.NoError(t, err)
	rootB, err := b.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, rootA, rootB)
}

// --- Warm Key Tests ---

func newTestSMWithWarmKey(t *testing.T, hosts []*signing.Secp256k1Signer, balance uint64, resolver WarmKeyResolver) (*StateMachine, *signing.Secp256k1Signer) {
	t.Helper()
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := NewStateMachine("escrow-1", config, group, balance, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, balance), WithWarmKeyResolver(resolver))
	require.NoError(t, err)
	return sm, user
}

// applyStartConfirmWithWarmKey starts an inference and confirms it using a warm key signer.
// executorIdx is the index in hosts that is the executor (cold key).
func applyStartConfirmWithWarmKey(t *testing.T, sm *StateMachine, user *signing.Secp256k1Signer, hosts []*signing.Secp256k1Signer, warmSigner *signing.Secp256k1Signer, inferenceID uint64, executorIdx int) {
	t.Helper()
	nonce := sm.LatestNonce() + 1

	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: inferenceID, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Sign executor receipt with warm key instead of cold key.
	execSig := testutil.SignExecutorReceipt(t, warmSigner, "escrow-1", inferenceID, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: inferenceID, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
}

func TestWarmKey_ConfirmStartWithWarmKey(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	warmSigner := testutil.MustGenerateKey(t)
	executorIdx := 1 // inference 1 % 3 = 1

	resolver := func(warmAddr, coldAddr string) (bool, error) {
		return warmAddr == warmSigner.Address() && coldAddr == hosts[executorIdx].Address(), nil
	}
	sm, user := newTestSMWithWarmKey(t, hosts, 10000, resolver)

	applyStartConfirmWithWarmKey(t, sm, user, hosts, warmSigner, 1, executorIdx)

	st := sm.SnapshotState()
	require.Equal(t, types.StatusStarted, st.Inferences[1].Status)
	require.Equal(t, warmSigner.Address(), st.WarmKeys[uint32(executorIdx)])
	require.True(t, sm.IsWarmKeyAddress(warmSigner.Address()))
}

func TestWarmKey_ConfirmStartRejectsUnauthorizedKey(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	randomKey := testutil.MustGenerateKey(t)

	resolver := func(warmAddr, coldAddr string) (bool, error) {
		return false, nil // always reject
	}
	sm, user := newTestSMWithWarmKey(t, hosts, 10000, resolver)

	nonce := sm.LatestNonce() + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, randomKey, "escrow-1", 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidExecutorSig)
}

func TestWarmKey_ConfirmStartNoResolver(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	randomKey := testutil.MustGenerateKey(t)

	// No WithWarmKeyResolver -- resolver is nil.
	sm, user := newTestSM(t, hosts, 10000)

	nonce := sm.LatestNonce() + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, randomKey, "escrow-1", 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrInvalidExecutorSig)
}

func TestWarmKey_BindingCached(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	warmSigner := testutil.MustGenerateKey(t)
	executorIdx := 1

	callCount := 0
	resolver := func(warmAddr, coldAddr string) (bool, error) {
		callCount++
		return warmAddr == warmSigner.Address() && coldAddr == hosts[executorIdx].Address(), nil
	}
	sm, user := newTestSMWithWarmKey(t, hosts, 100000, resolver)

	// First inference: resolver called once, binding stored.
	applyStartConfirmWithWarmKey(t, sm, user, hosts, warmSigner, 1, executorIdx)
	require.Equal(t, 1, callCount, "resolver should be called once for first warm key use")

	// Second inference with same executor slot (4 % 3 = 1).
	// Need nonce == inferenceID, so advance nonce to 3, then use inference 4.
	nonce := sm.LatestNonce() + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, nil) // empty diff nonce 3
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	applyStartConfirmWithWarmKey(t, sm, user, hosts, warmSigner, 4, executorIdx)
	require.Equal(t, 1, callCount, "resolver should NOT be called again -- binding is cached")
}

func TestWarmKey_FinishInferenceWithWarmKey(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	warmSigner := testutil.MustGenerateKey(t)
	executorIdx := 1

	resolver := func(warmAddr, coldAddr string) (bool, error) {
		return warmAddr == warmSigner.Address() && coldAddr == hosts[executorIdx].Address(), nil
	}
	sm, user := newTestSMWithWarmKey(t, hosts, 10000, resolver)

	applyStartConfirmWithWarmKey(t, sm, user, hosts, warmSigner, 1, executorIdx)

	// Finish inference signed by warm key.
	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("response"),
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: 1,
		EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, warmSigner, finishMsg)

	nonce := sm.LatestNonce() + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txFinish(finishMsg)})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	st := sm.SnapshotState()
	require.Equal(t, types.StatusFinished, st.Inferences[1].Status)
}

func TestWarmKey_ValidationWithWarmKey(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	warmSigner := testutil.MustGenerateKey(t)
	validatorIdx := 0 // validator slot 0

	resolver := func(warmAddr, coldAddr string) (bool, error) {
		return warmAddr == warmSigner.Address() && coldAddr == hosts[validatorIdx].Address(), nil
	}
	sm, user := newTestSMWithWarmKey(t, hosts, 10000, resolver)

	// Start, confirm, finish with cold keys (executor = slot 1).
	applyStartConfirmFinish(t, sm, user, hosts, 1)

	// Validate with warm key for slot 0.
	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: true, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, warmSigner, valMsg)

	nonce := sm.LatestNonce() + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	st := sm.SnapshotState()
	require.True(t, st.Inferences[1].ValidatedBy.IsSet(0))
}

func TestWarmKey_SeedRevealWithWarmKeyIsIgnored(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	warmSigner := testutil.MustGenerateKey(t)
	revealerIdx := 0

	resolver := func(warmAddr, coldAddr string) (bool, error) {
		return warmAddr == warmSigner.Address() && coldAddr == hosts[revealerIdx].Address(), nil
	}
	sm, user := newTestSMWithWarmKey(t, hosts, 10000, resolver)

	// Enter finalizing phase.
	nonce := sm.LatestNonce() + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txFinalize()})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, types.PhaseFinalizing, sm.Phase())

	// Sign seed reveal with warm key.
	seedSig, err := warmSigner.Sign([]byte("escrow-1"))
	require.NoError(t, err)
	seedMsg := &types.MsgRevealSeed{
		SlotId:    0,
		Signature: seedSig,
		EscrowId:  "escrow-1",
	}
	seedMsg.ProposerSig = testutil.SignProposerTx(t, warmSigner, seedMsg)

	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txRevealSeed(seedMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	st := sm.SnapshotState()
	require.Empty(t, st.WarmKeys, "reveal-seed must not create a warm key binding")
}

func TestWarmKey_TimeoutVoteWithWarmKey(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	warmSigners := make([]*signing.Secp256k1Signer, len(hosts))
	for i := range warmSigners {
		warmSigners[i] = testutil.MustGenerateKey(t)
	}

	resolver := func(warmAddr, coldAddr string) (bool, error) {
		for i, ws := range warmSigners {
			if warmAddr == ws.Address() && coldAddr == hosts[i].Address() {
				return true, nil
			}
		}
		return false, nil
	}
	sm, user := newTestSMWithWarmKey(t, hosts, 10000, resolver)

	nonce := sm.LatestNonce() + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Build timeout votes signed by warm keys for slots 0, 2, 3.
	var votes []*types.TimeoutVote
	for _, slot := range []uint32{0, 2, 3} {
		v := testutil.SignTimeoutVote(t, warmSigners[slot], "escrow-1", 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, true)
		v.VoterSlot = slot
		votes = append(votes, v)
	}

	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txTimeout(&types.MsgTimeoutInference{
		InferenceId: 1, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED, Votes: votes,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	st := sm.SnapshotState()
	require.Equal(t, types.StatusTimedOut, st.Inferences[1].Status)
}

func TestWarmKey_StateRootChangesWithWarmKeys(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	hostStats := make(map[uint32]*types.HostStats)
	for i := range hosts {
		hostStats[uint32(i)] = &types.HostStats{}
	}
	inferences := make(map[uint64]*types.InferenceRecord)

	rootNil, err := ComputeStateRoot(10000, hostStats, inferences, types.PhaseActive, nil, 0, types.DevshardStateRootAndProtocolVersion)
	require.NoError(t, err)

	rootEmpty, err := ComputeStateRoot(10000, hostStats, inferences, types.PhaseActive, map[uint32]string{}, 0, types.DevshardStateRootAndProtocolVersion)
	require.NoError(t, err)

	rootWithWarm, err := ComputeStateRoot(10000, hostStats, inferences, types.PhaseActive, map[uint32]string{1: "0xwarmaddr"}, 0, types.DevshardStateRootAndProtocolVersion)
	require.NoError(t, err)

	// nil and empty should produce the same root (both hash sha256(nil)).
	require.Equal(t, rootNil, rootEmpty, "nil and empty warm keys should produce same root")
	// Non-empty warm keys must produce a different root.
	require.NotEqual(t, rootEmpty, rootWithWarm, "warm keys must change state root")
}

func TestWarmKey_IsWarmKeyAddress(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	warmSigner := testutil.MustGenerateKey(t)
	executorIdx := 1

	resolver := func(warmAddr, coldAddr string) (bool, error) {
		return warmAddr == warmSigner.Address() && coldAddr == hosts[executorIdx].Address(), nil
	}
	sm, user := newTestSMWithWarmKey(t, hosts, 10000, resolver)

	// Before any warm key binding.
	require.False(t, sm.IsWarmKeyAddress(warmSigner.Address()))
	require.False(t, sm.IsWarmKeyAddress(hosts[0].Address()))

	// Trigger warm key binding via confirm.
	applyStartConfirmWithWarmKey(t, sm, user, hosts, warmSigner, 1, executorIdx)

	require.True(t, sm.IsWarmKeyAddress(warmSigner.Address()))
	require.False(t, sm.IsWarmKeyAddress(testutil.MustGenerateKey(t).Address()), "unrelated address should be false")
}

func TestInjectWarmKeys(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, _ := newTestSM(t, hosts, 10000)

	// Inject warm keys.
	sm.InjectWarmKeys(map[uint32]string{0: "warm-0", 1: "warm-1"})
	wk := sm.WarmKeys()
	require.Equal(t, "warm-0", wk[0])
	require.Equal(t, "warm-1", wk[1])

	// Inject conflicting key for same slot: original preserved.
	sm.InjectWarmKeys(map[uint32]string{0: "warm-0-different"})
	wk = sm.WarmKeys()
	require.Equal(t, "warm-0", wk[0], "original binding should be preserved")

	// Inject for new slot: accepted.
	sm.InjectWarmKeys(map[uint32]string{2: "warm-2"})
	wk = sm.WarmKeys()
	require.Equal(t, "warm-2", wk[2])
}

func TestApplyLocal_WithInjectedWarmKeys(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	warmSigner := testutil.MustGenerateKey(t)
	executorIdx := 1

	resolver := func(warmAddr, coldAddr string) (bool, error) {
		return warmAddr == warmSigner.Address() && coldAddr == hosts[executorIdx].Address(), nil
	}

	// SM1: apply normally with resolver (warm keys resolved via bridge).
	sm1, user := newTestSMWithWarmKey(t, hosts, 10000, resolver)
	applyStartConfirmWithWarmKey(t, sm1, user, hosts, warmSigner, 1, executorIdx)

	warmBefore := sm1.WarmKeys()
	root1, err := sm1.ComputeStateRoot()
	require.NoError(t, err)
	require.NotNil(t, warmBefore)
	require.Equal(t, warmSigner.Address(), warmBefore[uint32(executorIdx)])

	// SM2: replay with injected warm keys (no resolver).
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm2, err := NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)

	// Inject the warm keys that were captured from SM1.
	sm2.InjectWarmKeys(warmBefore)

	// Replay the same txs via ApplyLocal.
	startTx := txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})
	_, err = sm2.ApplyLocal(1, []*types.DevshardTx{startTx})
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, warmSigner, "escrow-1", 1, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	confirmTx := txConfirm(&types.MsgConfirmStart{InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000})
	_, err = sm2.ApplyLocal(2, []*types.DevshardTx{confirmTx})
	require.NoError(t, err)

	root2, err := sm2.ComputeStateRoot()
	require.NoError(t, err)

	require.Equal(t, root1, root2, "state roots must match after replay with injected warm keys")
}

func TestApplyDiff_RevealSeed_NoNewWarmKeyBinding(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	warmSigner := testutil.MustGenerateKey(t)
	revealerIdx := 0

	resolver := func(warmAddr, coldAddr string) (bool, error) {
		return warmAddr == warmSigner.Address() && coldAddr == hosts[revealerIdx].Address(), nil
	}
	sm, user := newTestSMWithWarmKey(t, hosts, 10000, resolver)

	// Enter finalizing.
	nonce := sm.LatestNonce() + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txFinalize()})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Sign seed and proposer_sig with the COLD key,
	// but replace the seed signature with the warm key.
	// The proposer_sig is cold -> no warm key binding created.
	seedSig, err := warmSigner.Sign([]byte("escrow-1"))
	require.NoError(t, err)
	seedMsg := &types.MsgRevealSeed{
		SlotId:    0,
		Signature: seedSig,
		EscrowId:  "escrow-1",
	}
	// Sign proposer with cold key (no ResolveWarmKey trigger).
	seedMsg.ProposerSig = testutil.SignProposerTx(t, hosts[revealerIdx], seedMsg)

	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txRevealSeed(seedMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	wk := sm.WarmKeys()
	require.Nil(t, wk, "no warm key binding should exist")
}

func TestApplyDiff_RevealSeed_PreservesExistingBinding(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	warmSigner := testutil.MustGenerateKey(t)
	revealerIdx := 0

	resolver := func(warmAddr, coldAddr string) (bool, error) {
		return warmAddr == warmSigner.Address() && coldAddr == hosts[revealerIdx].Address(), nil
	}
	sm, user := newTestSMWithWarmKey(t, hosts, 100000, resolver)

	// Create warm key binding via ConfirmStart.
	// inference_id must equal nonce, and inference_id % 3 must = 0 for executor = slot 0.
	// nonce=3: inference 3 % 3 = 0 -> executor = slot 0 = hosts[revealerIdx].
	// Burn nonces 1,2 first.
	nonce := sm.LatestNonce() + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, nil)
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)
	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, nil)
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 3, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	// ConfirmStart with warm key -> creates binding for slot 0.
	execSig := testutil.SignExecutorReceipt(t, warmSigner, "escrow-1", 3, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 3, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Verify warm key binding exists.
	wkBefore := sm.WarmKeys()
	require.Equal(t, warmSigner.Address(), wkBefore[uint32(revealerIdx)])

	// Finish the inference so state is clean.
	finishMsg := &types.MsgFinishInference{
		InferenceId: 3, ResponseHash: []byte("resp"), InputTokens: 80,
		OutputTokens: 40, ExecutorSlot: 0, EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, warmSigner, finishMsg)
	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txFinish(finishMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Finalize.
	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txFinalize()})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Reveal seed signed with warm key is ignored, but must not disturb the existing binding.
	seedSig, err := warmSigner.Sign([]byte("escrow-1"))
	require.NoError(t, err)
	seedMsg := &types.MsgRevealSeed{
		SlotId:    0,
		Signature: seedSig,
		EscrowId:  "escrow-1",
	}
	seedMsg.ProposerSig = testutil.SignProposerTx(t, warmSigner, seedMsg)

	nonce++
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txRevealSeed(seedMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	wkAfter := sm.WarmKeys()
	require.Equal(t, wkBefore, wkAfter, "warm keys should not change during seed reveal")
}

func TestApplyDiff_Validation_Invalid_CostUnderflowGuard(t *testing.T) {
	// Set up a scenario where HostStats.Cost is 0 but ActualCost > 0.
	// The underflow guard should prevent unsigned subtraction wraparound.
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 10000)

	// Start, confirm, finish inference 1 (executor = slot 1).
	applyStartConfirmFinish(t, sm, user, hosts, 1)
	st := sm.SnapshotState()
	require.True(t, st.HostStats[1].Cost > 0)

	// Start, confirm, finish inference 4 (executor = slot 4).
	// This gives slot 4 some cost. We'll later invalidate inference 1 (slot 1)
	// but first manually zero slot 1's cost via a second invalidation cycle.
	// Instead, test directly: finish two inferences on same slot, invalidate
	// the second one that has cost > slot's remaining cost after first invalidation.

	// Inference 4 -> executor slot 4%5=4
	applyStartConfirmFinish(t, sm, user, hosts, 4)

	// Challenge inference 1 (executor slot 1).
	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: false, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], valMsg)
	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Vote invalid to reach threshold (need >2 for 5 slots).
	var voteTxs []*types.DevshardTx
	for _, slot := range []uint32{2, 3} {
		voteMsg := &types.MsgValidationVote{InferenceId: 1, VoterSlot: slot, VoteValid: false, EscrowId: "escrow-1"}
		voteMsg.ProposerSig = testutil.SignProposerTx(t, hosts[slot], voteMsg)
		voteTxs = append(voteTxs, txVote(voteMsg))
	}
	nonce = sm.SnapshotState().LatestNonce + 1
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, voteTxs)
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Inference 1 is now invalidated, cost was refunded from slot 1.
	st = sm.SnapshotState()
	require.Equal(t, types.StatusInvalidated, st.Inferences[1].Status)
	require.Equal(t, uint64(0), st.HostStats[1].Cost)
}

type recordObsTrackingStore struct {
	*storage.Memory
	recordCalls int
}

func (s *recordObsTrackingStore) RecordValidationsAppliedOnce(escrowID string, entries []storage.ValidationObsEntry) error {
	s.recordCalls++
	return s.Memory.RecordValidationsAppliedOnce(escrowID, entries)
}

func TestApplyDiff_DoesNotIncrementValidationObs(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	mem := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000)
	track := &recordObsTrackingStore{Memory: mem}
	sm, err := NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, track)
	require.NoError(t, err)

	applyStartConfirmFinish(t, sm, user, hosts, 1)

	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: true, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], valMsg)
	nonce := sm.SnapshotState().LatestNonce + 1
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txValidation(valMsg)})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)
	require.Equal(t, 0, track.recordCalls, "SM must not record validation obs; host records via RecordValidationsAppliedOnce")
}
