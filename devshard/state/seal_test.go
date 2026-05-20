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
	require.NotEqual(t, rootBefore, rootAfter, "v2 seal folds into SealedAcc and changes the root")

	row, ok, err := store.GetSealedInference("escrow-seal", 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(1), row.InferenceID)
	require.Equal(t, sm.LatestNonce(), row.SealedNonce)

	_, hasCommitted := sm.ExportCommittedEntries()[1]
	require.False(t, hasCommitted, "v2 seal drops committed entry for sealed id")

	err = sm.applyStartInference(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("other"), Model: "llama", InputLength: 100, MaxTokens: 50, StartedAt: 3000,
	})
	require.ErrorIs(t, err, types.ErrDuplicateInferenceID)
}

func TestSealInference_LateValidationRejectedAfterSeal(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, _, _, _ := newSealTestSM(t, "escrow-sealed", hosts, true)
	driveSealInferenceToFinished(t, sm, "escrow-sealed", hosts)
	require.NoError(t, sm.SealInference(1))

	validation := &types.MsgValidation{
		InferenceId:   1,
		ValidatorSlot: 2,
		Valid:         false,
		EscrowId:      "escrow-sealed",
	}
	validation.ProposerSig = testutil.SignProposerTx(t, hosts[2], validation)
	_, err := sm.ApplyLocal(4, []*types.DevshardTx{txValidation(validation)})
	require.ErrorIs(t, err, types.ErrInferenceSealed)

	_, exists := sm.SnapshotState().Inferences[1]
	require.False(t, exists, "sealed inference must stay out of RAM")
}

func TestSeal_BuildSettlement_RestHashMatchesAfterSeal(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, _, _, _ := newSealTestSM(t, "escrow-settle", hosts, false)
	driveSealInferenceToFinished(t, sm, "escrow-settle", hosts)
	require.NoError(t, sm.SealInference(1))

	st := sm.SnapshotState()
	payload, err := BuildSettlement("escrow-settle", st, nil, sm.LatestNonce())
	require.NoError(t, err)

	acc := sealedAccBytes32(st.SealedAcc)
	restFromState, err := ComputeRestHashV2(st.Balance, acc, st.Inferences, st.WarmKeys)
	require.NoError(t, err)
	require.Equal(t, restFromState, payload.RestHash)

	hostStatsHash, err := ComputeHostStatsHash(st.HostStats)
	require.NoError(t, err)
	rootFromPayload := ComputeStateRootFromRestHash(hostStatsHash, payload.RestHash, st.Fees, types.PhaseSettlement, st.Version)
	rootFromSM, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	hostStatsHashActive, err := ComputeHostStatsHash(st.HostStats)
	require.NoError(t, err)
	rootActivePhase := ComputeStateRootFromRestHash(hostStatsHashActive, restFromState, st.Fees, st.Phase, st.Version)
	require.Equal(t, rootActivePhase, rootFromSM, "intra-session root uses active phase")
	require.NotEqual(t, rootFromPayload, rootFromSM, "settlement phase byte differs from active")
}
