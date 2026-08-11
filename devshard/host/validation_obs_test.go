package host

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

const (
	// obsTestInferenceSealGraceNonces is the nonce gate used by obs tests: an
	// inference id may be sealed only once nonce >= id + this.
	obsTestInferenceSealGraceNonces = 2
	// obsTestInferenceSealGraceSeconds is the clock gate: an inference may be sealed
	// only once stateClock - ConfirmedAt >= this many "seconds".
	obsTestInferenceSealGraceSeconds = 5
)

type obsTestRig struct {
	t        *testing.T
	hosts    []*signing.Secp256k1Signer
	user     *signing.Secp256k1Signer
	group    []types.SlotAssignment
	config   types.SessionConfig
	host     *Host
	stub     *stub.InferenceEngine
	store    *storage.Memory
	escrowID string
}

func newObsRig(t *testing.T, store storage.Storage, opts ...HostOption) *obsTestRig {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    uint32(len(hosts)) / 2,
		ValidationRate:   0,
		// Small, explicit seal gates for deterministic auto-seal in tests.
		InferenceSealGraceNonces:  obsTestInferenceSealGraceNonces,
		InferenceSealGraceSeconds: obsTestInferenceSealGraceSeconds,
	}
	verifier := signing.NewSecp256k1Verifier()

	memStore, ok := store.(*storage.Memory)
	if !ok {
		mem := storage.NewMemory()
		require.NoError(t, mem.CreateSession(storage.CreateSessionParams{
			EscrowID:       "escrow-1",
			EpochID:        1,
			Version:        testutil.RuntimeTestVersion,
			CreatorAddr:    user.Address(),
			Config:         config,
			Group:          group,
			InitialBalance: 1_000_000,
		}))
		memStore = mem
		store = mem
	}

	sm, err := state.NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(), verifier, store)
	require.NoError(t, err)

	stubEngine := stub.NewInferenceEngine()
	hostOpts := []HostOption{WithStorage(store), WithVerifier(verifier)}
	hostOpts = append(hostOpts, opts...)
	h, err := NewHost(sm, hosts[0], stubEngine, "escrow-1", group, nil, hostOpts...)
	require.NoError(t, err)

	return &obsTestRig{
		t:        t,
		hosts:    hosts,
		user:     user,
		group:    group,
		config:   config,
		host:     h,
		stub:     stubEngine,
		store:    memStore,
		escrowID: "escrow-1",
	}
}

func (r *obsTestRig) applyDiff(nonce uint64, txs []*types.DevshardTx) {
	r.t.Helper()
	d := testutil.SignDiff(r.t, r.user, r.escrowID, nonce, txs)
	_, err := r.host.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d}})
	require.NoError(r.t, err)
}

func (r *obsTestRig) advanceToNextAutoSealNonce(after uint64) uint64 {
	r.t.Helper()
	target := state.NextAutoSealNonce(after)
	for n := after + 1; n <= target; n++ {
		r.applyDiff(n, nil)
	}
	return target + 1
}

func (r *obsTestRig) applyDiffExpectError(nonce uint64, txs []*types.DevshardTx) error {
	r.t.Helper()
	d := testutil.SignDiff(r.t, r.user, r.escrowID, nonce, txs)
	_, err := r.host.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d}})
	return err
}

func (r *obsTestRig) inferenceMissing(inferenceID uint64) {
	r.t.Helper()
	st := r.host.SnapshotState()
	_, ok := st.Inferences[inferenceID]
	require.False(r.t, ok, "inference %d should be evicted from RAM", inferenceID)
}

func (r *obsTestRig) sealedRow(inferenceID uint64) storage.InferenceRow {
	r.t.Helper()
	row, ok, err := r.store.GetSealedInference(r.escrowID, inferenceID)
	require.NoError(r.t, err)
	require.True(r.t, ok, "sealed inference %d should exist in storage", inferenceID)
	return row
}

// awaitTerminalSeal waits for the terminal seal once the nonce gate clears and
// the state clock is non-zero. If the terminalizing diff already sealed the
// inference, this returns immediately; otherwise it advances empty diffs.
func (r *obsTestRig) awaitTerminalSeal(inferenceID uint64, nonce uint64) uint64 {
	r.t.Helper()
	if row, ok, err := r.store.GetSealedInference(r.escrowID, inferenceID); err == nil && ok && row.SealedNonce != 0 {
		return nonce
	}
	after := r.host.LatestNonce()
	for i := 0; i < 3; i++ {
		nonce = r.advanceToNextAutoSealNonce(after)
		if row, ok, err := r.store.GetSealedInference(r.escrowID, inferenceID); err == nil && ok && row.SealedNonce != 0 {
			return nonce
		}
		after = nonce - 1
	}
	r.t.Fatalf("expected inference %d to be sealed", inferenceID)
	return nonce
}

func (r *obsTestRig) driveStartConfirmFinish(inferenceID, startNonce uint64) uint64 {
	r.t.Helper()
	executorSlot := uint32(inferenceID % uint64(len(r.group)))
	executorSigner := r.hosts[executorSlot]
	confirmedAt := int64(2000) + int64(inferenceID)

	r.applyDiff(startNonce, []*types.DevshardTx{testutil.StartTx(inferenceID)})

	execSig := testutil.SignExecutorReceipt(r.t, executorSigner, r.escrowID, inferenceID,
		testutil.TestPromptHash[:], "llama", 100, 50, 1000, confirmedAt)
	confirmTx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: inferenceID, ExecutorSig: execSig, ConfirmedAt: confirmedAt,
	}}}
	r.applyDiff(startNonce+1, []*types.DevshardTx{confirmTx})

	finishMsg := &types.MsgFinishInference{
		InferenceId:  inferenceID,
		ResponseHash: r.stub.ResponseHash,
		InputTokens:  r.stub.InputTokens,
		OutputTokens: r.stub.OutputTokens,
		ExecutorSlot: executorSlot,
		EscrowId:     r.escrowID,
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(r.t, executorSigner, finishMsg)
	finishTx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: finishMsg}}
	r.applyDiff(startNonce+2, []*types.DevshardTx{finishTx})

	return startNonce + 3
}

func (r *obsTestRig) signValidation(inferenceID uint64, validatorSlot uint32, valid bool) *types.DevshardTx {
	r.t.Helper()
	signer := r.hosts[validatorSlot]
	msg := &types.MsgValidation{
		InferenceId:   inferenceID,
		ValidatorSlot: validatorSlot,
		Valid:         valid,
		EscrowId:      r.escrowID,
	}
	msg.ProposerSig = testutil.SignProposerTx(r.t, signer, msg)
	return &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: msg}}
}

func (r *obsTestRig) signValidationVote(inferenceID uint64, voterSlot uint32, valid bool) *types.DevshardTx {
	r.t.Helper()
	signer := r.hosts[voterSlot]
	msg := &types.MsgValidationVote{
		InferenceId: inferenceID,
		VoterSlot:   voterSlot,
		VoteValid:   valid,
		EscrowId:    r.escrowID,
	}
	msg.ProposerSig = testutil.SignProposerTx(r.t, signer, msg)
	return &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{ValidationVote: msg}}
}

func obsCompletedForSlot(t *testing.T, store storage.Storage, escrowID string, slotID uint32) uint32 {
	t.Helper()
	rows, err := store.GetValidationObservability(escrowID)
	require.NoError(t, err)
	for _, r := range rows {
		if r.SlotID == slotID {
			return r.CompletedValidations
		}
	}
	return 0
}

func waitObsCompletedForSlot(t *testing.T, store storage.Storage, escrowID string, slotID uint32, want uint32) {
	t.Helper()
	require.Eventually(t, func() bool {
		return obsCompletedForSlot(t, store, escrowID, slotID) == want
	}, time.Second, 10*time.Millisecond)
}

func waitObsRowCount(t *testing.T, store storage.Storage, escrowID string, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		rows, err := store.GetValidationObservability(escrowID)
		return err == nil && len(rows) == want
	}, time.Second, 10*time.Millisecond)
}

func snapshotValidationObs(t *testing.T, store storage.Storage, escrowID string) []storage.SlotValidationObs {
	t.Helper()
	rows, err := store.GetValidationObservability(escrowID)
	require.NoError(t, err)
	return rows
}

func waitValidationObsUnchanged(t *testing.T, store storage.Storage, escrowID string, want []storage.SlotValidationObs) {
	t.Helper()
	require.Eventually(t, func() bool {
		got := snapshotValidationObs(t, store, escrowID)
		return obsRowsEqual(got, want)
	}, time.Second, 10*time.Millisecond)
}

func obsRowsEqual(a, b []storage.SlotValidationObs) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type failingRecordObsStore struct {
	*storage.Memory
	failRecord bool
}

func (f *failingRecordObsStore) RecordValidationsAppliedOnce(escrowID string, entries []storage.ValidationObsEntry) error {
	if f.failRecord {
		return errors.New("injected record validation obs error")
	}
	return f.Memory.RecordValidationsAppliedOnce(escrowID, entries)
}

func TestHost_ApplyAndPersist_ValidationObs_RecordsOnDiff(t *testing.T) {
	r := newObsRig(t, nil)
	next := r.driveStartConfirmFinish(1, 1)
	valTx := r.signValidation(1, 0, true)
	r.applyDiff(next, []*types.DevshardTx{valTx})

	waitObsCompletedForSlot(t, r.store, r.escrowID, 0, 1)
	rows, err := r.store.GetValidationObservability(r.escrowID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint32(1), rows[0].RequiredValidations)
	require.Equal(t, uint32(1), rows[0].CompletedValidations)
}

func TestHost_ApplyAndPersist_ValidationObs_DuplicateTxInDiff(t *testing.T) {
	r := newObsRig(t, nil)
	next := r.driveStartConfirmFinish(1, 1)
	valTx := r.signValidation(1, 0, true)
	r.applyDiff(next, []*types.DevshardTx{valTx, valTx})

	waitObsCompletedForSlot(t, r.store, r.escrowID, 0, 1)
}

func TestHost_ApplyAndPersist_ValidationObs_NoDiffApplyNoRecord(t *testing.T) {
	r := newObsRig(t, nil)
	next := r.driveStartConfirmFinish(1, 1)
	valTx := r.signValidation(1, 0, true)
	r.host.mempool.Add(MempoolEntry{Tx: valTx, ProposedAt: next})

	require.Equal(t, uint32(0), obsCompletedForSlot(t, r.store, r.escrowID, 0))
}

func TestHost_ApplyAndPersist_ValidationObs_StoreErrorDoesNotFailApply(t *testing.T) {
	mem := storage.NewMemory()
	user := testutil.MustGenerateKey(t)
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    1,
		ValidationRate:   0,
	}
	require.NoError(t, mem.CreateSession(storage.CreateSessionParams{
		EscrowID: "escrow-1", EpochID: 1, Version: testutil.RuntimeTestVersion,
		CreatorAddr: user.Address(), Config: config, Group: group, InitialBalance: 1_000_000,
	}))
	failStore := &failingRecordObsStore{Memory: mem, failRecord: true}
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(), verifier, failStore)
	require.NoError(t, err)
	h, err := NewHost(sm, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil,
		WithStorage(failStore), WithVerifier(verifier))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
	require.NoError(t, err)

	recs, err := failStore.GetDiffs("escrow-1", 1, 1)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, uint32(0), obsCompletedForSlot(t, failStore, "escrow-1", 0))
}

func TestHost_ApplyAndPersist_ValidationVoteObs_RecordsOnDiff(t *testing.T) {
	r := newObsRig(t, nil)
	next := r.driveStartConfirmFinish(1, 1)
	r.applyDiff(next, []*types.DevshardTx{r.signValidation(1, 0, false)})
	next++
	voteTx := r.signValidationVote(1, 2, true)
	r.applyDiff(next, []*types.DevshardTx{voteTx})

	waitObsCompletedForSlot(t, r.store, r.escrowID, 2, 1)
}

func TestHost_ApplyAndPersist_ValidationVoteObs_DedupDuplicateVote(t *testing.T) {
	r := newObsRig(t, nil)
	next := r.driveStartConfirmFinish(1, 1)
	r.applyDiff(next, []*types.DevshardTx{r.signValidation(1, 0, false)})
	next++
	voteTx := r.signValidationVote(1, 2, true)
	r.applyDiff(next, []*types.DevshardTx{voteTx})

	// Exercise batch dedup directly (sync path).
	writeValidationObsBatch(r.store, r.escrowID, storage.ValidationObsEntriesFromTxs([]*types.DevshardTx{voteTx, voteTx}))

	require.Equal(t, uint32(1), obsCompletedForSlot(t, r.store, r.escrowID, 2))
}

func TestHost_ApplyRecoveredDiffs_ValidationObs_NoDoubleCount(t *testing.T) {
	r := newObsRig(t, nil)
	next := r.driveStartConfirmFinish(1, 1)
	valTx := r.signValidation(1, 0, true)
	r.applyDiff(next, []*types.DevshardTx{valTx})
	waitObsCompletedForSlot(t, r.store, r.escrowID, 0, 1)

	diff := testutil.SignDiff(r.t, r.user, r.escrowID, next, []*types.DevshardTx{valTx})
	_, err := r.host.ApplyRecoveredDiffs(context.Background(), []types.Diff{diff})
	require.NoError(t, err)
	waitObsCompletedForSlot(t, r.store, r.escrowID, 0, 1)
}

func TestHost_ApplyAndPersist_ValidationObs_ValidateAndSealSameDiff(t *testing.T) {
	r := newObsRig(t, nil)
	next := r.driveStartConfirmFinish(1, 1)
	r.applyDiff(next, []*types.DevshardTx{r.signValidation(1, 0, false)})
	next++
	waitObsCompletedForSlot(t, r.store, r.escrowID, 0, 1)

	// Terminal invalid vote; host records vote obs after SM terminal persist inside ApplyDiff.
	r.applyDiff(next, []*types.DevshardTx{r.signValidationVote(1, 1, false)})
	waitObsCompletedForSlot(t, r.store, r.escrowID, 0, 1)
	waitObsCompletedForSlot(t, r.store, r.escrowID, 1, 1)
}

func TestHost_ApplyAndPersist_ValidationObs_NoRecordOnUserLocalCompose(t *testing.T) {
	r := newObsRig(t, nil)
	next := r.driveStartConfirmFinish(1, 1)
	valTx := r.signValidation(1, 0, true)

	verifier := signing.NewSecp256k1Verifier()
	userSM, err := state.NewStateMachine(r.escrowID, r.config, r.group, 1_000_000, r.user.Address(), verifier, r.store)
	require.NoError(t, err)
	records, err := r.store.GetDiffs(r.escrowID, 1, next-1)
	require.NoError(t, err)
	for _, rec := range records {
		userSM.InjectWarmKeys(rec.WarmKeyDelta)
		_, err := userSM.ApplyLocal(rec.Nonce, rec.Txs)
		require.NoError(t, err)
	}
	_, _, err = userSM.ApplyLocalBestEffort(next, []*types.DevshardTx{valTx})
	require.NoError(t, err)
	require.Equal(t, uint32(0), obsCompletedForSlot(t, r.store, r.escrowID, 0))

	r.applyDiff(next, []*types.DevshardTx{valTx})
	waitObsCompletedForSlot(t, r.store, r.escrowID, 0, 1)
}

func applyDiffRecordsFromStore(t *testing.T, h *Host, store storage.Storage, escrowID string, from, to uint64) {
	t.Helper()
	records, err := store.GetDiffs(escrowID, from, to)
	require.NoError(t, err)
	for _, rec := range records {
		_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{rec.Diff}})
		require.NoError(t, err)
	}
}

func TestHost_CrossHost_ValidationObsConvergesOnDiffApply(t *testing.T) {
	rigA := newObsRig(t, nil)
	next := rigA.driveStartConfirmFinish(1, 1)
	valTx := rigA.signValidation(1, 0, true)
	rigA.applyDiff(next, []*types.DevshardTx{valTx})
	waitObsCompletedForSlot(t, rigA.store, rigA.escrowID, 0, 1)

	storeB := storage.NewMemory()
	require.NoError(t, storeB.CreateSession(storage.CreateSessionParams{
		EscrowID:       rigA.escrowID,
		EpochID:        1,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    rigA.user.Address(),
		Config:         rigA.config,
		Group:          rigA.group,
		InitialBalance: 1_000_000,
	}))
	verifier := signing.NewSecp256k1Verifier()
	smB, err := state.NewStateMachine(rigA.escrowID, rigA.config, rigA.group, 1_000_000, rigA.user.Address(), verifier, storeB)
	require.NoError(t, err)
	hB, err := NewHost(smB, rigA.hosts[1], rigA.stub, rigA.escrowID, rigA.group, nil,
		WithStorage(storeB),
		WithVerifier(verifier),
	)
	require.NoError(t, err)

	applyDiffRecordsFromStore(t, hB, rigA.store, rigA.escrowID, 1, next-1)
	require.Equal(t, uint32(0), obsCompletedForSlot(t, storeB, rigA.escrowID, 0))

	valDiff := testutil.SignDiff(t, rigA.user, rigA.escrowID, next, []*types.DevshardTx{valTx})
	_, err = hB.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{valDiff}})
	require.NoError(t, err)
	waitObsCompletedForSlot(t, storeB, rigA.escrowID, 0, 1)
}

func TestHost_ValidateAsync_DoesNotRecordObsBeforeDiff(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    1,
		ValidationRate:   10000,
	}
	verifier := signing.NewSecp256k1Verifier()
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID: "escrow-1", EpochID: 1, Version: testutil.RuntimeTestVersion,
		CreatorAddr: user.Address(), Config: config, Group: group, InitialBalance: 100_000,
	}))
	sm, err := state.NewStateMachine("escrow-1", config, group, 100_000, user.Address(), verifier, store)
	require.NoError(t, err)
	valEngine := &trackingValidationEngine{valid: true}
	engine := stub.NewInferenceEngine()
	h, err := NewHost(sm, hosts[0], engine, "escrow-1", group, nil,
		WithStorage(store), WithValidator(valEngine), WithVerifier(verifier))
	require.NoError(t, err)
	h.Start()
	t.Cleanup(h.Close)

	diff1 := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff1}})
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, testutil.TestPromptHash[:], "llama", 100, 50, 1000, 2000)
	confirmTx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 2000,
	}}}
	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: engine.ResponseHash, InputTokens: 80, OutputTokens: 40,
		ExecutorSlot: 1, EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[1], finishMsg)
	finishTx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: finishMsg}}
	diff2 := testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{confirmTx})
	diff3 := testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{finishTx})
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff2, diff3}})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, tx := range h.mempool.Txs() {
			if v := tx.GetValidation(); v != nil && v.InferenceId == 1 {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond)

	require.Equal(t, uint32(0), obsCompletedForSlot(t, store, "escrow-1", 0))
}

func TestHost_ValidateAsync_RecordsObsAfterDiffApplied(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    1,
		ValidationRate:   10000,
	}
	verifier := signing.NewSecp256k1Verifier()
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID: "escrow-1", EpochID: 1, Version: testutil.RuntimeTestVersion,
		CreatorAddr: user.Address(), Config: config, Group: group, InitialBalance: 100_000,
	}))
	sm, err := state.NewStateMachine("escrow-1", config, group, 100_000, user.Address(), verifier, store)
	require.NoError(t, err)
	valEngine := &trackingValidationEngine{valid: true}
	engine := stub.NewInferenceEngine()
	h, err := NewHost(sm, hosts[0], engine, "escrow-1", group, nil,
		WithStorage(store), WithValidator(valEngine), WithVerifier(verifier))
	require.NoError(t, err)
	h.Start()
	t.Cleanup(h.Close)

	diff1 := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff1}})
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, testutil.TestPromptHash[:], "llama", 100, 50, 1000, 2000)
	confirmTx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 2000,
	}}}
	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: engine.ResponseHash, InputTokens: 80, OutputTokens: 40,
		ExecutorSlot: 1, EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[1], finishMsg)
	finishTx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: finishMsg}}
	diff2 := testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{confirmTx})
	diff3 := testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{finishTx})
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff2, diff3}})
	require.NoError(t, err)

	var valTx *types.DevshardTx
	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, tx := range h.mempool.Txs() {
			if v := tx.GetValidation(); v != nil && v.InferenceId == 1 {
				valTx = tx
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond)
	require.NotNil(t, valTx)

	diff4 := testutil.SignDiff(t, user, "escrow-1", 4, []*types.DevshardTx{valTx})
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff4}})
	require.NoError(t, err)
	waitObsCompletedForSlot(t, store, "escrow-1", 0, 1)
}

func TestExtractValidationObsEntries(t *testing.T) {
	r := newObsRig(t, nil)
	valTx := r.signValidation(1, 0, true)
	voteTx := r.signValidationVote(1, 2, true)

	entries := storage.ValidationObsEntriesFromTxs([]*types.DevshardTx{valTx, voteTx})
	require.Len(t, entries, 2)
	require.Equal(t, uint64(1), entries[0].InferenceID)
	require.Equal(t, uint32(0), entries[0].SlotID)
	require.Equal(t, uint32(2), entries[1].SlotID)
}

func TestHost_ApplyAndPersist_ValidationObs_AsyncBatchMultipleTxs(t *testing.T) {
	r := newObsRig(t, nil)
	next := r.driveStartConfirmFinish(1, 1)
	r.applyDiff(next, []*types.DevshardTx{
		r.signValidation(1, 0, false),
		r.signValidationVote(1, 2, true),
	})
	waitObsCompletedForSlot(t, r.store, r.escrowID, 0, 1)
	waitObsCompletedForSlot(t, r.store, r.escrowID, 2, 1)
	waitObsRowCount(t, r.store, r.escrowID, 2)
}

func TestHost_ApplyAndPersist_NoObsRecordForSealedInference(t *testing.T) {
	r := newObsRig(t, nil)

	const inferenceID = uint64(1)
	next := r.driveStartConfirmFinish(inferenceID, 1)

	// Record obs for slot 0, then terminalize and seal inference 1.
	r.applyDiff(next, []*types.DevshardTx{r.signValidation(inferenceID, 0, false)})
	next++
	waitObsCompletedForSlot(t, r.store, r.escrowID, 0, 1)

	r.applyDiff(next, []*types.DevshardTx{r.signValidationVote(inferenceID, 1, false)})
	next++
	waitObsCompletedForSlot(t, r.store, r.escrowID, 1, 1)
	waitObsRowCount(t, r.store, r.escrowID, 2)

	next = r.awaitTerminalSeal(inferenceID, next)
	r.inferenceMissing(inferenceID)
	require.NotZero(t, r.sealedRow(inferenceID).SealedNonce)

	beforeObs := snapshotValidationObs(t, r.store, r.escrowID)
	require.Len(t, beforeObs, 2)

	// Late validation on a slot that never recorded obs must fail before async write.
	err := r.applyDiffExpectError(next, []*types.DevshardTx{r.signValidation(inferenceID, 2, true)})
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrInferenceSealed)

	waitValidationObsUnchanged(t, r.store, r.escrowID, beforeObs)
	require.Equal(t, uint32(0), obsCompletedForSlot(t, r.store, r.escrowID, 2))
}
