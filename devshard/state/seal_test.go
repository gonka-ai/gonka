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
	return newSealTestSMVersion(t, escrowID, hosts, withStore, types.DevshardStateRootAndProtocolVersion)
}

func newSealTestSMVersion(t *testing.T, escrowID string, hosts []*signing.Secp256k1Signer, withStore bool, sessionVersion string) (*StateMachine, *storage.Memory, *signing.Secp256k1Signer, []types.SlotAssignment) {
	t.Helper()

	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()

	store := testutil.MustMemoryStore(t, escrowID, user.Address(), config, group, 100000)
	opts := []SMOption{WithVersion(sessionVersion)}
	sm, err := NewStateMachine(escrowID, config, group, 100000, user.Address(), verifier, store, opts...)
	require.NoError(t, err)
	return sm, store, user, group
}

func driveSealInferenceToFinished(t *testing.T, sm *StateMachine, escrowID string, hosts []*signing.Secp256k1Signer) {
	t.Helper()

	_, err := sm.ApplyLocal(1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})})
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[1], escrowID, 1, []byte("prompt"), "llama", 100, testutil.TestMaxTokens, 1000, 2000)
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
		InferenceId: 1, PromptHash: []byte("other"), Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 3000,
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
	restFromState, err := ComputeRestHashV2(st.Balance, acc, st.Inferences, st.WarmKeys, types.HeightSyncEscrowCommitFromState(&st))
	require.NoError(t, err)
	require.Equal(t, restFromState, payload.RestHash)

	hostStatsHash, err := ComputeHostStatsHash(st.HostStats)
	require.NoError(t, err)
	rootFromPayload := ComputeStateRootFromRestHash(hostStatsHash, payload.RestHash, st.Fees, types.PhaseSettlement, st.StateRootAndProtocolVersion)
	rootFromSM, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	hostStatsHashActive, err := ComputeHostStatsHash(st.HostStats)
	require.NoError(t, err)
	rootActivePhase := ComputeStateRootFromRestHash(hostStatsHashActive, restFromState, st.Fees, st.Phase, st.StateRootAndProtocolVersion)
	require.Equal(t, rootActivePhase, rootFromSM, "intra-session root uses active phase")
	require.NotEqual(t, rootFromPayload, rootFromSM, "settlement phase byte differs from active")
}

func TestChallengedInferencePersistedBeforeSeal(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	escrowID := "escrow-challenged"
	sm, store, _, _ := newSealTestSM(t, escrowID, hosts, true)
	driveSealInferenceToFinished(t, sm, escrowID, hosts)

	valMsg := &types.MsgValidation{
		InferenceId: 1, ValidatorSlot: 2, Valid: false, EscrowId: escrowID,
	}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[2], valMsg)
	_, err := sm.ApplyLocal(4, []*types.DevshardTx{txValidation(valMsg)})
	require.NoError(t, err)

	row, ok, err := store.GetSealedInference(escrowID, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, row.ObsPresent)
	require.Equal(t, uint32(types.StatusChallenged), row.SealedStatus)
	require.Equal(t, uint64(0), row.SealedNonce, "not sealed yet")

	require.NoError(t, sm.SealInference(1))
	row, ok, err = store.GetSealedInference(escrowID, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Greater(t, row.SealedNonce, uint64(0))
}

func TestSealInference_LookupReturnsStatisticsSnapshot(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	escrowID := "escrow-stats"
	sm, _, _, _ := newSealTestSM(t, escrowID, hosts, true)
	driveSealInferenceToFinished(t, sm, escrowID, hosts)

	live, ok := sm.GetInference(1)
	require.True(t, ok)

	require.NoError(t, sm.SealInference(1))
	_, ok = sm.GetInference(1)
	require.False(t, ok)

	got, ok := sm.LookupSealedInference(1)
	require.True(t, ok)
	require.Equal(t, live.Status, got.Status)
	require.Equal(t, live.ExecutorSlot, got.ExecutorSlot)
	require.Equal(t, live.Model, got.Model)
	require.Equal(t, live.PromptHash, got.PromptHash)
	require.Equal(t, live.ResponseHash, got.ResponseHash)
	require.Equal(t, live.InputLength, got.InputLength)
	require.Equal(t, live.MaxTokens, got.MaxTokens)
	require.Equal(t, live.InputTokens, got.InputTokens)
	require.Equal(t, live.OutputTokens, got.OutputTokens)
	require.Equal(t, live.ReservedCost, got.ReservedCost)
	require.Equal(t, live.ActualCost, got.ActualCost)
	require.Equal(t, live.StartedAt, got.StartedAt)
	require.Equal(t, live.ConfirmedAt, got.ConfirmedAt)
	require.Greater(t, got.ActualCost, uint64(0))
}

func TestNextAutoSealNonce(t *testing.T) {
	require.Equal(t, uint64(150), NextAutoSealNonce(0))
	require.Equal(t, uint64(150), NextAutoSealNonce(149))
	require.Equal(t, uint64(300), NextAutoSealNonce(150))
	require.Equal(t, uint64(300), NextAutoSealNonce(299))
}

func TestAutoSealStateClock_SkipsUnconfirmedInTailWindow(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, _, _, _ := newSealTestSM(t, "escrow-clock", hosts, false)

	_, err := sm.ApplyLocal(1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("pending"), Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})})
	require.NoError(t, err)

	_, err = sm.ApplyLocal(2, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 2, PromptHash: []byte("confirmed"), Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})})
	require.NoError(t, err)
	execSig := testutil.SignExecutorReceipt(t, hosts[2], "escrow-clock", 2, []byte("confirmed"), "llama", 100, testutil.TestMaxTokens, 1000, 5000)
	_, err = sm.ApplyLocal(3, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 2, ExecutorSig: execSig, ConfirmedAt: 5000,
	})})
	require.NoError(t, err)

	clock := sm.AutoSealStateClock()
	require.True(t, clock.Known)
	require.Equal(t, int64(5000), clock.MinConfirmedAt, "Pending ConfirmedAt=0 must not pull window min to zero")
	require.Equal(t, int64(5000), clock.MaxConfirmedAt)
	require.Equal(t, int64(5000), clock.Clock)
}

func TestExportAllInferenceRecords_IncludesSealedFromDB(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, store, _, _ := newSealTestSM(t, "escrow-export", hosts, true)
	driveSealInferenceToFinished(t, sm, "escrow-export", hosts)

	require.NoError(t, sm.SealInference(1))
	_, live := sm.SnapshotState().Inferences[1]
	require.False(t, live)

	records := sm.ExportAllInferenceRecords()
	require.Len(t, records, 1)
	rec, ok := records[1]
	require.True(t, ok)
	require.Equal(t, types.StatusFinished, rec.Status)
	require.Equal(t, "llama", rec.Model)
	require.Equal(t, uint64(10), rec.InputTokens)
	require.Equal(t, uint64(20), rec.OutputTokens)

	row, ok, err := store.GetSealedInference("escrow-export", 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, row.ObsPresent)
	require.Equal(t, uint32(types.StatusFinished), row.SealedStatus)
}

func TestFinishedClockRequiredSeconds(t *testing.T) {
	require.Equal(t, int64(3600), FinishedClockRequiredSeconds(3600, 0))
	require.Equal(t, int64(3700), FinishedClockRequiredSeconds(3600, 100))
	require.Equal(t, int64(5), FinishedClockRequiredSeconds(5, -10))
	require.Equal(t, int64(100), FinishedClockRequiredSeconds(-1, 100))
}

func TestAutoSeal_FinishedClockGateIncludesExecutionTimeout(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	const (
		graceSeconds     = 5
		executionTimeout = 100
		confirmedAt      = int64(1000)
	)
	config := types.NormalizeSessionConfig(types.SessionConfig{
		RefusalTimeout:            60,
		ExecutionTimeout:          executionTimeout,
		TokenPrice:                1,
		VoteThreshold:             1,
		ValidationRate:            0,
		InferenceSealGraceNonces:  2,
		InferenceSealGraceSeconds: graceSeconds,
		AutoSealEveryNNonces:      1,
	}, len(group))
	verifier := signing.NewSecp256k1Verifier()
	escrowID := "escrow-clock-timeout"
	sm, err := NewStateMachine(escrowID, config, group, 1_000_000, user.Address(), verifier,
		testutil.MustMemoryStore(t, escrowID, user.Address(), config, group, 1_000_000))
	require.NoError(t, err)

	_, err = sm.ApplyLocal(1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})})
	require.NoError(t, err)
	execSig := testutil.SignExecutorReceipt(t, hosts[1], escrowID, 1, []byte("prompt"), "llama", 100, testutil.TestMaxTokens, 1000, confirmedAt)
	_, err = sm.ApplyLocal(2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: confirmedAt,
	})})
	require.NoError(t, err)
	finish := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("response"), InputTokens: 10, OutputTokens: 20, ExecutorSlot: 1, EscrowId: escrowID,
	}
	finish.ProposerSig = testutil.SignProposerTx(t, hosts[1], finish)
	_, err = sm.ApplyLocal(3, []*types.DevshardTx{txFinish(finish)})
	require.NoError(t, err)
	require.Equal(t, types.StatusFinished, sm.SnapshotState().Inferences[1].Status)

	oldGraceClock := confirmedAt + graceSeconds + 1
	startConfirmAt(t, sm, hosts, escrowID, 4, 4, oldGraceClock)
	_, live := sm.SnapshotState().Inferences[1]
	require.True(t, live, "must not seal when only InferenceSealGraceSeconds has elapsed")

	required := FinishedClockRequiredSeconds(graceSeconds, executionTimeout)
	startConfirmAt(t, sm, hosts, escrowID, 6, 6, confirmedAt+required+1)
	_, live = sm.SnapshotState().Inferences[1]
	require.False(t, live, "must seal after grace + execution timeout")
	require.Contains(t, sm.ExportSealedNonces(), uint64(1))
}

func startConfirmAt(t *testing.T, sm *StateMachine, hosts []*signing.Secp256k1Signer, escrowID string, id, startNonce uint64, confirmedAt int64) {
	t.Helper()
	slot := uint32(id % uint64(len(hosts)))
	_, err := sm.ApplyLocal(startNonce, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: id, PromptHash: []byte("prompt"), Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})})
	require.NoError(t, err)
	execSig := testutil.SignExecutorReceipt(t, hosts[slot], escrowID, id, []byte("prompt"), "llama", 100, testutil.TestMaxTokens, 1000, confirmedAt)
	_, err = sm.ApplyLocal(startNonce+1, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: id, ExecutorSig: execSig, ConfirmedAt: confirmedAt,
	})})
	require.NoError(t, err)
}

func finishedInferenceDiffs(escrowID string) []types.DiffRecord {
	return []types.DiffRecord{
		{Diff: types.Diff{Nonce: 1, Txs: []*types.DevshardTx{txStart(&types.MsgStartInference{
			InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama", InputLength: 100, MaxTokens: 50, StartedAt: 1000,
		})}}},
		{Diff: types.Diff{Nonce: 2, Txs: []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
			InferenceId: 1, ConfirmedAt: 2000,
		})}}},
		{Diff: types.Diff{Nonce: 3, Txs: []*types.DevshardTx{txFinish(&types.MsgFinishInference{
			InferenceId: 1, ResponseHash: []byte("response"), InputTokens: 10, OutputTokens: 20,
			ExecutorSlot: 1, EscrowId: escrowID,
		})}}},
	}
}

func TestFillSealedInferenceIndexGaps_PreservesRichRow(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, store, _, _ := newSealTestSM(t, "escrow-gap-rich", hosts, true)
	driveSealInferenceToFinished(t, sm, "escrow-gap-rich", hosts)
	require.NoError(t, sm.SealInference(1))

	before, ok, err := store.GetSealedInference("escrow-gap-rich", 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, before.ObsPresent)

	inserted, err := sm.FillSealedInferenceIndexGaps()
	require.NoError(t, err)
	require.Zero(t, inserted)

	after, ok, err := store.GetSealedInference("escrow-gap-rich", 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, before, after)
}

func TestFillSealedInferenceIndexGaps_InsertsBareForMissing(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, store, _, _ := newSealTestSM(t, "escrow-gap-bare", hosts, true)
	driveSealInferenceToFinished(t, sm, "escrow-gap-bare", hosts)
	require.NoError(t, sm.SealInference(1))

	before, ok, err := store.GetSealedInference("escrow-gap-bare", 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, store.DeleteSealedInferences("escrow-gap-bare"))

	inserted, err := sm.FillSealedInferenceIndexGaps()
	require.NoError(t, err)
	require.Equal(t, 1, inserted)

	row, ok, err := store.GetSealedInference("escrow-gap-bare", 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, row.ObsPresent)
	require.Equal(t, before.SealedNonce, row.SealedNonce)
}

func TestFillSealedInferenceIndexGaps_SkipsLive(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, store, _, _ := newSealTestSM(t, "escrow-gap-live", hosts, true)
	driveSealInferenceToFinished(t, sm, "escrow-gap-live", hosts)

	inserted, err := sm.FillSealedInferenceIndexGaps()
	require.NoError(t, err)
	require.Zero(t, inserted)
	_, ok, err := store.GetSealedInference("escrow-gap-live", 1)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestFillSealedInferenceIndexGaps_Idempotent(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, store, _, _ := newSealTestSM(t, "escrow-gap-idem", hosts, true)
	driveSealInferenceToFinished(t, sm, "escrow-gap-idem", hosts)
	require.NoError(t, sm.SealInference(1))
	require.NoError(t, store.DeleteSealedInferences("escrow-gap-idem"))

	inserted, err := sm.FillSealedInferenceIndexGaps()
	require.NoError(t, err)
	require.Equal(t, 1, inserted)
	inserted, err = sm.FillSealedInferenceIndexGaps()
	require.NoError(t, err)
	require.Zero(t, inserted)
}

func TestRebuildSealedInferenceIndexFromDiffs_RestoresRichAndDropsStale(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	escrowID := "escrow-from-diffs"
	sm, store, _, _ := newSealTestSM(t, escrowID, hosts, true)
	driveSealInferenceToFinished(t, sm, escrowID, hosts)
	live, ok := sm.GetInference(1)
	require.True(t, ok)
	require.NoError(t, sm.SealInference(1))

	require.NoError(t, store.InsertSealedInference(escrowID, storage.InferenceRow{
		InferenceID: 99, SealedNonce: 1, ObsPresent: true, SealedModel: "stale",
	}))

	require.NoError(t, sm.RebuildSealedInferenceIndexFromDiffs(store, finishedInferenceDiffs(escrowID)))

	row, ok, err := store.GetSealedInference(escrowID, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, row.ObsPresent)
	require.Equal(t, live.Model, row.SealedModel)
	require.Equal(t, live.ActualCost, row.SealedActualCost)
	require.Equal(t, live.InputTokens, row.SealedInputTokens)
	require.Equal(t, live.OutputTokens, row.SealedOutputTokens)
	require.Equal(t, uint32(live.Status), row.SealedStatus)

	_, ok, err = store.GetSealedInference(escrowID, 99)
	require.NoError(t, err)
	require.False(t, ok, "ids absent from replayed history must be dropped")
}
