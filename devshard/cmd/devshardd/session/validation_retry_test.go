package session

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"common/chain"
	devshardpkg "devshard"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

// --- stubs ---

type stubStaleLeaseStore struct {
	acquireFn      func(ctx context.Context, escrowId, instanceAddr string, ttl time.Duration) (uint64, uint64, error)
	setResultFn    func(ctx context.Context, escrowId string, inferenceId, epochId uint64, status storage.LeaseStatus, instanceAddr string) error
	ownsFn         func(ctx context.Context, escrowId string, inferenceId, epochId uint64, instanceAddr string) (bool, error)
	releaseFn      func(ctx context.Context, escrowId string, inferenceId, epochId uint64, instanceAddr string) error
	setResultCalls []string
	releaseCalls   []string
}

func (s *stubStaleLeaseStore) AcquireOneStale(ctx context.Context, escrowId, instanceAddr string, ttl time.Duration) (uint64, uint64, error) {
	return s.acquireFn(ctx, escrowId, instanceAddr, ttl)
}

func (s *stubStaleLeaseStore) SetResult(ctx context.Context, escrowId string, inferenceId, epochId uint64, status storage.LeaseStatus, instanceAddr string) error {
	s.setResultCalls = append(s.setResultCalls, fmt.Sprintf("%s/%d/%d/%s", escrowId, inferenceId, epochId, status))
	if s.setResultFn != nil {
		return s.setResultFn(ctx, escrowId, inferenceId, epochId, status, instanceAddr)
	}
	return nil
}

func (s *stubStaleLeaseStore) OwnsPendingLease(ctx context.Context, escrowId string, inferenceId, epochId uint64, instanceAddr string) (bool, error) {
	if s.ownsFn != nil {
		return s.ownsFn(ctx, escrowId, inferenceId, epochId, instanceAddr)
	}
	return true, nil
}

func (s *stubStaleLeaseStore) Release(ctx context.Context, escrowId string, inferenceId, epochId uint64, instanceAddr string) error {
	s.releaseCalls = append(s.releaseCalls, fmt.Sprintf("%s/%d/%d/%s", escrowId, inferenceId, epochId, instanceAddr))
	if s.releaseFn != nil {
		return s.releaseFn(ctx, escrowId, inferenceId, epochId, instanceAddr)
	}
	return nil
}

type stubSessionManager struct {
	ids       []string
	snap      hostSnap
	snapFn    func() (hostSnap, bool)
	snapCalls int
}

func (s *stubSessionManager) ActiveEscrowIDs() []string { return s.ids }
func (s *stubSessionManager) hostSnapshot(_ string) (hostSnap, bool) {
	s.snapCalls++
	if s.snapFn != nil {
		return s.snapFn()
	}
	if s.snap == nil {
		return nil, false
	}
	return s.snap, true
}

type stubHostSnap struct {
	state types.EscrowState
	group []types.SlotAssignment
}

func (s *stubHostSnap) SnapshotState() types.EscrowState { return s.state }
func (s *stubHostSnap) Group() []types.SlotAssignment    { return s.group }

type stubEngine struct {
	validateFn func(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error)
	calls      int
}

func (s *stubEngine) Validate(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	s.calls++
	if s.validateFn != nil {
		return s.validateFn(ctx, req)
	}
	return &devshardpkg.ValidateResult{Valid: true}, nil
}

func inferenceSnap(id uint64, status types.InferenceStatus) *stubHostSnap {
	return &stubHostSnap{
		state: types.EscrowState{
			Inferences: map[uint64]*types.InferenceRecord{
				id: {
					Status:       status,
					ExecutorSlot: 2,
					Model:        "llama-3",
					PromptHash:   []byte("ph"),
					ResponseHash: []byte("rh"),
					InputTokens:  100,
					OutputTokens: 50,
				},
			},
		},
		group: []types.SlotAssignment{
			{SlotID: 2, ValidatorAddress: "executor-addr"},
		},
	}
}

func newTestValidationRetryLoop(leases *stubStaleLeaseStore, snap hostSnap, inner *stubEngine) *ValidationRetryLoop {
	return &ValidationRetryLoop{
		leases:       leases,
		inner:        inner,
		manager:      &stubSessionManager{snap: snap},
		instanceAddr: "addr",
		leaseTTL:     DefaultValidationLeaseTTL,
		interval:     DefaultValidationRetryInterval,
	}
}

// --- lookupValidateTarget tests ---

func TestLookupValidateTarget_InferenceNotFound(t *testing.T) {
	h := &stubHostSnap{
		state: types.EscrowState{Inferences: map[uint64]*types.InferenceRecord{}},
	}
	_, status, found := lookupValidateTarget(h, "escrow-1", 99, 0)
	assert.False(t, found)
	assert.Equal(t, types.InferenceStatus(0), status)
}

func TestLookupValidateTarget_ReportsStatus(t *testing.T) {
	h := inferenceSnap(10, types.StatusPending)
	req, status, found := lookupValidateTarget(h, "escrow-1", 10, 5)
	require.True(t, found)
	assert.Equal(t, types.StatusPending, status)
	assert.Equal(t, uint64(10), req.InferenceID)
	assert.Equal(t, uint64(5), req.EpochID)
}

func TestLookupValidateTarget_HappyPath(t *testing.T) {
	h := inferenceSnap(7, types.StatusFinished)
	req, status, found := lookupValidateTarget(h, "escrow-1", 7, 3)
	require.True(t, found)
	assert.Equal(t, types.StatusFinished, status)
	assert.Equal(t, uint64(7), req.InferenceID)
	assert.Equal(t, "escrow-1", req.EscrowID)
	assert.Equal(t, "llama-3", req.Model)
	assert.Equal(t, []byte("ph"), req.PromptHash)
	assert.Equal(t, []byte("rh"), req.ResponseHash)
	assert.Equal(t, uint64(100), req.InputTokens)
	assert.Equal(t, uint64(50), req.OutputTokens)
	assert.Equal(t, "executor-addr", req.ExecutorAddress)
}

// --- retryStaleValidationsForEscrow tests ---

func TestRetryStaleValidationsForEscrow_NoStaleLeases(t *testing.T) {
	calls := 0
	leases := &stubStaleLeaseStore{
		acquireFn: func(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
			calls++
			return 0, 0, nil // no stale leases
		},
	}
	rl := &ValidationRetryLoop{
		leases:       leases,
		manager:      &stubSessionManager{snap: inferenceSnap(1, types.StatusFinished)},
		instanceAddr: "addr",
		leaseTTL:     DefaultValidationLeaseTTL,
		interval:     DefaultValidationRetryInterval,
	}
	rl.retryStaleValidationsForEscrow(context.Background(), "escrow-1")
	assert.Equal(t, 1, calls, "should call AcquireOneStale once and stop")
}

func TestRetryStaleValidationsForEscrow_AcquireError_Stops(t *testing.T) {
	calls := 0
	leases := &stubStaleLeaseStore{
		acquireFn: func(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
			calls++
			return 0, 0, errors.New("db error")
		},
	}
	rl := &ValidationRetryLoop{
		leases:       leases,
		manager:      &stubSessionManager{snap: inferenceSnap(1, types.StatusFinished)},
		instanceAddr: "addr",
		leaseTTL:     DefaultValidationLeaseTTL,
	}
	rl.retryStaleValidationsForEscrow(context.Background(), "escrow-1")
	assert.Equal(t, 1, calls, "should stop after first error")
}

func TestRetryStaleValidationsForEscrow_LeaseFromPreviousEpochIsSkipped(t *testing.T) {
	callCount := 0
	leases := &stubStaleLeaseStore{
		acquireFn: func(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
			callCount++
			if callCount == 1 {
				return 1, 10, nil
			}
			return 0, 0, nil
		},
	}
	phase := new(chain.Phase)
	phase.Update(11, 0)
	rl := &ValidationRetryLoop{
		leases:       leases,
		manager:      &stubSessionManager{snap: inferenceSnap(1, types.StatusFinished)},
		phase:        phase,
		instanceAddr: "addr",
		leaseTTL:     DefaultValidationLeaseTTL,
	}

	rl.retryStaleValidationsForEscrow(context.Background(), "escrow-1")

	assert.Equal(t, 2, callCount)
	require.Len(t, leases.setResultCalls, 1)
	assert.Equal(t, "escrow-1/1/10/skipped", leases.setResultCalls[0])
}

func TestRetryStaleValidationsForEscrow_SessionNotLoaded_DoesNotClaim(t *testing.T) {
	calls := 0
	leases := &stubStaleLeaseStore{
		acquireFn: func(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
			calls++
			return 1, 3, nil
		},
	}
	rl := &ValidationRetryLoop{
		leases:       leases,
		manager:      &stubSessionManager{},
		instanceAddr: "addr",
		leaseTTL:     DefaultValidationLeaseTTL,
	}
	rl.retryStaleValidationsForEscrow(context.Background(), "escrow-1")
	assert.Equal(t, 0, calls, "must not AcquireOneStale when the session is not loaded")
	require.Empty(t, leases.releaseCalls)
	require.Empty(t, leases.setResultCalls)
}

func TestRetryStaleValidation_SessionNotLoaded_Releases(t *testing.T) {
	leases := &stubStaleLeaseStore{}
	rl := newTestValidationRetryLoop(leases, nil, &stubEngine{})

	err := rl.retryStaleValidation(context.Background(), "escrow-1", 7, 3)
	require.ErrorContains(t, err, "not loaded")
	require.Equal(t, []string{"escrow-1/7/3/addr"}, leases.releaseCalls)
	require.Empty(t, leases.setResultCalls, "must not mark skipped; that row would be permanently unacquirable")
}

func TestRetryStaleValidationsForEscrow_SessionUnloadsAfterClaim_Releases(t *testing.T) {
	callCount := 0
	leases := &stubStaleLeaseStore{
		acquireFn: func(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
			callCount++
			if callCount == 1 {
				return 7, 3, nil
			}
			return 0, 0, nil
		},
	}
	mgr := &stubSessionManager{}
	mgr.snapFn = func() (hostSnap, bool) {
		// First hostSnapshot is the pre-claim check; after AcquireOneStale the
		// session is gone (settlement / epoch eviction).
		if mgr.snapCalls == 1 {
			return inferenceSnap(7, types.StatusFinished), true
		}
		return nil, false
	}
	rl := &ValidationRetryLoop{
		leases:       leases,
		inner:        &stubEngine{},
		manager:      mgr,
		instanceAddr: "addr",
		leaseTTL:     DefaultValidationLeaseTTL,
	}
	rl.retryStaleValidationsForEscrow(context.Background(), "escrow-1")

	assert.Equal(t, 1, callCount, "loop must stop after the session unloads")
	require.Equal(t, []string{"escrow-1/7/3/addr"}, leases.releaseCalls)
	require.Empty(t, leases.setResultCalls)
}

func TestRetryStaleValidationsForEscrow_ChallengedReleasesAndContinues(t *testing.T) {
	callCount := 0
	leases := &stubStaleLeaseStore{
		acquireFn: func(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
			callCount++
			if callCount == 1 {
				return 7, 3, nil
			}
			return 0, 0, nil
		},
	}
	inner := &stubEngine{}
	rl := newTestValidationRetryLoop(leases, inferenceSnap(7, types.StatusChallenged), inner)
	rl.retryStaleValidationsForEscrow(context.Background(), "escrow-1")

	assert.Equal(t, 2, callCount)
	assert.Equal(t, 0, inner.calls, "retry loop must not validate challenged inferences")
	require.Empty(t, leases.setResultCalls, "challenged must not be marked skipped")
	require.Equal(t, []string{"escrow-1/7/3/addr"}, leases.releaseCalls)
}

func TestRetryStaleValidation_Challenged_ReleasesWithoutSubmit(t *testing.T) {
	leases := &stubStaleLeaseStore{}
	inner := &stubEngine{}
	rl := newTestValidationRetryLoop(leases, inferenceSnap(7, types.StatusChallenged), inner)

	err := rl.retryStaleValidation(context.Background(), "escrow-1", 7, 3)
	require.NoError(t, err)
	assert.Equal(t, 0, inner.calls)
	require.Empty(t, leases.setResultCalls)
	require.Equal(t, []string{"escrow-1/7/3/addr"}, leases.releaseCalls)
}

func TestRetryStaleValidation_TerminalOrAbsent_Skipped(t *testing.T) {
	tests := []struct {
		name string
		snap hostSnap
	}{
		{name: "absent", snap: &stubHostSnap{state: types.EscrowState{Inferences: map[uint64]*types.InferenceRecord{}}}},
		{name: "pending", snap: inferenceSnap(7, types.StatusPending)},
		{name: "validated", snap: inferenceSnap(7, types.StatusValidated)},
		{name: "invalidated", snap: inferenceSnap(7, types.StatusInvalidated)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leases := &stubStaleLeaseStore{}
			inner := &stubEngine{}
			rl := newTestValidationRetryLoop(leases, tt.snap, inner)
			err := rl.retryStaleValidation(context.Background(), "escrow-1", 7, 3)
			require.NoError(t, err)
			assert.Equal(t, 0, inner.calls)
			require.Empty(t, leases.releaseCalls)
			require.Equal(t, []string{"escrow-1/7/3/skipped"}, leases.setResultCalls)
		})
	}
}

func TestRetryStaleValidation_ValidateError_Releases(t *testing.T) {
	leases := &stubStaleLeaseStore{}
	inner := &stubEngine{
		validateFn: func(_ context.Context, _ devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
			return nil, errors.New("local ml 503")
		},
	}
	rl := newTestValidationRetryLoop(leases, inferenceSnap(7, types.StatusFinished), inner)

	err := rl.retryStaleValidation(context.Background(), "escrow-1", 7, 3)
	require.Error(t, err)
	assert.Equal(t, 1, inner.calls)
	require.Empty(t, leases.setResultCalls)
	require.Equal(t, []string{"escrow-1/7/3/addr"}, leases.releaseCalls)
}

func TestRetryStaleValidationsForEscrow_TransientErrorReleaseThenHotPathReacquireSubmits(t *testing.T) {
	h := newFinishedRetryHost(t)
	leases := storage.NewMemory()
	ctx := context.Background()
	require.NoError(t, acquireMemoryLease(ctx, leases, "escrow-1", 1, 3, "old-owner"))

	validateCalls := 0
	inner := &stubEngine{
		validateFn: func(_ context.Context, _ devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
			validateCalls++
			if validateCalls == 1 {
				return nil, errors.New("local ml 503")
			}
			return &devshardpkg.ValidateResult{Valid: true}, nil
		},
	}
	rl := &ValidationRetryLoop{
		leases:       leases,
		inner:        inner,
		manager:      &stubSessionManager{snap: h},
		instanceAddr: "addr",
		leaseTTL:     50 * time.Millisecond,
	}

	time.Sleep(60 * time.Millisecond)
	rl.retryStaleValidationsForEscrow(ctx, "escrow-1")

	assert.Equal(t, 1, inner.calls)
	require.False(t, ownsMemoryLease(ctx, t, leases, "escrow-1", 1, 3, "addr"),
		"release deletes the stale row; retry must not immediately reacquire it")

	won, err := leases.Acquire(ctx, "escrow-1", 1, 3, "hot-path")
	require.NoError(t, err)
	require.True(t, won, "hot path should be able to recreate the released lease")

	time.Sleep(60 * time.Millisecond)
	rl.retryStaleValidationsForEscrow(ctx, "escrow-1")

	assert.Equal(t, 2, inner.calls)
	owned, err := leases.OwnsPendingLease(ctx, "escrow-1", 1, 3, "addr")
	require.NoError(t, err)
	require.False(t, owned, "submitted lease must no longer be pending")

	var found bool
	for _, tx := range h.HostMempool().Txs() {
		if v := tx.GetValidation(); v != nil && v.InferenceId == 1 {
			found = true
			assert.True(t, v.Valid)
		}
	}
	require.True(t, found, "second retry should publish MsgValidation")
}

func TestRetryStaleValidation_OwnershipLostAfterValidate_DoesNotSubmit(t *testing.T) {
	h := newFinishedRetryHost(t)
	leases := &stubStaleLeaseStore{
		ownsFn: func(_ context.Context, _ string, _, _ uint64, _ string) (bool, error) {
			return false, nil
		},
	}
	inner := &stubEngine{}
	rl := &ValidationRetryLoop{
		leases:       leases,
		inner:        inner,
		manager:      &stubSessionManager{snap: h},
		instanceAddr: "addr",
		leaseTTL:     DefaultValidationLeaseTTL,
	}

	err := rl.retryStaleValidation(context.Background(), "escrow-1", 1, 3)
	require.NoError(t, err)
	assert.Equal(t, 1, inner.calls)
	require.Empty(t, leases.releaseCalls, "lost ownership means this instance no longer owns a row to release")
	require.Empty(t, leases.setResultCalls, "lost ownership must not be marked submitted")
	for _, tx := range h.HostMempool().Txs() {
		require.Nil(t, tx.GetValidation(), "must not publish MsgValidation after ownership is lost")
	}
}

func TestRetryStaleValidation_SetResultLeaseNotOwnedAfterSubmit_IsBenign(t *testing.T) {
	h := newFinishedRetryHost(t)
	leases := &stubStaleLeaseStore{
		setResultFn: func(_ context.Context, _ string, _, _ uint64, _ storage.LeaseStatus, _ string) error {
			return storage.ErrLeaseNotOwned
		},
	}
	inner := &stubEngine{}
	rl := &ValidationRetryLoop{
		leases:       leases,
		inner:        inner,
		manager:      &stubSessionManager{snap: h},
		instanceAddr: "addr",
		leaseTTL:     DefaultValidationLeaseTTL,
	}

	err := rl.retryStaleValidation(context.Background(), "escrow-1", 1, 3)
	require.NoError(t, err)
	assert.Equal(t, 1, inner.calls)
	require.Empty(t, leases.releaseCalls)
	require.Equal(t, []string{"escrow-1/1/3/submitted"}, leases.setResultCalls)

	var found bool
	for _, tx := range h.HostMempool().Txs() {
		if v := tx.GetValidation(); v != nil && v.InferenceId == 1 {
			found = true
			assert.True(t, v.Valid)
		}
	}
	require.True(t, found, "mempool submit should remain successful when SetResult loses ownership")
}

func TestRetryStaleValidation_OwnsPendingLeaseErrorAfterValidate_Releases(t *testing.T) {
	h := newFinishedRetryHost(t)
	leases := &stubStaleLeaseStore{
		ownsFn: func(_ context.Context, _ string, _, _ uint64, _ string) (bool, error) {
			return false, errors.New("database unavailable")
		},
	}
	inner := &stubEngine{}
	rl := &ValidationRetryLoop{
		leases:       leases,
		inner:        inner,
		manager:      &stubSessionManager{snap: h},
		instanceAddr: "addr",
		leaseTTL:     DefaultValidationLeaseTTL,
	}

	err := rl.retryStaleValidation(context.Background(), "escrow-1", 1, 3)
	require.ErrorContains(t, err, "owns pending lease")
	assert.Equal(t, 1, inner.calls)
	require.Equal(t, []string{"escrow-1/1/3/addr"}, leases.releaseCalls)
	require.Empty(t, leases.setResultCalls)
	for _, tx := range h.HostMempool().Txs() {
		require.Nil(t, tx.GetValidation(), "must not publish MsgValidation when ownership check errors")
	}
}

func acquireMemoryLease(ctx context.Context, store *storage.Memory, escrowID string, inferenceID, epochID uint64, instanceAddr string) error {
	won, err := store.Acquire(ctx, escrowID, inferenceID, epochID, instanceAddr)
	if err != nil {
		return err
	}
	if !won {
		return fmt.Errorf("memory lease was not acquired")
	}
	return nil
}

func ownsMemoryLease(ctx context.Context, t *testing.T, store *storage.Memory, escrowID string, inferenceID, epochID uint64, instanceAddr string) bool {
	t.Helper()
	owned, err := store.OwnsPendingLease(ctx, escrowID, inferenceID, epochID, instanceAddr)
	require.NoError(t, err)
	return owned
}

func TestRetryStaleValidation_TTLExceededAfterValidate_Releases(t *testing.T) {
	leases := &stubStaleLeaseStore{}
	inner := &stubEngine{}
	rl := newTestValidationRetryLoop(leases, inferenceSnap(7, types.StatusFinished), inner)
	rl.leaseTTL = 0

	err := rl.retryStaleValidation(context.Background(), "escrow-1", 7, 3)
	require.NoError(t, err)
	assert.Equal(t, 1, inner.calls)
	require.Empty(t, leases.setResultCalls)
	require.Equal(t, []string{"escrow-1/7/3/addr"}, leases.releaseCalls)
}

func TestRetryStaleValidation_MempoolSubmitError_Releases(t *testing.T) {
	leases := &stubStaleLeaseStore{}
	inner := &stubEngine{}
	// stubHostSnap is not *host.Host, so submit fails after a successful validate.
	rl := newTestValidationRetryLoop(leases, inferenceSnap(7, types.StatusFinished), inner)

	err := rl.retryStaleValidation(context.Background(), "escrow-1", 7, 3)
	require.ErrorContains(t, err, "submit to mempool")
	assert.Equal(t, 1, inner.calls)
	require.Empty(t, leases.setResultCalls)
	require.Equal(t, []string{"escrow-1/7/3/addr"}, leases.releaseCalls)
}

func TestRetryStaleValidation_Finished_SubmitsInline(t *testing.T) {
	h := newFinishedRetryHost(t)
	leases := &stubStaleLeaseStore{}
	inner := &stubEngine{}
	rl := &ValidationRetryLoop{
		leases:       leases,
		inner:        inner,
		manager:      &stubSessionManager{snap: h},
		instanceAddr: "addr",
		leaseTTL:     DefaultValidationLeaseTTL,
	}

	err := rl.retryStaleValidation(context.Background(), "escrow-1", 1, 3)
	require.NoError(t, err)
	assert.Equal(t, 1, inner.calls)
	require.Empty(t, leases.releaseCalls)
	require.Equal(t, []string{"escrow-1/1/3/submitted"}, leases.setResultCalls)

	var found bool
	for _, tx := range h.HostMempool().Txs() {
		if v := tx.GetValidation(); v != nil && v.InferenceId == 1 {
			found = true
			assert.True(t, v.Valid)
			assert.Equal(t, uint32(0), v.ValidatorSlot)
			assert.NotEmpty(t, v.ProposerSig)
		}
		assert.Nil(t, tx.GetValidationVote(), "retry loop must not publish MsgValidationVote")
	}
	require.True(t, found, "MsgValidation should be in mempool")
}

func newFinishedRetryHost(t *testing.T) *host.Host {
	t.Helper()
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
	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000))
	require.NoError(t, err)

	engine := stub.NewInferenceEngine()
	h, err := host.NewHost(sm, hosts[0], engine, "escrow-1", group, nil)
	require.NoError(t, err)

	diff1 := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	_, err = h.HandleRequest(context.Background(), host.HostRequest{Diffs: []types.Diff{diff1}})
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, testutil.TestPromptHash[:], "llama", 100, 50, 1000, 2000)
	confirmTx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 2000,
	}}}
	finishMsg := &types.MsgFinishInference{
		InferenceId:  1,
		ResponseHash: engine.ResponseHash,
		InputTokens:  80,
		OutputTokens: 40,
		ExecutorSlot: 1,
		EscrowId:     "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[1], finishMsg)
	finishTx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: finishMsg}}
	diff2 := testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{confirmTx})
	diff3 := testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{finishTx})
	_, err = h.HandleRequest(context.Background(), host.HostRequest{Diffs: []types.Diff{diff2, diff3}})
	require.NoError(t, err)
	return h
}
