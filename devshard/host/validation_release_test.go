package host

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/internal/testutil"
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
	result *devshard.ValidateResult
	err    error
}

func (e *scriptedValidationEngine) Validate(context.Context, devshard.ValidateRequest) (*devshard.ValidateResult, error) {
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
		ResponseHash: engine.ResponseHash,
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
		if v := tx.GetValidation(); v != nil && v.GetInferenceId() == infID {
			return true
		}
	}
	return false
}

func mempoolHasVote(h *Host, infID uint64) bool {
	for _, tx := range h.MempoolTxs() {
		if v := tx.GetValidationVote(); v != nil && v.GetInferenceId() == infID {
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
		name         string
		status       types.InferenceStatus
		skipApply    bool
		validator    scriptedValidationEngine
		allowErr     error
		markErr      error
		failSign     bool
		wantRelease  int
		wantAllow    int
		wantMark     int
		wantVal      bool
		wantVote     bool
		wantCooldown bool
	}{
		{
			name:         "validate error",
			skipApply:    true,
			validator:    scriptedValidationEngine{err: errors.New("local ml 503")},
			wantRelease:  1,
			wantCooldown: true,
		},
		{
			name:         "validation skipped",
			skipApply:    true,
			validator:    scriptedValidationEngine{err: devshard.ErrValidationSkipped},
			wantRelease:  1,
			wantCooldown: true,
		},
		{
			name:      "already leased",
			skipApply: true,
			validator: scriptedValidationEngine{err: devshard.ErrValidationAlreadyLeased},
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
			if !tt.skipApply {
				applyInferenceTo(t, h, hosts, user, tt.status)
			}
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
			if tt.wantCooldown {
				require.True(t, until.After(time.Now()), "cooldown must be in the future")
				require.LessOrEqual(t, time.Until(until), validationCooldown)
			}
		})
	}
}

func TestHost_ValidateAsync_CanceledReleases(t *testing.T) {
	rec := &recordingLeaseRecorder{}
	h, _, _ := newLeaseReleaseHost(t, &scriptedValidationEngine{err: errors.New("local ml 503")}, rec)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.validateAsync(ctx, testValidateJob())

	_, _, release := rec.counts()
	require.Equal(t, 1, release, "aborted Validate must free the lease for sibling re-acquire")
}

func TestHost_ValidateAsync_ClosedDoesNotRelease(t *testing.T) {
	rec := &recordingLeaseRecorder{}
	h, _, _ := newLeaseReleaseHost(t, &scriptedValidationEngine{err: errors.New("local ml 503")}, rec)
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
	require.LessOrEqual(t, time.Until(until), validationCooldown)
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
		if v := tx.GetValidation(); v != nil && v.GetInferenceId() == 1 {
			require.False(t, v.GetValid(), "executor payload unavailability must vote false")
			found = true
		}
	}
	require.True(t, found)
	_, mark, release := rec.counts()
	require.Equal(t, 1, mark, "false verdict is submitted, not released")
	require.Equal(t, 0, release)
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
