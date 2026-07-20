package inference

import (
	"context"
	"errors"
	"fmt"
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
	setResultFn    func(ctx context.Context, escrowId string, inferenceId uint64, status storage.LeaseStatus, instanceAddr string) error
	ownsFn         func(ctx context.Context, escrowId string, inferenceId uint64, instanceAddr string) (bool, error)
	setResultCalls []string // records "escrowId/inferenceId/status"
}

func (s *stubLeases) Acquire(ctx context.Context, escrowId string, inferenceId uint64, epochId uint64, instanceAddr string) (bool, error) {
	return s.acquireFn(ctx, escrowId, inferenceId, epochId, instanceAddr)
}

func (s *stubLeases) SetResult(ctx context.Context, escrowId string, inferenceId uint64, status storage.LeaseStatus, instanceAddr string) error {
	s.setResultCalls = append(s.setResultCalls, fmt.Sprintf("%s/%d/%s", escrowId, inferenceId, status))
	if s.setResultFn != nil {
		return s.setResultFn(ctx, escrowId, inferenceId, status, instanceAddr)
	}
	return nil
}

func (s *stubLeases) OwnsPendingLease(ctx context.Context, escrowId string, inferenceId uint64, instanceAddr string) (bool, error) {
	if s.ownsFn != nil {
		return s.ownsFn(ctx, escrowId, inferenceId, instanceAddr)
	}
	return true, nil
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

// hashMismatchInner returns an error wrapping ErrHashMismatch.
func hashMismatchInner(_ context.Context, _ devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	return nil, errors.Join(commonvalidation.ErrHashMismatch, errors.New("prompt expected abc got def"))
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

// TestLeaseValidator_HashMismatch_ReturnsInvalidWithoutMarkingSubmitted verifies that when
// the inner function returns a wrapped ErrHashMismatch, Validate returns
// {Valid:false} (no error) but leaves lease completion to the async submitter.
func TestLeaseValidator_HashMismatch_ReturnsInvalidWithoutMarkingSubmitted(t *testing.T) {
	store := &stubLeases{
		acquireFn: func(_ context.Context, _ string, _ uint64, _ uint64, _ string) (bool, error) {
			return true, nil
		},
	}
	c := newTestLeaseValidator(store, hashMismatchInner)

	result, err := c.Validate(context.Background(), makeReq())
	require.NoError(t, err)
	assert.False(t, result.Valid)
	require.Empty(t, store.setResultCalls)
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

func (unknownValidationResult) IsSuccessful() bool                     { return true }
func (unknownValidationResult) GetInferenceId() string               { return "unknown" }
func (unknownValidationResult) GetValidationResponseBytes() []byte   { return nil }

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
	require.Empty(t, store.setResultCalls)
}

func TestLeaseValidator_AllowValidationSubmit_NotOwned(t *testing.T) {
	store := &stubLeases{
		acquireFn: func(_ context.Context, _ string, _ uint64, _ uint64, _ string) (bool, error) {
			return true, nil
		},
		ownsFn: func(_ context.Context, _ string, _ uint64, _ string) (bool, error) {
			return false, nil
		},
	}
	c := newTestLeaseValidator(store, successInner)
	_, err := c.Validate(context.Background(), makeReq())
	require.NoError(t, err)

	err = c.AllowValidationSubmit(context.Background(), "escrow-1", 42)
	require.ErrorIs(t, err, devshardpkg.ErrValidationLeaseAbandoned)
}
