package inference

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"common/chain"
	commonvalidation "common/validation"
	devshardpkg "devshard"
	"devshard/storage"
)

// stubLeases implements leaseOps for testing.
type stubLeases struct {
	acquireFn      func(ctx context.Context, escrowId string, inferenceId uint64, epochId uint64, instanceAddr string) (bool, error)
	setResultFn    func(ctx context.Context, escrowId string, inferenceId, epochId uint64, status storage.LeaseStatus, instanceAddr string) error
	ownsFn         func(ctx context.Context, escrowId string, inferenceId, epochId uint64, instanceAddr string) (bool, error)
	releaseFn      func(ctx context.Context, escrowId string, inferenceId, epochId uint64, instanceAddr string) error
	setResultCalls []string // records "escrowId/inferenceId/epochId/status"
	releaseCalls   []string // records "escrowId/inferenceId/epochId/instanceAddr"
	acquireEpochs  []uint64
}

func (s *stubLeases) Acquire(ctx context.Context, escrowId string, inferenceId uint64, epochId uint64, instanceAddr string) (bool, error) {
	s.acquireEpochs = append(s.acquireEpochs, epochId)
	return s.acquireFn(ctx, escrowId, inferenceId, epochId, instanceAddr)
}

func (s *stubLeases) SetResult(ctx context.Context, escrowId string, inferenceId, epochId uint64, status storage.LeaseStatus, instanceAddr string) error {
	s.setResultCalls = append(s.setResultCalls, fmt.Sprintf("%s/%d/%d/%s", escrowId, inferenceId, epochId, status))
	if s.setResultFn != nil {
		return s.setResultFn(ctx, escrowId, inferenceId, epochId, status, instanceAddr)
	}
	return nil
}

func (s *stubLeases) OwnsPendingLease(ctx context.Context, escrowId string, inferenceId, epochId uint64, instanceAddr string) (bool, error) {
	if s.ownsFn != nil {
		return s.ownsFn(ctx, escrowId, inferenceId, epochId, instanceAddr)
	}
	return true, nil
}

func (s *stubLeases) Release(ctx context.Context, escrowId string, inferenceId, epochId uint64, instanceAddr string) error {
	s.releaseCalls = append(s.releaseCalls, fmt.Sprintf("%s/%d/%d/%s", escrowId, inferenceId, epochId, instanceAddr))
	if s.releaseFn != nil {
		return s.releaseFn(ctx, escrowId, inferenceId, epochId, instanceAddr)
	}
	return nil
}

func makeReq() devshardpkg.ValidateRequest {
	return devshardpkg.ValidateRequest{
		InferenceID: 42,
		EscrowID:    "escrow-1",
		Model:       "test-model",
	}
}

// stubValidator implements devshardpkg.ValidationEngine for testing.
type stubValidator struct {
	fn func(context.Context, devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error)
}

func (s *stubValidator) Validate(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	return s.fn(ctx, req)
}

// newTestLeaseValidator builds a LeaseValidator wrapping a stub ValidationEngine.
// phase is always a zero *chain.Phase (EpochID returns 0).
func newTestLeaseValidator(leases leaseOps, innerFn func(context.Context, devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error)) *LeaseValidator {
	return NewLeaseValidator(&stubValidator{fn: innerFn}, new(chain.Phase), leases, "validator-addr", time.Hour)
}

// successInner returns a valid result.
func successInner(_ context.Context, _ devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	return &devshardpkg.ValidateResult{Valid: true}, nil
}

func TestResolveValidationEpoch(t *testing.T) {
	t.Parallel()
	phase := new(chain.Phase)
	phase.Update(10, 0)
	assert.Equal(t, uint64(5), resolveValidationEpoch(phase, 5))
	assert.Equal(t, uint64(10), resolveValidationEpoch(phase, 0))
}

func TestLeaseValidator_AcquireUsesRequestEpoch(t *testing.T) {
	store := &stubLeases{
		acquireFn: func(_ context.Context, _ string, _ uint64, epochId uint64, _ string) (bool, error) {
			return true, nil
		},
	}
	phase := new(chain.Phase)
	phase.Update(11, 0)
	c := NewLeaseValidator(&stubValidator{fn: successInner}, phase, store, "validator-addr", time.Hour)

	req := makeReq()
	req.EpochID = 5
	_, err := c.Validate(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, []uint64{5}, store.acquireEpochs)

	req.EpochID = 0
	_, err = c.Validate(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, []uint64{5, 11}, store.acquireEpochs, "unset request epoch falls back to phase")
}

// TestLeaseValidator_InvalidResult_DoesNotSetSubmitted verifies that an
// inner Valid:false result (hash mismatch or executor payload fault, converted
// in Validator.Validate) is passed through without completing the lease.
func TestLeaseValidator_InvalidResult_DoesNotSetSubmitted(t *testing.T) {
	store := &stubLeases{
		acquireFn: func(_ context.Context, _ string, _ uint64, _ uint64, _ string) (bool, error) {
			return true, nil
		},
	}
	c := newTestLeaseValidator(store, func(_ context.Context, _ devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
		return &devshardpkg.ValidateResult{Valid: false, Reason: executorPayloadUnavailableReason}, nil
	})

	result, err := c.Validate(context.Background(), makeReq())
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Equal(t, executorPayloadUnavailableReason, result.Reason)
	require.Empty(t, store.setResultCalls)
	require.Empty(t, store.releaseCalls, "executor-fault verdict is submitted by the caller; do not release")
}

// TestLeaseValidator_LeaseLost_ReturnsLeasedEachCall verifies that every call
// goes to Postgres; losing the lease always returns ErrValidationAlreadyLeased.
func TestLeaseValidator_LeaseLost_ReturnsLeasedEachCall(t *testing.T) {
	acquireCalls := 0
	store := &stubLeases{
		acquireFn: func(_ context.Context, _ string, _ uint64, _ uint64, _ string) (bool, error) {
			acquireCalls++
			return false, nil
		},
	}
	c := newTestLeaseValidator(store, successInner)

	_, err := c.Validate(context.Background(), makeReq())
	assert.ErrorIs(t, err, devshardpkg.ErrValidationAlreadyLeased)
	_, err = c.Validate(context.Background(), makeReq())
	assert.ErrorIs(t, err, devshardpkg.ErrValidationAlreadyLeased)
	assert.Equal(t, 2, acquireCalls, "Acquire must be called for every Validate call")
	require.Empty(t, store.releaseCalls, "never acquired, so must not release")
}

// TestLeaseValidator_LeaseDBError_FailsClosed verifies that a lease store
// failure prevents validation from running without cross-instance deduplication.
func TestLeaseValidator_LeaseDBError_FailsClosed(t *testing.T) {
	store := &stubLeases{
		acquireFn: func(_ context.Context, _ string, _ uint64, _ uint64, _ string) (bool, error) {
			return false, errors.New("connection refused")
		},
	}
	innerCalls := 0
	c := newTestLeaseValidator(store, func(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
		innerCalls++
		return successInner(ctx, req)
	})

	result, err := c.Validate(context.Background(), makeReq())
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Equal(t, 0, innerCalls)
}

// TestLeaseValidator_Success_DoesNotSetSubmitted verifies that validation
// execution does not complete the lease before MsgValidation is submitted.
func TestLeaseValidator_Success_DoesNotSetSubmitted(t *testing.T) {
	store := &stubLeases{
		acquireFn: func(_ context.Context, _ string, _ uint64, _ uint64, _ string) (bool, error) {
			return true, nil
		},
	}
	c := newTestLeaseValidator(store, successInner)

	result, err := c.Validate(context.Background(), makeReq())
	require.NoError(t, err)
	assert.True(t, result.Valid)
	require.Empty(t, store.setResultCalls)
}

type stubThresholdResolver struct {
	threshold float64
	err       error
}

func (s stubThresholdResolver) Resolve(_ context.Context, _ uint64, _ string) (float64, error) {
	if s.err != nil {
		return 0, s.err
	}
	return s.threshold, nil
}

type unknownValidationResult struct{}

func (unknownValidationResult) IsSuccessful() bool                 { return true }
func (unknownValidationResult) GetInferenceId() string             { return "unknown" }
func (unknownValidationResult) GetValidationResponseBytes() []byte { return nil }

// The executor salts, the validator must not: a replay carrying a cache_salt would
// diverge from the prompt the executor signed and committed.
func TestValidatorExecuteMLRequest_ReplaysWithoutCacheSalt(t *testing.T) {
	var gotBody []byte
	srv := captureBodyMLServer(t, &gotBody)
	engine := newTestEngineForNode(t, srv.URL)
	engine.affinityEnabled = true
	validator := &Validator{engine: engine}
	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	resp, err := validator.executeMLRequest(context.Background(), "model-a", "escrow-1", prompt)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	assert.JSONEq(t, string(prompt), string(gotBody), "the validator must replay the executor's prompt with no cache_salt added")
}

func TestEvaluateValidationResult_UsesModelThreshold(t *testing.T) {
	resolver := stubThresholdResolver{threshold: 0.90}

	tests := []struct {
		name       string
		similarity float64
		want       bool
	}{
		{name: "above threshold passes", similarity: 0.91, want: true},
		{name: "equal threshold fails", similarity: 0.90, want: false},
		{name: "below threshold fails", similarity: 0.89, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &commonvalidation.SimilarityValidationResult{Value: tt.similarity}
			valid, err := evaluateValidationResult(context.Background(), result, 7, "model-a", resolver)
			require.NoError(t, err)
			assert.Equal(t, tt.want, valid)
		})
	}
}

func TestEvaluateValidationResult_KnownFailureTypesFailWithoutThreshold(t *testing.T) {
	results := []commonvalidation.ValidationResult{
		&commonvalidation.DifferentLengthValidationResult{},
		&commonvalidation.DifferentTokensValidationResult{},
		&commonvalidation.InvalidInferenceResult{},
	}

	for _, result := range results {
		valid, err := evaluateValidationResult(context.Background(), result, 7, "model-a", nil)
		require.NoError(t, err)
		assert.False(t, valid)
	}
}

func TestEvaluateValidationResult_UnknownTypeErrors(t *testing.T) {
	valid, err := evaluateValidationResult(context.Background(), unknownValidationResult{}, 7, "model-a", nil)
	require.Error(t, err)
	assert.False(t, valid)
}

func TestEvaluateValidationResult_ThresholdResolveError(t *testing.T) {
	resolver := stubThresholdResolver{err: errors.New("threshold unavailable")}
	result := &commonvalidation.SimilarityValidationResult{Value: 0.95}

	valid, err := evaluateValidationResult(context.Background(), result, 7, "model-a", resolver)
	require.Error(t, err)
	assert.False(t, valid)
}

func TestLeaseValidator_MarkValidationSubmitted_SetsSubmitted(t *testing.T) {
	store := &stubLeases{
		acquireFn: func(_ context.Context, _ string, _ uint64, _ uint64, _ string) (bool, error) {
			return true, nil
		},
	}
	c := newTestLeaseValidator(store, successInner)

	_, err := c.Validate(context.Background(), makeReq())
	require.NoError(t, err)

	err = c.MarkValidationSubmitted(context.Background(), "escrow-1", 42)
	require.NoError(t, err)
	require.Len(t, store.setResultCalls, 1)
	assert.Contains(t, store.setResultCalls[0], storage.LeaseStatusSubmitted)
}

func TestLeaseValidator_AllowValidationSubmit_TTLExceeded(t *testing.T) {
	store := &stubLeases{
		acquireFn: func(_ context.Context, _ string, _ uint64, _ uint64, _ string) (bool, error) {
			return true, nil
		},
	}
	c := NewLeaseValidator(&stubValidator{fn: successInner}, new(chain.Phase), store, "validator-addr", time.Millisecond)
	_, err := c.Validate(context.Background(), makeReq())
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)

	err = c.AllowValidationSubmit(context.Background(), "escrow-1", 42)
	require.ErrorIs(t, err, devshardpkg.ErrValidationLeaseAbandoned)
	require.ErrorIs(t, err, devshardpkg.ErrValidationLeaseTTLExceeded)
	require.Empty(t, store.setResultCalls)
	require.Empty(t, store.releaseCalls, "AllowValidationSubmit must leave the acquire recorded")

	err = c.ReleaseValidationLease(context.Background(), "escrow-1", 42)
	require.NoError(t, err)
	require.Equal(t, []string{"escrow-1/42/0/validator-addr"}, store.releaseCalls)
}

func TestLeaseValidator_AllowValidationSubmit_NotOwned(t *testing.T) {
	store := &stubLeases{
		acquireFn: func(_ context.Context, _ string, _ uint64, _ uint64, _ string) (bool, error) {
			return true, nil
		},
		ownsFn: func(_ context.Context, _ string, _, _ uint64, _ string) (bool, error) {
			return false, nil
		},
	}
	c := newTestLeaseValidator(store, successInner)
	_, err := c.Validate(context.Background(), makeReq())
	require.NoError(t, err)

	err = c.AllowValidationSubmit(context.Background(), "escrow-1", 42)
	require.ErrorIs(t, err, devshardpkg.ErrValidationLeaseAbandoned)
	require.NotErrorIs(t, err, devshardpkg.ErrValidationLeaseTTLExceeded)
	require.Empty(t, store.releaseCalls, "AllowValidationSubmit must leave the acquire recorded")
}

func TestLeaseValidator_InnerError_Releases(t *testing.T) {
	store := &stubLeases{
		acquireFn: func(_ context.Context, _ string, _ uint64, _ uint64, _ string) (bool, error) {
			return true, nil
		},
	}
	c := newTestLeaseValidator(store, func(_ context.Context, _ devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
		return nil, errors.New("local ml 503")
	})

	result, err := c.Validate(context.Background(), makeReq())
	require.Error(t, err)
	assert.Nil(t, result)
	require.Empty(t, store.setResultCalls)
	require.Equal(t, []string{"escrow-1/42/0/validator-addr"}, store.releaseCalls)

	err = c.ReleaseValidationLease(context.Background(), "escrow-1", 42)
	require.NoError(t, err)
	require.Len(t, store.releaseCalls, 1, "forgotten acquire must not release again")
}

func TestLeaseValidator_Canceled_Releases(t *testing.T) {
	store := &stubLeases{
		acquireFn: func(_ context.Context, _ string, _ uint64, _ uint64, _ string) (bool, error) {
			return true, nil
		},
	}
	c := newTestLeaseValidator(store, func(_ context.Context, _ devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
		return nil, context.Canceled
	})

	result, err := c.Validate(context.Background(), makeReq())
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, result)
	require.Empty(t, store.setResultCalls)
	require.Equal(t, []string{"escrow-1/42/0/validator-addr"}, store.releaseCalls,
		"graceful abort must free the row so a sibling can re-acquire")

	err = c.ReleaseValidationLease(context.Background(), "escrow-1", 42)
	require.NoError(t, err)
	require.Len(t, store.releaseCalls, 1, "forgotten acquire must not release again")
}

func TestLeaseValidator_AlreadyLeased_DoesNotRelease(t *testing.T) {
	store := &stubLeases{
		acquireFn: func(_ context.Context, _ string, _ uint64, _ uint64, _ string) (bool, error) {
			return false, nil
		},
	}
	innerCalls := 0
	c := newTestLeaseValidator(store, func(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
		innerCalls++
		return successInner(ctx, req)
	})

	_, err := c.Validate(context.Background(), makeReq())
	assert.ErrorIs(t, err, devshardpkg.ErrValidationAlreadyLeased)
	assert.Equal(t, 0, innerCalls)
	require.Empty(t, store.releaseCalls)
}

func TestLeaseValidator_ReleaseValidationLease_NoAcquire_NoOp(t *testing.T) {
	store := &stubLeases{
		acquireFn: func(_ context.Context, _ string, _ uint64, _ uint64, _ string) (bool, error) {
			return true, nil
		},
	}
	c := newTestLeaseValidator(store, successInner)

	err := c.ReleaseValidationLease(context.Background(), "escrow-1", 42)
	require.NoError(t, err)
	require.Empty(t, store.releaseCalls)
}

func TestLeaseValidator_ReleaseValidationLease_ErrorForgetsAcquire(t *testing.T) {
	releaseErr := errors.New("release failed")
	store := &stubLeases{
		acquireFn: func(_ context.Context, _ string, _ uint64, _ uint64, _ string) (bool, error) {
			return true, nil
		},
		releaseFn: func(_ context.Context, _ string, _, _ uint64, _ string) error {
			return releaseErr
		},
	}
	c := newTestLeaseValidator(store, successInner)

	_, err := c.Validate(context.Background(), makeReq())
	require.NoError(t, err)

	err = c.ReleaseValidationLease(context.Background(), "escrow-1", 42)
	require.ErrorIs(t, err, releaseErr)
	require.Equal(t, []string{"escrow-1/42/0/validator-addr"}, store.releaseCalls)

	err = c.ReleaseValidationLease(context.Background(), "escrow-1", 42)
	require.NoError(t, err)
	require.Len(t, store.releaseCalls, 1, "release error must still forget the local acquire")
}

type stubChainParams struct{}

func (stubChainParams) LogprobsMode() string { return "" }

func faultReq(epoch uint64) devshardpkg.ValidateRequest {
	req := makeReq()
	req.EpochID = epoch
	req.ExecutorAddress = "executor-1"
	return req
}

func newFaultTestValidator(phaseEpoch uint64, voteFalse bool, fetch payloadFetchFunc, executeML mlExecuteFunc, thresholds ValidationThresholdResolver) *Validator {
	phase := new(chain.Phase)
	phase.Update(phaseEpoch, 0)
	if thresholds == nil {
		thresholds = stubThresholdResolver{threshold: 0.9}
	}
	return &Validator{
		phase:                   phase,
		chainParams:             stubChainParams{},
		thresholds:              thresholds,
		voteFalseOnFetchFailure: voteFalse,
		fetchPayloads:           fetch,
		executeML:               executeML,
	}
}

func taggedFetch(err error) payloadFetchFunc {
	return func(context.Context, devshardpkg.ValidateRequest, string, uint64) ([]byte, []byte, error) {
		return nil, nil, err
	}
}

func TestValidator_Validate_ExecutorFaultClassification(t *testing.T) {
	validPrompt := []byte(`{"messages":[]}`)
	validResponse := []byte(`{"id":"test","object":"chat.completion","choices":[{"index":0,"logprobs":{"content":[{"token":"42","logprob":-0.5,"top_logprobs":[{"token":"42","logprob":-0.5},{"token":"99","logprob":-1.5}]}]}}]}`)
	okFetch := func(context.Context, devshardpkg.ValidateRequest, string, uint64) ([]byte, []byte, error) {
		return validPrompt, validResponse, nil
	}

	tests := []struct {
		name       string
		fetch      payloadFetchFunc
		executeML  mlExecuteFunc
		thresholds ValidationThresholdResolver
		voteFalse  bool
		phaseEpoch uint64
		reqEpoch   uint64
		ctx        context.Context
		wantFalse  bool
		wantSkip   bool
		wantErr    bool
	}{
		{
			name:       "executor 500 votes false",
			fetch:      taggedFetch(tagExecutorPayloadFault(errors.New("executor returned status 500"))),
			voteFalse:  true,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantFalse:  true,
		},
		{
			name:       "connection refused votes false",
			fetch:      taggedFetch(tagExecutorPayloadFault(errors.New("request failed: connection refused"))),
			voteFalse:  true,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantFalse:  true,
		},
		{
			name:       "undecodable body votes false",
			fetch:      taggedFetch(tagExecutorPayloadFault(errors.New("failed to decode response"))),
			voteFalse:  true,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantFalse:  true,
		},
		{
			name:       "invalid signature votes false",
			fetch:      taggedFetch(tagExecutorPayloadFault(errors.New("verify executor signature: bad sig"))),
			voteFalse:  true,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantFalse:  true,
		},
		{
			name:       "prompt hash mismatch votes false",
			fetch:      taggedFetch(tagExecutorPayloadFault(fmt.Errorf("%w: prompt", commonvalidation.ErrHashMismatch))),
			voteFalse:  true,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantFalse:  true,
		},
		{
			name:       "response hash mismatch votes false",
			fetch:      taggedFetch(tagExecutorPayloadFault(fmt.Errorf("%w: response", commonvalidation.ErrHashMismatch))),
			voteFalse:  true,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantFalse:  true,
		},
		{
			name:       "oversized body votes false",
			fetch:      taggedFetch(tagExecutorPayloadFault(fmt.Errorf("%w: over 1024 bytes", commonvalidation.ErrPayloadTooLarge))),
			voteFalse:  true,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantFalse:  true,
		},
		{
			name:       "404 in window votes false",
			fetch:      taggedFetch(fmt.Errorf("payload not found: %w", commonvalidation.ErrPayloadGone)),
			voteFalse:  true,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantFalse:  true,
		},
		{
			name:       "404 out of window skipped",
			fetch:      taggedFetch(fmt.Errorf("payload not found: %w", commonvalidation.ErrPayloadGone)),
			voteFalse:  true,
			phaseEpoch: 11,
			reqEpoch:   10,
			wantSkip:   true,
		},
		{
			name: "malformed prompt votes false",
			fetch: func(context.Context, devshardpkg.ValidateRequest, string, uint64) ([]byte, []byte, error) {
				return []byte("not-json"), validResponse, nil
			},
			voteFalse:  true,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantFalse:  true,
		},
		{
			name: "malformed response votes false",
			fetch: func(context.Context, devshardpkg.ValidateRequest, string, uint64) ([]byte, []byte, error) {
				return validPrompt, []byte("not-json"), nil
			},
			voteFalse:  true,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantFalse:  true,
		},
		{
			name: "malformed prompt with switch off errors",
			fetch: func(context.Context, devshardpkg.ValidateRequest, string, uint64) ([]byte, []byte, error) {
				return []byte("not-json"), validResponse, nil
			},
			voteFalse:  false,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantErr:    true,
		},
		{
			name: "malformed response with switch off errors",
			fetch: func(context.Context, devshardpkg.ValidateRequest, string, uint64) ([]byte, []byte, error) {
				return validPrompt, []byte("not-json"), nil
			},
			voteFalse:  false,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantErr:    true,
		},
		{
			name:  "local ML 503 does not vote",
			fetch: okFetch,
			executeML: func(context.Context, string, string, []byte) (*http.Response, error) {
				return nil, errors.New("ml node 503")
			},
			voteFalse:  true,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantErr:    true,
		},
		{
			name:  "threshold resolve does not vote",
			fetch: okFetch,
			executeML: func(context.Context, string, string, []byte) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusBadRequest, Body: http.NoBody}, nil
			},
			thresholds: stubThresholdResolver{err: errors.New("threshold unavailable")},
			voteFalse:  true,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantErr:    true,
		},
		{
			name:       "untagged pubkey resolve does not vote",
			fetch:      taggedFetch(errors.New("resolve executor pubkeys: chain down")),
			voteFalse:  true,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantErr:    true,
		},
		{
			name:       "cancelled context does not vote",
			fetch:      taggedFetch(tagExecutorPayloadFault(errors.New("executor returned status 500"))),
			voteFalse:  true,
			phaseEpoch: 10,
			reqEpoch:   10,
			ctx:        cancelledCtx(),
			wantErr:    true,
		},
		{
			name:       "switch off 500 does not vote",
			fetch:      taggedFetch(tagExecutorPayloadFault(errors.New("executor returned status 500"))),
			voteFalse:  false,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantErr:    true,
		},
		{
			name:       "switch off 404 skipped",
			fetch:      taggedFetch(fmt.Errorf("payload not found: %w", commonvalidation.ErrPayloadGone)),
			voteFalse:  false,
			phaseEpoch: 10,
			reqEpoch:   10,
			wantSkip:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			v := newFaultTestValidator(tt.phaseEpoch, tt.voteFalse, tt.fetch, tt.executeML, tt.thresholds)
			result, err := v.Validate(ctx, faultReq(tt.reqEpoch))
			switch {
			case tt.wantFalse:
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.False(t, result.Valid)
				assert.Equal(t, executorPayloadUnavailableReason, result.Reason)
			case tt.wantSkip:
				require.ErrorIs(t, err, devshardpkg.ErrValidationSkipped)
				assert.Nil(t, result)
			case tt.wantErr:
				require.Error(t, err)
				assert.Nil(t, result)
				assert.False(t, errors.Is(err, devshardpkg.ErrValidationSkipped))
			default:
				t.Fatal("test case must set wantFalse, wantSkip, or wantErr")
			}
		})
	}
}

func cancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestExecutorFaultVerdict_DisabledOrCancelled(t *testing.T) {
	phase := new(chain.Phase)
	phase.Update(10, 0)
	req := faultReq(10)
	err := tagExecutorPayloadFault(errors.New("executor returned status 500"))

	assert.Nil(t, executorFaultVerdict(context.Background(), phase, req, req.EpochID, err, false))
	assert.Nil(t, executorFaultVerdict(cancelledCtx(), phase, req, req.EpochID, err, true))
	assert.Nil(t, executorFaultVerdict(context.Background(), phase, req, req.EpochID, errors.New("local bridge down"), true))
}

// The D2 window must follow the epoch the payload was actually requested for.
// req.EpochID is zero whenever the caller leaves it unset, and Validate then
// resolves it from the phase.
func TestExecutorFaultVerdict_UnsetRequestEpochUsesResolvedEpoch(t *testing.T) {
	gone := fmt.Errorf("payload not found: %w", commonvalidation.ErrPayloadGone)
	v := newFaultTestValidator(10, true, taggedFetch(gone), nil, nil)

	req := faultReq(0)
	result, err := v.Validate(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Valid)
	assert.Equal(t, executorPayloadUnavailableReason, result.Reason)
}

func TestExecutorFaultVerdict_TooLargeUsesDistinctReason(t *testing.T) {
	err := tagExecutorPayloadFault(fmt.Errorf("%w: over 1024 bytes", commonvalidation.ErrPayloadTooLarge))
	v := newFaultTestValidator(10, true, taggedFetch(err), nil, nil)

	result, verr := v.Validate(context.Background(), faultReq(10))
	require.NoError(t, verr)
	require.NotNil(t, result)
	assert.False(t, result.Valid)
	assert.Equal(t, executorPayloadUnavailableReason, result.Reason)
	assert.Contains(t, fmt.Sprint(result.Details), "payload_too_large")
}

func TestTruncateCause(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "short", truncateCause("short"))

	long := strings.Repeat("x", maxVerdictCauseBytes*2)
	got := truncateCause(long)
	assert.Len(t, got, maxVerdictCauseBytes+len("...(truncated)"))
	assert.True(t, strings.HasSuffix(got, "...(truncated)"))
}
