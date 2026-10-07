package host

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/observability"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

type recordingLeaseRecorder struct {
	mu           sync.Mutex
	allowErr     error
	markErr      error
	releaseCalls int
	allowCalls   int
	markCalls    int
}

func (r *recordingLeaseRecorder) AllowValidationSubmit(context.Context, string, uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.allowCalls++
	return r.allowErr
}

func (r *recordingLeaseRecorder) MarkValidationSubmitted(context.Context, string, uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markCalls++
	return r.markErr
}

func (r *recordingLeaseRecorder) ReleaseValidationLease(context.Context, string, uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseCalls++
	return nil
}

func (r *recordingLeaseRecorder) counts() (allow, mark, release int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allowCalls, r.markCalls, r.releaseCalls
}

type scriptedValidationEngine struct {
	beforeReturn func()
	result       *devshard.ValidateResult
	err          error
}

func (e *scriptedValidationEngine) Validate(context.Context, devshard.ValidateRequest) (*devshard.ValidateResult, error) {
	if e.beforeReturn != nil {
		e.beforeReturn()
	}
	if e.err != nil {
		return nil, e.err
	}
	if e.result != nil {
		return e.result, nil
	}
	return &devshard.ValidateResult{Valid: true}, nil
}

type errorSigner struct {
	addr string
	err  error
}

func (s errorSigner) Address() string             { return s.addr }
func (s errorSigner) Sign([]byte) ([]byte, error) { return nil, s.err }

func newLeaseReleaseHost(t *testing.T, validator devshard.ValidationEngine, rec *recordingLeaseRecorder) (*Host, []*signing.Secp256k1Signer, *signing.Secp256k1Signer) {
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
		VoteThreshold:    1,
		ValidationRate:   10000,
	}
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000))
	require.NoError(t, err)

	opts := []HostOption{WithGrace(10), WithValidator(validator), WithEpochID(1)}
	if rec != nil {
		opts = append(opts, WithValidationCompletionRecorder(rec))
	}
	h, err := NewHost(sm, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil, opts...)
	require.NoError(t, err)
	return h, hosts, user
}

func applyInferenceTo(t *testing.T, h *Host, hosts []*signing.Secp256k1Signer, user *signing.Secp256k1Signer, status types.InferenceStatus) {
	t.Helper()
	engine := stub.NewInferenceEngine()
	nonce := uint64(1)
	diff1 := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{testutil.StartTx(1)})
	_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff1}})
	require.NoError(t, err)
	if status == types.StatusPending {
		return
	}

	nonce++
	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, testutil.TestPromptHash[:], "llama", 100, testutil.TestMaxTokens, 1000, 2000)
	confirmTx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 2000,
	}}}
	diff2 := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{confirmTx})
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff2}})
	require.NoError(t, err)
	if status == types.StatusStarted {
		return
	}

	nonce++
	finishMsg := &types.MsgFinishInference{
		InferenceId:  1,
		ResponseHash: engine.ResponseHash, ServedHash: testutil.TestServedHash,
		InputTokens:  80,
		OutputTokens: 40,
		ExecutorSlot: 1,
		EscrowId:     "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[1], finishMsg)
	txs := []*types.DevshardTx{{Tx: &types.DevshardTx_FinishInference{FinishInference: finishMsg}}}
	if status == types.StatusChallenged {
		challengeMsg := &types.MsgValidation{
			InferenceId:   1,
			ValidatorSlot: 2,
			Valid:         false,
			EscrowId:      "escrow-1",
		}
		challengeMsg.ProposerSig = testutil.SignProposerTx(t, hosts[2], challengeMsg)
		txs = append(txs, &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: challengeMsg}})
	}
	diff3 := testutil.SignDiff(t, user, "escrow-1", nonce, txs)
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff3}})
	require.NoError(t, err)
}

func testValidateJob() validateJob {
	return validateJob{
		inferenceID:     1,
		validatorSlot:   0,
		flow:            validationFlowShouldValidate,
		model:           "llama",
		escrowID:        "escrow-1",
		executorAddress: "executor",
		epochID:         1,
	}
}

func mempoolHasValidation(h *Host, infID uint64) bool {
	for _, tx := range h.MempoolTxs() {
		if v := tx.GetValidation(); v != nil && v.InferenceId == infID {
			return true
		}
	}
	return false
}

func mempoolHasVote(h *Host, infID uint64) bool {
	for _, tx := range h.MempoolTxs() {
		if v := tx.GetValidationVote(); v != nil && v.InferenceId == infID {
			return true
		}
	}
	return false
}

func cooldownUntil(h *Host, inferenceID uint64) (time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	until, ok := h.validationCooldown[inferenceID]
	return until, ok
}

func collectValidationJobsLocked(h *Host) []validateJob {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.collectValidationJobs()
}

func TestHost_ValidateAsync_ReleasesOnNonSubmitPaths(t *testing.T) {
	signFail := errors.New("sign failed")
	tests := []struct {
		name             string
		status           types.InferenceStatus
		skipApply        bool
		validator        scriptedValidationEngine
		allowErr         error
		markErr          error
		failSign         bool
		wantRelease      int
		wantAllow        int
		wantMark         int
		wantVal          bool
		wantVote         bool
		wantCooldown     bool
		wantCooldownHold bool
	}{
		{
			name:         "validate error",
			status:       types.StatusFinished,
			validator:    scriptedValidationEngine{err: errors.New("local ml 503")},
			wantRelease:  1,
			wantCooldown: true,
		},
		{
			name:         "validation deferred",
			status:       types.StatusFinished,
			validator:    scriptedValidationEngine{err: devshard.ErrValidationDeferred},
			wantRelease:  1,
			wantCooldown: true,
		},
		{
			name:         "validation skipped",
			status:       types.StatusFinished,
			validator:    scriptedValidationEngine{err: devshard.ErrValidationSkipped},
			wantRelease:  1,
			wantCooldown: true,
		},
		{
			name:         "already leased",
			status:       types.StatusFinished,
			validator:    scriptedValidationEngine{err: devshard.ErrValidationAlreadyLeased},
			wantCooldown: true,
		},
		{
			// Releasing here would free a row this attempt never acquired.
			// The row is still there, so the next request waits out the cooldown.
			name:   "lease conflict",
			status: types.StatusFinished,
			validator: scriptedValidationEngine{err: &devshard.LeaseConflict{
				Status: devshard.LeaseStatusPending,
				Owner:  "gonka1owner",
			}},
			wantCooldown: true,
		},
		{
			name:   "lease conflict submitted",
			status: types.StatusFinished,
			validator: scriptedValidationEngine{err: &devshard.LeaseConflict{
				Status: devshard.LeaseStatusSubmitted,
			}},
			wantCooldown: true,
		},
		{
			name:   "lease conflict skipped",
			status: types.StatusFinished,
			validator: scriptedValidationEngine{err: &devshard.LeaseConflict{
				Status: devshard.LeaseStatusSkipped,
			}},
			wantCooldown:     true,
			wantCooldownHold: true,
		},
		{
			name:   "lease conflict stale pending",
			status: types.StatusFinished,
			validator: scriptedValidationEngine{err: &devshard.LeaseConflict{
				Status: devshard.LeaseStatusPending,
				Stale:  true,
			}},
			wantCooldown: true,
		},
		{
			name:   "lease conflict read failed",
			status: types.StatusFinished,
			validator: scriptedValidationEngine{err: &devshard.LeaseConflict{
				Detail: "lease read failed: db down",
			}},
			wantCooldown: true,
		},
		{
			name:   "lease conflict already released",
			status: types.StatusFinished,
			validator: scriptedValidationEngine{err: &devshard.LeaseConflict{
				Detail: devshard.LeaseRowAbsentDetail,
			}},
		},
		{
			name:         "inference disappeared",
			skipApply:    true,
			wantRelease:  1,
			wantCooldown: true,
		},
		{
			name:         "status neither finished nor challenged",
			status:       types.StatusStarted,
			wantRelease:  1,
			wantCooldown: true,
		},
		{
			name:         "sign fail finished",
			status:       types.StatusFinished,
			failSign:     true,
			wantRelease:  1,
			wantCooldown: true,
		},
		{
			name:         "sign fail challenged",
			status:       types.StatusChallenged,
			failSign:     true,
			wantRelease:  1,
			wantCooldown: true,
		},
		{
			name:        "allow submit refused: lost ownership",
			status:      types.StatusFinished,
			allowErr:    devshard.ErrValidationLeaseAbandoned,
			wantRelease: 1,
			wantAllow:   1,
		},
		{
			name:         "allow submit refused: TTL exceeded",
			status:       types.StatusFinished,
			allowErr:     fmt.Errorf("%w: %w", devshard.ErrValidationLeaseAbandoned, devshard.ErrValidationLeaseTTLExceeded),
			wantRelease:  1,
			wantAllow:    1,
			wantCooldown: true,
		},
		{
			name:      "mark submitted failed after mempool add",
			status:    types.StatusFinished,
			markErr:   errors.New("db unavailable"),
			wantAllow: 1,
			wantMark:  1,
			wantVal:   true,
		},
		{
			name:      "success finished does not release",
			status:    types.StatusFinished,
			wantAllow: 1,
			wantMark:  1,
			wantVal:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recordingLeaseRecorder{allowErr: tt.allowErr, markErr: tt.markErr}
			validator := tt.validator
			h, hosts, user := newLeaseReleaseHost(t, &validator, rec)
			initialStatus := tt.status
			if tt.skipApply || tt.status == types.StatusStarted {
				initialStatus = types.StatusFinished
				// Keep exercising state changes after the new preflight check.
				validator.beforeReturn = func() {
					snapshot := h.sm.SnapshotState()
					if tt.skipApply {
						delete(snapshot.Inferences, 1)
					} else {
						snapshot.Inferences[1].Status = tt.status
					}
					restoreWithLiveFloor(t, h.sm, &snapshot)
				}
			}
			applyInferenceTo(t, h, hosts, user, initialStatus)
			if tt.failSign {
				h.signer = errorSigner{addr: hosts[0].Address(), err: signFail}
			}

			h.validateAsync(context.Background(), testValidateJob())

			allow, mark, release := rec.counts()
			require.Equal(t, tt.wantRelease, release)
			require.Equal(t, tt.wantAllow, allow)
			require.Equal(t, tt.wantMark, mark)
			require.Equal(t, tt.wantVal, mempoolHasValidation(h, 1))
			require.Equal(t, tt.wantVote, mempoolHasVote(h, 1))
			until, onCooldown := cooldownUntil(h, 1)
			require.Equal(t, tt.wantCooldown, onCooldown)
			if tt.wantCooldownHold {
				require.True(t, until.IsZero(), "skipped lease must be held, not retried on the 30s cooldown")
			} else if tt.wantCooldown {
				require.True(t, until.After(time.Now()), "cooldown must be in the future")
				require.True(t, time.Until(until) <= validationCooldown)
			}
		})
	}
}

func TestHost_ValidateAsync_CanceledReleases(t *testing.T) {
	rec := &recordingLeaseRecorder{}
	h, hosts, user := newLeaseReleaseHost(t, &scriptedValidationEngine{err: errors.New("local ml 503")}, rec)
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.validateAsync(ctx, testValidateJob())

	_, _, release := rec.counts()
	require.Equal(t, 1, release, "aborted Validate must free the lease for sibling re-acquire")
}

// releaseOnErrorEngine mimics LeaseValidator: DELETE the pending row when
// inner Validate fails, including context cancel on shutdown.
type releaseOnErrorEngine struct {
	inner    devshard.ValidationEngine
	releases atomic.Int32
}

func (e *releaseOnErrorEngine) Validate(ctx context.Context, req devshard.ValidateRequest) (*devshard.ValidateResult, error) {
	res, err := e.inner.Validate(ctx, req)
	if err != nil {
		e.releases.Add(1)
	}
	return res, err
}

func TestHost_CloseWaitsForInFlightLeaseRelease(t *testing.T) {
	inner := newBlockingValidationEngine(1)
	engine := &releaseOnErrorEngine{inner: inner}
	h, hosts, user := newLeaseReleaseHost(t, engine, nil)
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)
	h.Start()
	t.Cleanup(h.Close)

	h.validationLifecycleMu.RLock()
	q := h.validationQueue
	h.validationLifecycleMu.RUnlock()
	require.NotNil(t, q)
	q <- testValidateJob()

	select {
	case <-inner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Validate did not start")
	}

	closed := make(chan struct{})
	go func() {
		h.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after canceling in-flight Validate")
	}
	require.Equal(t, int32(1), engine.releases.Load(), "Close must wait until abort Release runs while storage is still open")
}

func TestHost_CloseWithoutStartDoesNotBlock(t *testing.T) {
	h, _, _ := newLeaseReleaseHost(t, &scriptedValidationEngine{}, nil)
	closed := make(chan struct{})
	go func() {
		h.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close of an unstarted host blocked")
	}
}

// TestLeaseConflictSeverity pins which conflicts are worth a warning. A
// submitted row and a young pending row are the dedup guard working, so grading
// them as errors is what made this path unreadable in the first place.
func TestLeaseConflictSeverity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		conflict  *devshard.LeaseConflict
		wantLevel observability.Level
		wantMsg   string
	}{
		{
			name:      "pending within ttl",
			conflict:  &devshard.LeaseConflict{Status: devshard.LeaseStatusPending},
			wantLevel: observability.LevelInfo,
			wantMsg:   "lease already held",
		},
		{
			name:      "pending past ttl",
			conflict:  &devshard.LeaseConflict{Status: devshard.LeaseStatusPending, Stale: true},
			wantLevel: observability.LevelWarn,
			wantMsg:   "lease held past TTL",
		},
		{
			name:      "submitted",
			conflict:  &devshard.LeaseConflict{Status: devshard.LeaseStatusSubmitted},
			wantLevel: observability.LevelInfo,
			wantMsg:   "lease already submitted",
		},
		{
			name:      "skipped",
			conflict:  &devshard.LeaseConflict{Status: devshard.LeaseStatusSkipped},
			wantLevel: observability.LevelWarn,
			wantMsg:   "lease marked skipped for this epoch",
		},
		{
			name:      "row not read",
			conflict:  &devshard.LeaseConflict{Detail: devshard.LeaseRowAbsentDetail},
			wantLevel: observability.LevelInfo,
			wantMsg:   "lease already held",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			level, msg := leaseConflictSeverity(tt.conflict)
			require.Equal(t, tt.wantLevel, level)
			require.Contains(t, msg, tt.wantMsg)
		})
	}
}

func TestHost_ValidateAsync_ClosedDoesNotRelease(t *testing.T) {
	rec := &recordingLeaseRecorder{}
	h, hosts, user := newLeaseReleaseHost(t, &scriptedValidationEngine{err: errors.New("local ml 503")}, rec)
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)
	h.Close()
	h.validateAsync(context.Background(), testValidateJob())

	_, _, release := rec.counts()
	require.Equal(t, 0, release, "Host.Close must not double-release after workers are canceled")
	_, onCooldown := cooldownUntil(h, 1)
	require.False(t, onCooldown)
}

func TestHost_ValidateAsync_ErrorReleaseCooldownThenRecollects(t *testing.T) {
	rec := &recordingLeaseRecorder{}
	validator := &scriptedValidationEngine{err: errors.New("local ml 503")}
	h, hosts, user := newTwoHostValidationHost(t, validator)
	h.validationRecorder = rec
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)

	h.validationLifecycleMu.Lock()
	h.validationQueue = make(chan validateJob, defaultValidationQueueSize)
	h.validationLifecycleMu.Unlock()

	h.validateAsync(context.Background(), testValidateJob())

	_, _, release := rec.counts()
	require.Equal(t, 1, release, "validation error must release the owned lease")
	_, onCooldown := cooldownUntil(h, 1)
	require.True(t, onCooldown, "validation error must stamp cooldown")

	jobs := collectValidationJobsLocked(h)
	for _, job := range jobs {
		require.NotEqual(t, uint64(1), job.inferenceID, "cooldown must block immediate re-pick")
	}

	h.mu.Lock()
	h.validationCooldown[1] = time.Now().Add(-time.Nanosecond)
	delete(h.validating, 1)
	h.mu.Unlock()

	jobs = collectValidationJobsLocked(h)
	var found bool
	for _, job := range jobs {
		if job.inferenceID == 1 {
			found = true
		}
	}
	require.True(t, found, "expired cooldown must allow the same inference to be collected again")
	_, still := cooldownUntil(h, 1)
	require.False(t, still, "expired cooldown entry must be cleared when the job is collected")
}

func TestHost_ValidateAsync_SubmitAbandonedLostOwnershipReleasesWithoutCooldown(t *testing.T) {
	rec := &recordingLeaseRecorder{allowErr: devshard.ErrValidationLeaseAbandoned}
	validator := &scriptedValidationEngine{result: &devshard.ValidateResult{Valid: true}}
	h, hosts, user := newTwoHostValidationHost(t, validator)
	h.validationRecorder = rec
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)

	h.validateAsync(context.Background(), testValidateJob())

	allow, mark, release := rec.counts()
	require.Equal(t, 1, allow)
	require.Equal(t, 0, mark)
	require.Equal(t, 1, release, "lost ownership must release the local remembered lease")
	require.False(t, mempoolHasValidation(h, 1), "abandoned submit must not publish validation")
	_, onCooldown := cooldownUntil(h, 1)
	require.False(t, onCooldown, "lost ownership should not throttle future collection")
}

func TestHost_ValidateAsync_SubmitAbandonedTTLExceededReleasesAndCooldowns(t *testing.T) {
	rec := &recordingLeaseRecorder{
		allowErr: fmt.Errorf("%w: %w", devshard.ErrValidationLeaseAbandoned, devshard.ErrValidationLeaseTTLExceeded),
	}
	validator := &scriptedValidationEngine{result: &devshard.ValidateResult{Valid: true}}
	h, hosts, user := newTwoHostValidationHost(t, validator)
	h.validationRecorder = rec
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)

	h.validateAsync(context.Background(), testValidateJob())

	allow, mark, release := rec.counts()
	require.Equal(t, 1, allow)
	require.Equal(t, 0, mark)
	require.Equal(t, 1, release, "TTL abandonment must release the local remembered lease")
	require.False(t, mempoolHasValidation(h, 1), "abandoned submit must not publish validation")
	until, onCooldown := cooldownUntil(h, 1)
	require.True(t, onCooldown, "TTL exceeded should throttle immediate recollection")
	require.True(t, until.After(time.Now()), "cooldown must be in the future")
	require.True(t, time.Until(until) <= validationCooldown)
}

func TestHost_ValidateAsync_AllowSubmitGenericErrorReleasesWithoutCooldown(t *testing.T) {
	rec := &recordingLeaseRecorder{allowErr: errors.New("owns pending lease: database unavailable")}
	validator := &scriptedValidationEngine{result: &devshard.ValidateResult{Valid: true}}
	h, hosts, user := newTwoHostValidationHost(t, validator)
	h.validationRecorder = rec
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)

	h.validationLifecycleMu.Lock()
	h.validationQueue = make(chan validateJob, defaultValidationQueueSize)
	h.validationLifecycleMu.Unlock()

	h.validateAsync(context.Background(), testValidateJob())

	allow, mark, release := rec.counts()
	require.Equal(t, 1, allow)
	require.Equal(t, 0, mark)
	require.Equal(t, 1, release, "submit gate errors must release the remembered lease")
	require.False(t, mempoolHasValidation(h, 1), "submit gate errors must not publish validation")
	_, onCooldown := cooldownUntil(h, 1)
	require.False(t, onCooldown, "generic submit gate errors currently do not stamp cooldown")

	jobs := collectValidationJobsLocked(h)
	var found bool
	for _, job := range jobs {
		if job.inferenceID == 1 {
			found = true
		}
	}
	require.True(t, found, "without cooldown, the same inference is immediately collectible again")
}

func TestHost_ChallengedInferencePublishesValidationVote(t *testing.T) {
	rec := &recordingLeaseRecorder{}
	valEngine := &trackingValidationEngine{valid: true}
	h, hosts, user := newLeaseReleaseHost(t, valEngine, rec)
	h.Start()
	t.Cleanup(h.Close)

	applyInferenceTo(t, h, hosts, user, types.StatusChallenged)

	require.Eventually(t, func() bool {
		return mempoolHasVote(h, 1)
	}, 2*time.Second, 10*time.Millisecond, "MsgValidationVote should be in mempool")
	require.False(t, mempoolHasValidation(h, 1), "challenged inference must not publish MsgValidation")
	_, mark, release := rec.counts()
	require.Equal(t, 0, release)
	require.Equal(t, 1, mark)
	_, onCooldown := cooldownUntil(h, 1)
	require.False(t, onCooldown)
}

type gateValidationEngine struct {
	entered chan struct{}
	release chan struct{}
	result  *devshard.ValidateResult
	calls   atomic.Int32
}

func (e *gateValidationEngine) Validate(context.Context, devshard.ValidateRequest) (*devshard.ValidateResult, error) {
	e.calls.Add(1)
	if e.entered != nil {
		close(e.entered)
	}
	if e.release != nil {
		<-e.release
	}
	if e.result != nil {
		return e.result, nil
	}
	return &devshard.ValidateResult{Valid: true}, nil
}

func TestHost_StopValidationEnqueue_SkipsNewWork(t *testing.T) {
	t.Cleanup(resetValidationEnqueueForTest)
	engine := &gateValidationEngine{}
	h, hosts, user := newTwoHostValidationHost(t, engine)
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)
	h.Start()
	t.Cleanup(h.Close)

	StopValidationEnqueue()
	require.Empty(t, collectValidationJobsLocked(h))
	h.validateAsync(context.Background(), testValidateJob())
	require.Equal(t, int32(0), engine.calls.Load())

	resetValidationEnqueueForTest()
	require.NotEmpty(t, collectValidationJobsLocked(h))
}

func TestHost_InFlightValidationVotesAfterEnqueueStop(t *testing.T) {
	t.Cleanup(resetValidationEnqueueForTest)
	entered := make(chan struct{})
	release := make(chan struct{})
	engine := &gateValidationEngine{
		entered: entered,
		release: release,
		result:  &devshard.ValidateResult{Valid: true},
	}
	h, hosts, user := newTwoHostValidationHost(t, engine)
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)

	done := make(chan struct{})
	go func() {
		h.validateAsync(context.Background(), testValidateJob())
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("validation did not enter Validate")
	}
	StopValidationEnqueue()
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("validation did not finish")
	}
	require.Equal(t, int32(1), engine.calls.Load())
	var valid bool
	var found bool
	for _, tx := range h.MempoolTxs() {
		if v := tx.GetValidation(); v != nil && v.InferenceId == 1 {
			valid = v.Valid
			found = true
		}
	}
	require.True(t, found)
	require.True(t, valid)
}

func TestHost_FetchFailureVerdict_PublishesInvalidValidation(t *testing.T) {
	rec := &recordingLeaseRecorder{}
	val := &scriptedValidationEngine{result: &devshard.ValidateResult{
		Valid:  false,
		Reason: "executor_payload_unavailable",
	}}
	h, hosts, user := newTwoHostValidationHost(t, val)
	h.validationRecorder = rec
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)
	h.validateAsync(context.Background(), testValidateJob())

	require.True(t, mempoolHasValidation(h, 1), "fetch-failure verdict must publish MsgValidation")
	var found bool
	for _, tx := range h.MempoolTxs() {
		if v := tx.GetValidation(); v != nil && v.InferenceId == 1 {
			require.False(t, v.Valid, "executor payload unavailability must vote false")
			found = true
		}
	}
	require.True(t, found)
	_, mark, release := rec.counts()
	require.Equal(t, 1, mark, "false verdict is submitted, not released")
	require.Equal(t, 0, release)
}

// restoreWithLiveFloor swaps in an edited snapshot. These hosts run without a
// store, so the state machine's store holds no journal to fold; the live
// floor is the snapshot floor a real restore would carry.
func restoreWithLiveFloor(t *testing.T, sm *state.StateMachine, st *types.EscrowState) {
	t.Helper()
	floor, err := heightsync.FloorIndexFromProto(heightsync.FloorConfig{}, sm.ExportHeightSyncFloor())
	require.NoError(t, err)
	require.NoError(t, sm.RestoreStateWithFloor(st, floor))
}

func newTwoHostValidationHost(t *testing.T, validator devshard.ValidationEngine) (*Host, []*signing.Secp256k1Signer, *signing.Secp256k1Signer) {
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
	h, err := NewHost(sm, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil,
		WithGrace(10), WithValidator(validator), WithEpochID(1))
	require.NoError(t, err)
	return h, hosts, user
}

func TestHost_CollectValidationJobs_SkipsCooldownThenPicksAfterExpiry(t *testing.T) {
	h, hosts, user := newTwoHostValidationHost(t, stub.NewValidationEngine())
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)
	h.Start()
	t.Cleanup(h.Close)

	h.mu.Lock()
	h.validationCooldown[1] = time.Now().Add(time.Hour)
	h.mu.Unlock()

	jobs := collectValidationJobsLocked(h)
	for _, job := range jobs {
		require.NotEqual(t, uint64(1), job.inferenceID, "cooldown must block re-pick")
	}

	h.mu.Lock()
	h.validationCooldown[1] = time.Now().Add(-time.Nanosecond)
	delete(h.validating, 1)
	h.mu.Unlock()

	jobs = collectValidationJobsLocked(h)
	var found bool
	for _, job := range jobs {
		if job.inferenceID == 1 {
			found = true
		}
	}
	require.True(t, found, "expired cooldown must allow re-pick")
	_, still := cooldownUntil(h, 1)
	require.False(t, still, "expired cooldown entry must be dropped on pick")
}

func TestHost_CollectValidationJobs_SkippedLeaseStaysHeld(t *testing.T) {
	h, hosts, user := newTwoHostValidationHost(t, stub.NewValidationEngine())
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)
	h.Start()
	t.Cleanup(h.Close)

	h.mu.Lock()
	h.validationCooldown[1] = time.Time{}
	delete(h.validating, 1)
	h.mu.Unlock()

	jobs := collectValidationJobsLocked(h)
	for _, job := range jobs {
		require.NotEqual(t, uint64(1), job.inferenceID, "a held skipped lease must not be re-picked")
	}
	until, onCooldown := cooldownUntil(h, 1)
	require.True(t, onCooldown)
	require.True(t, until.IsZero(), "the hold must survive collection")

	h.mu.Lock()
	h.validationCooldown[99] = time.Time{}
	h.mu.Unlock()
	_ = collectValidationJobsLocked(h)
	_, gone := cooldownUntil(h, 99)
	require.False(t, gone, "a hold for an inference outside the live set must be pruned")
}

func TestHost_CollectValidationJobs_PrunesCooldownForEvictedInferences(t *testing.T) {
	h, hosts, user := newTwoHostValidationHost(t, stub.NewValidationEngine())
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)
	h.Start()
	t.Cleanup(h.Close)

	h.mu.Lock()
	h.validationCooldown[99] = time.Now().Add(time.Hour)
	h.mu.Unlock()

	_ = collectValidationJobsLocked(h)
	_, ok := cooldownUntil(h, 99)
	require.False(t, ok, "cooldown for an inference no longer in the live set must be pruned")
}

func TestHost_CollectValidationJobs_QueueFullDoesNotAcquireOrCooldown(t *testing.T) {
	h, hosts, user := newTwoHostValidationHost(t, stub.NewValidationEngine())
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)

	h.validationLifecycleMu.Lock()
	h.validationQueue = make(chan validateJob, 1)
	h.validationQueue <- testValidateJob()
	h.validationLifecycleMu.Unlock()

	jobs := collectValidationJobsLocked(h)
	require.Empty(t, jobs, "full validation queue must skip collection")

	h.mu.Lock()
	_, validating := h.validating[1]
	_, onCooldown := h.validationCooldown[1]
	h.mu.Unlock()
	require.False(t, validating, "queue-full collection must not reserve the inference")
	require.False(t, onCooldown, "queue-full collection must not stamp cooldown")
}

func TestHostDeferredValidationRecordsFinished(t *testing.T) {
	deferredCount := func() float64 {
		families, err := observability.Registry().Gather()
		require.NoError(t, err)
		for _, family := range families {
			if family.GetName() != "devshard_validation_total" {
				continue
			}
			for _, metric := range family.Metric {
				labels := map[string]string{}
				for _, label := range metric.Label {
					labels[label.GetName()] = label.GetValue()
				}
				if labels["stage"] == "validation_finished" && labels["status"] == "deferred" {
					return metric.GetCounter().GetValue()
				}
			}
		}
		return 0
	}
	rec := &recordingLeaseRecorder{}
	h, hosts, user := newLeaseReleaseHost(t, &scriptedValidationEngine{err: devshard.ErrValidationDeferred}, rec)
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)
	before := deferredCount()
	h.validateAsync(context.Background(), testValidateJob())
	require.Equal(t, before+1, deferredCount())
}

type creditGatedValidator struct {
	available bool
	calls     int
}

func (v *creditGatedValidator) CanValidate(string) bool { return v.available }
func (v *creditGatedValidator) Validate(context.Context, devshard.ValidateRequest) (*devshard.ValidateResult, error) {
	v.calls++
	return nil, devshard.ErrValidationDeferred
}
func TestHostValidationCreditScheduling(t *testing.T) {
	v := &creditGatedValidator{}
	h, hosts, user := newTwoHostValidationHost(t, v)
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)
	h.validationQueue = make(chan validateJob, defaultValidationQueueSize)
	require.Empty(t, collectValidationJobsLocked(h))
	require.Empty(t, h.validating)
	require.Empty(t, h.validationCooldown)
	v.available = true
	jobs := collectValidationJobsLocked(h)
	require.Len(t, jobs, 1, "earning credit makes the obligation schedulable")
	v.available = false
	h.validateAsync(context.Background(), jobs[0])
	require.Zero(t, v.calls, "queued work must recheck before validation or leases")
	require.Empty(t, h.validating)
	require.Empty(t, h.validationCooldown)
	v.available = true
	require.Len(t, collectValidationJobsLocked(h), 1, "deferral keeps the obligation")
}
