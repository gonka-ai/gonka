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
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens,
	})})
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[1], escrowID, 1, []byte("prompt"), "llama", 100, testutil.TestMaxTokens)
	_, err = sm.ApplyLocal(2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig,
	})})
	require.NoError(t, err)

	finish := &types.MsgFinishInference{
		InferenceId:  1,
		ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash,
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
		InferenceId: 1, PromptHash: []byte("other"), Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens,
	}, sm.LatestNonce()+1)
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
	require.Equal(t, live.StartedAtHeight, got.StartedAtHeight)
	require.Equal(t, live.ConfirmedAtHeight, got.ConfirmedAtHeight)
	require.Greater(t, got.ActualCost, uint64(0))
}

func TestNextAutoSealNonce(t *testing.T) {
	require.Equal(t, uint64(150), NextAutoSealNonce(0))
	require.Equal(t, uint64(150), NextAutoSealNonce(149))
	require.Equal(t, uint64(300), NextAutoSealNonce(150))
	require.Equal(t, uint64(300), NextAutoSealNonce(299))
}

func TestAutoSealStateClock_SkipsUnconfirmedInTailWindow(t *testing.T) {
	d := newFloorDriver(t)
	hash := []byte{0x01, 0x00}
	d.raise(100, hash)
	pending := d.next()
	require.NoError(t, d.start(pending, 100, hash))
	confirmed := d.next()
	require.NoError(t, d.start(confirmed, 100, hash))
	d.raise(5000, []byte{0x50, 0x00})
	require.NoError(t, d.apply(d.confirm(confirmed, 100, hash)))

	clock := d.sm.AutoSealStateClock()
	require.True(t, clock.Known)
	require.Equal(t, uint64(5000), clock.MinConfirmedHeight, "an unconfirmed record must not pull window min to zero")
	require.Equal(t, uint64(5000), clock.MaxConfirmedHeight)
	require.Equal(t, uint64(5000), clock.Clock)
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
		confirmedHeight  = uint64(1000)
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
	apply := func(nonce uint64, txs ...*types.DevshardTx) {
		t.Helper()
		_, err := sm.ApplyLocal(nonce, restamp(t, sm, hosts, escrowID, nonce, txs))
		require.NoError(t, err)
	}

	seedFloor(t, sm, confirmedHeight, []byte("block-1000"))
	apply(1, txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens,
	}))
	apply(2, txConfirm(&types.MsgConfirmStart{InferenceId: 1}))
	finish := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash, InputTokens: 10, OutputTokens: 20, ExecutorSlot: 1, EscrowId: escrowID,
	}
	finish.ProposerSig = testutil.SignProposerTx(t, hosts[1], finish)
	apply(3, txFinish(finish))
	require.Equal(t, types.StatusFinished, sm.SnapshotState().Inferences[1].Status)
	require.Equal(t, confirmedHeight, sm.SnapshotState().Inferences[1].ConfirmedAtHeight)

	startConfirmAt := func(id, startNonce, height uint64) {
		t.Helper()
		seedFloor(t, sm, height, []byte{byte(height), byte(height >> 8)})
		apply(startNonce, txStart(&types.MsgStartInference{
			InferenceId: id, PromptHash: []byte("prompt"), Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens,
		}))
		apply(startNonce+1, txConfirm(&types.MsgConfirmStart{InferenceId: id}))
	}

	startConfirmAt(4, 4, confirmedHeight+sm.timeoutBlocks(graceSeconds)+1)
	_, live := sm.SnapshotState().Inferences[1]
	require.True(t, live, "must not seal when only InferenceSealGraceSeconds has elapsed")

	required := sm.timeoutBlocks(FinishedClockRequiredSeconds(graceSeconds, executionTimeout))
	startConfirmAt(6, 6, confirmedHeight+required+1)
	_, live = sm.SnapshotState().Inferences[1]
	require.False(t, live, "must seal after grace + execution timeout")
	require.Contains(t, sm.ExportSealedNonces(), uint64(1))
}

func finishedInferenceDiffs(escrowID string) []types.DiffRecord {
	return []types.DiffRecord{
		{Diff: types.Diff{Nonce: 1, Txs: []*types.DevshardTx{txStart(&types.MsgStartInference{
			InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama", InputLength: 100, MaxTokens: 50,
		})}}},
		{Diff: types.Diff{Nonce: 2, Txs: []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
			InferenceId: 1,
		})}}},
		{Diff: types.Diff{Nonce: 3, Txs: []*types.DevshardTx{txFinish(&types.MsgFinishInference{
			InferenceId: 1, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash, InputTokens: 10, OutputTokens: 20,
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

// TestFoldInferenceRecords_LateValidationOnChallengedMatchesLive replays a
// second Phase-A MsgValidation that lands on Challenged. The fold must add
// weight, mark every slot of that address, and resolve the threshold the same
// way the live apply does, including after a sealed-index rebuild.
func TestFoldInferenceRecords_LateValidationOnChallengedMatchesLive(t *testing.T) {
	signers := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	// Inference 1's executor is slot 1 (signer[1]). signer[2] owns slots 2 and 3
	// (weight 2) and can still validate. Threshold for 4 slots is 2, so that
	// late validation crosses it. Start requires inference id == diff nonce.
	group := testutil.MakeMultiSlotGroup(signers, []int{1, 1, 2})
	config := testutil.DefaultConfig(len(group))
	require.Equal(t, uint32(2), config.VoteThreshold)
	verifier := signing.NewSecp256k1Verifier()
	const escrowID = "escrow-1"
	store := testutil.MustMemoryStore(t, escrowID, user.Address(), config, group, 100_000)
	sm, err := NewStateMachine(escrowID, config, group, 100_000, user.Address(), verifier, store)
	require.NoError(t, err)

	var journal []types.DiffRecord
	apply := func(txs []*types.DevshardTx) {
		t.Helper()
		nonce := sm.SnapshotState().LatestNonce + 1
		diff := testutil.SignDiff(t, user, escrowID, nonce, txs)
		_, err := sm.ApplyDiff(diff)
		require.NoError(t, err)
		journal = append(journal, types.DiffRecord{Diff: diff})
	}

	const inferenceID uint64 = 1
	executorSlot := group[inferenceID%uint64(len(group))]
	require.Equal(t, uint32(1), executorSlot.SlotID)
	require.Equal(t, signers[1].Address(), executorSlot.ValidatorAddress)

	apply([]*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: inferenceID, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens,
	})})
	execSig := testutil.SignExecutorReceipt(t, signers[1], escrowID, inferenceID, []byte("prompt"), "llama", 100, testutil.TestMaxTokens)
	apply([]*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: inferenceID, ExecutorSig: execSig,
	})})
	finishMsg := &types.MsgFinishInference{
		InferenceId: inferenceID, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash,
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: executorSlot.SlotID,
		EscrowId: escrowID,
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, signers[1], finishMsg)
	apply([]*types.DevshardTx{txFinish(finishMsg)})

	phaseA := &types.MsgValidation{InferenceId: inferenceID, ValidatorSlot: 0, Valid: false, EscrowId: escrowID}
	phaseA.ProposerSig = testutil.SignProposerTx(t, signers[0], phaseA)
	apply([]*types.DevshardTx{txValidation(phaseA)})
	require.Equal(t, types.StatusChallenged, sm.SnapshotState().Inferences[inferenceID].Status)

	late := &types.MsgValidation{InferenceId: inferenceID, ValidatorSlot: 2, Valid: false, EscrowId: escrowID}
	late.ProposerSig = testutil.SignProposerTx(t, signers[2], late)
	apply([]*types.DevshardTx{txValidation(late)})

	live := *sm.SnapshotState().Inferences[inferenceID]
	require.Equal(t, types.StatusInvalidated, live.Status)
	require.Equal(t, uint32(0), live.VotesValid)
	require.Equal(t, uint32(3), live.VotesInvalid)
	var wantBits types.Bitmap128
	wantBits.Set(0)
	wantBits.Set(2)
	wantBits.Set(3)
	require.Equal(t, wantBits, live.ValidatedBy)

	st := sm.SnapshotState()
	folded := sm.foldInferenceRecordsFromDiffs(st.Group, st.Config.TokenPrice, st.Config.VoteThreshold, journal)
	got := folded[inferenceID]
	require.NotNil(t, got)
	require.Equal(t, live.Status, got.Status)
	require.Equal(t, live.VotesValid, got.VotesValid)
	require.Equal(t, live.VotesInvalid, got.VotesInvalid)
	require.Equal(t, live.ValidatedBy, got.ValidatedBy)
	require.Equal(t, live.ExecutorSlot, got.ExecutorSlot)
	require.Equal(t, live.ActualCost, got.ActualCost)

	require.NoError(t, sm.SealInference(inferenceID))
	require.NoError(t, sm.RebuildSealedInferenceIndexFromDiffs(store, journal))
	rebuilt, ok := sm.LookupSealedInference(inferenceID)
	require.True(t, ok)
	require.Equal(t, live.Status, rebuilt.Status)
	require.Equal(t, live.VotesValid, rebuilt.VotesValid)
	require.Equal(t, live.VotesInvalid, rebuilt.VotesInvalid)
	require.Equal(t, live.ValidatedBy, rebuilt.ValidatedBy)
}
