package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/types"
)

func newSealTestSM(t *testing.T, escrowID string, hosts []*signing.Secp256k1Signer, withStore bool) (*StateMachine, *storage.Memory, *signing.Secp256k1Signer, []types.SlotAssignment) {
	t.Helper()
	return newSealTestSMVersion(t, escrowID, hosts, withStore, types.DefaultStateRootVersion)
}

func newSealTestSMVersion(t *testing.T, escrowID string, hosts []*signing.Secp256k1Signer, withStore bool, sessionVersion string) (*StateMachine, *storage.Memory, *signing.Secp256k1Signer, []types.SlotAssignment) {
	t.Helper()

	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()

	var storeOpt SMOption
	var store *storage.Memory
	if withStore {
		store = storage.NewMemory()
		require.NoError(t, store.CreateSession(storage.CreateSessionParams{
			EscrowID:       escrowID,
			EpochID:        7,
			Version:        sessionVersion,
			CreatorAddr:    user.Address(),
			Config:         config,
			Group:          group,
			InitialBalance: 100000,
		}))
		storeOpt = WithInferenceStore(store)
	}

	opts := []SMOption{WithVersion(sessionVersion)}
	if storeOpt != nil {
		opts = append(opts, storeOpt)
	}
	sm, err := NewStateMachine(escrowID, config, group, 100000, user.Address(), verifier, opts...)
	require.NoError(t, err)
	return sm, store, user, group
}

func driveSealInferenceToFinished(t *testing.T, sm *StateMachine, escrowID string, hosts []*signing.Secp256k1Signer) {
	t.Helper()

	_, err := sm.ApplyLocal(1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama", InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	})})
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[1], escrowID, 1, []byte("prompt"), "llama", 100, 50, 1000, 2000)
	_, err = sm.ApplyLocal(2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 2000,
	})})
	require.NoError(t, err)

	finish := &types.MsgFinishInference{
		InferenceId:  1,
		ResponseHash: []byte("response"),
		InputTokens:  10,
		OutputTokens: 20,
		ExecutorSlot: 1,
		EscrowId:     escrowID,
	}
	finish.ProposerSig = testutil.SignProposerTx(t, hosts[1], finish)
	_, err = sm.ApplyLocal(3, []*types.DevshardTx{txFinish(finish)})
	require.NoError(t, err)
}

func TestSealInference_PreservesRootAndBlocksDuplicateID(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, store, _, _ := newSealTestSM(t, "escrow-seal", hosts, true)
	driveSealInferenceToFinished(t, sm, "escrow-seal", hosts)

	rootBefore, err := sm.ComputeStateRoot()
	require.NoError(t, err)

	require.NoError(t, sm.SealInference(1))
	state := sm.SnapshotState()
	_, exists := state.Inferences[1]
	require.False(t, exists)

	rootAfter, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, rootBefore, rootAfter)

	row, ok, err := store.GetSealedInference("escrow-seal", 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(1), row.InferenceID)
	require.Equal(t, sm.LatestNonce(), row.SealedNonce)

	rec, ok := sm.GetCommittedRecord(1)
	require.True(t, ok)
	require.Equal(t, types.StatusFinished, rec.Status,
		"the canonical post-seal record state lives in committedEntries, not in InferenceRow")

	err = sm.applyStartInference(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("other"), Model: "llama", InputLength: 100, MaxTokens: 50, StartedAt: 3000,
	})
	require.ErrorIs(t, err, types.ErrDuplicateInferenceID)
}

func TestSealedValidationAndVote_MatchLiveStateRoot(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sealedSM, _, _, _ := newSealTestSM(t, "escrow-sealed", hosts, true)
	liveSM, _, _, _ := newSealTestSM(t, "escrow-live", hosts, false)

	driveSealInferenceToFinished(t, sealedSM, "escrow-sealed", hosts)
	driveSealInferenceToFinished(t, liveSM, "escrow-live", hosts)
	require.NoError(t, sealedSM.SealInference(1))

	validationSealed := &types.MsgValidation{
		InferenceId:   1,
		ValidatorSlot: 2,
		Valid:         false,
		EscrowId:      "escrow-sealed",
	}
	validationSealed.ProposerSig = testutil.SignProposerTx(t, hosts[2], validationSealed)
	_, err := sealedSM.ApplyLocal(4, []*types.DevshardTx{txValidation(validationSealed)})
	require.NoError(t, err)

	validationLive := &types.MsgValidation{
		InferenceId:   1,
		ValidatorSlot: 2,
		Valid:         false,
		EscrowId:      "escrow-live",
	}
	validationLive.ProposerSig = testutil.SignProposerTx(t, hosts[2], validationLive)
	_, err = liveSM.ApplyLocal(4, []*types.DevshardTx{txValidation(validationLive)})
	require.NoError(t, err)

	voteSealed := &types.MsgValidationVote{
		InferenceId: 1,
		VoterSlot:   3,
		VoteValid:   false,
		EscrowId:    "escrow-sealed",
	}
	voteSealed.ProposerSig = testutil.SignProposerTx(t, hosts[3], voteSealed)
	_, err = sealedSM.ApplyLocal(5, []*types.DevshardTx{txVote(voteSealed)})
	require.NoError(t, err)

	voteLive := &types.MsgValidationVote{
		InferenceId: 1,
		VoterSlot:   3,
		VoteValid:   false,
		EscrowId:    "escrow-live",
	}
	voteLive.ProposerSig = testutil.SignProposerTx(t, hosts[3], voteLive)
	_, err = liveSM.ApplyLocal(5, []*types.DevshardTx{txVote(voteLive)})
	require.NoError(t, err)

	sealedRoot, err := sealedSM.ComputeStateRoot()
	require.NoError(t, err)
	liveRoot, err := liveSM.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, liveRoot, sealedRoot)

	sealedState := sealedSM.SnapshotState()
	_, exists := sealedState.Inferences[1]
	require.False(t, exists, "sealed inference must stay out of RAM on cold-path votes")
}

func TestSealInference_V2_FoldsAccumulatorAndChangesRoot(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, _, _, _ := newSealTestSMVersion(t, "escrow-v2-seal", hosts, true, types.DefaultStateRootVersion)
	sm.testV2Composition = true
	driveSealInferenceToFinished(t, sm, "escrow-v2-seal", hosts)

	rootBefore, err := sm.ComputeStateRoot()
	require.NoError(t, err)

	require.NoError(t, sm.SealInference(1))

	committed := sm.ExportCommittedEntries()
	_, has := committed[1]
	require.False(t, has, "v2 seal must drop committed entry for sealed id")

	st := sm.SnapshotState()
	require.Len(t, st.SealedAcc, 32, "v2 seal must persist 32-byte accumulator")

	rootAfter, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	require.NotEqual(t, rootBefore, rootAfter, "v2 root must change when folding a sealed inference")

	err = sm.applyStartInference(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("other"), Model: "llama", InputLength: 100, MaxTokens: 50, StartedAt: 3000,
	})
	require.ErrorIs(t, err, types.ErrDuplicateInferenceID)
}
