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
	"devshard/storage"
	"devshard/transport"
	"devshard/types"
)

// --- stubs ---

type stubStaleLeaseStore struct {
	acquireFn      func(ctx context.Context, escrowId, instanceAddr string, ttl time.Duration) (uint64, uint64, error)
	setResultFn    func(ctx context.Context, escrowId string, inferenceId uint64, status storage.LeaseStatus, instanceAddr string) error
	ownsFn         func(ctx context.Context, escrowId string, inferenceId uint64, instanceAddr string) (bool, error)
	setResultCalls []string
}

func (s *stubStaleLeaseStore) AcquireOneStale(ctx context.Context, escrowId, instanceAddr string, ttl time.Duration) (uint64, uint64, error) {
	return s.acquireFn(ctx, escrowId, instanceAddr, ttl)
}

func (s *stubStaleLeaseStore) SetResult(ctx context.Context, escrowId string, inferenceId uint64, status storage.LeaseStatus, instanceAddr string) error {
	s.setResultCalls = append(s.setResultCalls, fmt.Sprintf("%s/%d/%s", escrowId, inferenceId, status))
	if s.setResultFn != nil {
		return s.setResultFn(ctx, escrowId, inferenceId, status, instanceAddr)
	}
	return nil
}

func (s *stubStaleLeaseStore) OwnsPendingLease(ctx context.Context, escrowId string, inferenceId uint64, instanceAddr string) (bool, error) {
	if s.ownsFn != nil {
		return s.ownsFn(ctx, escrowId, inferenceId, instanceAddr)
	}
	return true, nil
}


type stubSessionManager struct {
	ids []string
}

func (s *stubSessionManager) ActiveEscrowIDs() []string { return s.ids }
func (s *stubSessionManager) existingServer(_ string) (*transport.Server, bool) {
	return nil, false
}

type stubHostSnap struct {
	state types.EscrowState
	group []types.SlotAssignment
}

func (s *stubHostSnap) SnapshotState() types.EscrowState { return s.state }
func (s *stubHostSnap) Group() []types.SlotAssignment    { return s.group }

type stubEngine struct {
	validateFn func(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error)
}

func (s *stubEngine) Validate(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	return s.validateFn(ctx, req)
}

// --- buildValidateRequest tests ---

func TestBuildValidateRequest_InferenceNotFound(t *testing.T) {
	h := &stubHostSnap{
		state: types.EscrowState{Inferences: map[uint64]*types.InferenceRecord{}},
	}
	_, ok := buildValidateRequest(h, "escrow-1", 99, 0)
	assert.False(t, ok)
}

func TestBuildValidateRequest_WrongStatus(t *testing.T) {
	h := &stubHostSnap{
		state: types.EscrowState{
			Inferences: map[uint64]*types.InferenceRecord{
				10: {Status: types.StatusPending},
			},
		},
	}
	_, ok := buildValidateRequest(h, "escrow-1", 10, 5)
	assert.False(t, ok)
}

func TestBuildValidateRequest_HappyPath(t *testing.T) {
	h := &stubHostSnap{
		state: types.EscrowState{
			Inferences: map[uint64]*types.InferenceRecord{
				7: {
					Status:       types.StatusFinished,
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
	req, ok := buildValidateRequest(h, "escrow-1", 7, 3)
	require.True(t, ok)
	assert.Equal(t, uint64(7), req.InferenceID)
	assert.Equal(t, "escrow-1", req.EscrowID)
	assert.Equal(t, "llama-3", req.Model)
	assert.Equal(t, []byte("ph"), req.PromptHash)
	assert.Equal(t, []byte("rh"), req.ResponseHash)
	assert.Equal(t, uint64(100), req.InputTokens)
	assert.Equal(t, uint64(50), req.OutputTokens)
	assert.Equal(t, "executor-addr", req.ExecutorAddress)
}

// --- retryForEscrow tests ---

// retryLoopWithCustomRetryOne creates a RetryLoop that overrides retryOne via a
// closure captured in the lease store, so we can test the loop logic without
// needing a real transport.Server.
//
// We test retryForEscrow by observing how many times AcquireOneStale is called
// and whether it terminates correctly.

func TestRetryForEscrow_NoStaleLeases(t *testing.T) {
	calls := 0
	leases := &stubStaleLeaseStore{
		acquireFn: func(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
			calls++
			return 0, 0, nil // no stale leases
		},
	}
	rl := &RetryLoop{
		leases:       leases,
		manager:      &stubSessionManager{},
		instanceAddr: "addr",
		leaseTTL:     DefaultLeaseTTL,
		interval:     DefaultRetryInterval,
	}
	rl.retryForEscrow(context.Background(), "escrow-1")
	assert.Equal(t, 1, calls, "should call AcquireOneStale once and stop")
}

func TestRetryForEscrow_AcquireError_Stops(t *testing.T) {
	calls := 0
	leases := &stubStaleLeaseStore{
		acquireFn: func(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
			calls++
			return 0, 0, errors.New("db error")
		},
	}
	rl := &RetryLoop{
		leases:       leases,
		manager:      &stubSessionManager{},
		instanceAddr: "addr",
		leaseTTL:     DefaultLeaseTTL,
	}
	rl.retryForEscrow(context.Background(), "escrow-1")
	assert.Equal(t, 1, calls, "should stop after first error")
}

func TestRetryForEscrow_LeaseFromPreviousEpochIsSkipped(t *testing.T) {
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
	rl := &RetryLoop{
		leases:       leases,
		manager:      &stubSessionManager{},
		phase:        phase,
		instanceAddr: "addr",
		leaseTTL:     DefaultLeaseTTL,
	}

	rl.retryForEscrow(context.Background(), "escrow-1")

	assert.Equal(t, 2, callCount)
	require.Len(t, leases.setResultCalls, 1)
	assert.Equal(t, "escrow-1/1/skipped", leases.setResultCalls[0])
}

func TestRetryForEscrow_SessionNotLoaded_LogsAndContinues(t *testing.T) {
	// First call returns a stale inference; second returns empty (done).
	callCount := 0
	leases := &stubStaleLeaseStore{
		acquireFn: func(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
			callCount++
			if callCount == 1 {
				return 1, 0, nil
			}
			return 0, 0, nil
		},
	}
	// existingServer returns false — session not loaded.
	mgr := &stubSessionManager{}
	rl := &RetryLoop{
		leases:       leases,
		manager:      mgr,
		instanceAddr: "addr",
		leaseTTL:     DefaultLeaseTTL,
	}
	// Should not panic; logs the error and tries next iteration.
	rl.retryForEscrow(context.Background(), "escrow-1")
	// callCount should be 2: first acquire returned 1 (retryOne failed), second returned 0.
	assert.Equal(t, 2, callCount)
}
