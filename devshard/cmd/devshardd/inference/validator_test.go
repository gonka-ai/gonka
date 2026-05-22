package inference

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"common/chain"
	"common/storage/validationlease"
	commonvalidation "common/validation"
	devshardpkg "devshard"
)

// stubLeases implements leaseOps for testing.
type stubLeases struct {
	acquireFn      func(ctx context.Context, escrowId string, inferenceId uint64, epochId uint64, instanceAddr string) (bool, error)
	setResultFn    func(ctx context.Context, escrowId string, inferenceId uint64, status validationlease.LeaseStatus) error
	setResultCalls []string // records "escrowId/inferenceId/status"
}

func (s *stubLeases) Acquire(ctx context.Context, escrowId string, inferenceId uint64, epochId uint64, instanceAddr string) (bool, error) {
	return s.acquireFn(ctx, escrowId, inferenceId, epochId, instanceAddr)
}

func (s *stubLeases) SetResult(ctx context.Context, escrowId string, inferenceId uint64, status validationlease.LeaseStatus) error {
	s.setResultCalls = append(s.setResultCalls, fmt.Sprintf("%s/%d/%s", escrowId, inferenceId, status))
	if s.setResultFn != nil {
		return s.setResultFn(ctx, escrowId, inferenceId, status)
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
	return NewLeaseValidator(&stubValidator{fn: innerFn}, new(chain.Phase), leases, "validator-addr")
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

func TestLeaseValidator_MarkValidationSubmitted_SetsSubmitted(t *testing.T) {
	store := &stubLeases{
		acquireFn: func(_ context.Context, _ string, _ uint64, _ uint64, _ string) (bool, error) {
			return true, nil
		},
	}
	c := newTestLeaseValidator(store, successInner)

	err := c.MarkValidationSubmitted(context.Background(), "escrow-1", 42)
	require.NoError(t, err)
	require.Len(t, store.setResultCalls, 1)
	assert.Contains(t, store.setResultCalls[0], validationlease.StatusSubmitted)
}
