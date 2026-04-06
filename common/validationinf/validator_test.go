package validationinf

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubClaimStore is a minimal in-memory claimStore for unit tests.
type stubClaimStore struct {
	claimWon             bool
	claimErr             error
	reclaimStaleID       string
	reclaimStaleErr      error
	setTxHashCalled      bool
	setTxHashInferenceID string
	setTxHashErr         error
	deleteClaimsCalled   bool
	deleteClaimsEpochID  uint64
	deleteClaimsErr      error
}

func (s *stubClaimStore) Claim(_ context.Context, _ string, _ uint64, _ string) (bool, error) {
	return s.claimWon, s.claimErr
}

func (s *stubClaimStore) ReclaimOneStale(_ context.Context, _ string, _ string) (string, error) {
	id := s.reclaimStaleID
	s.reclaimStaleID = "" // return empty on next call so loop terminates
	return id, s.reclaimStaleErr
}

func (s *stubClaimStore) SetTxHash(_ context.Context, inferenceID, _ string) error {
	s.setTxHashCalled = true
	s.setTxHashInferenceID = inferenceID
	return s.setTxHashErr
}

func (s *stubClaimStore) DeleteClaimsByEpoch(_ context.Context, beforeEpochID uint64) error {
	s.deleteClaimsCalled = true
	s.deleteClaimsEpochID = beforeEpochID
	return s.deleteClaimsErr
}

// stubChainBridge is a minimal chainBridge for unit tests.
type stubChainBridge struct{}

func (s *stubChainBridge) GetEpochSeed(_ context.Context, _ uint64) ([]byte, error) {
	return nil, nil
}

func (s *stubChainBridge) ReportValidation(_ context.Context, _ string, _ float64) error {
	return nil
}

func newTestValidator(store *stubClaimStore) *Validator {
	return New(
		Config{InstanceAddress: "test-addr"},
		store,
		&stubChainBridge{},
	)
}

// TestValidator_HandleInferenceFinished_ClaimWon verifies that when the claim is won,
// runValidation is called and SetTxHash is recorded with the correct inferenceID.
func TestValidator_HandleInferenceFinished_ClaimWon(t *testing.T) {
	store := &stubClaimStore{claimWon: true}
	v := newTestValidator(store)

	v.HandleInferenceFinished(context.Background(), "inf-001", 5)

	require.True(t, store.setTxHashCalled, "SetTxHash should have been called")
	assert.Equal(t, "inf-001", store.setTxHashInferenceID)
}

// TestValidator_HandleInferenceFinished_ClaimLost verifies that when the claim is lost,
// SetTxHash is NOT called (another instance owns the work).
func TestValidator_HandleInferenceFinished_ClaimLost(t *testing.T) {
	store := &stubClaimStore{claimWon: false}
	v := newTestValidator(store)

	v.HandleInferenceFinished(context.Background(), "inf-002", 5)

	assert.False(t, store.setTxHashCalled, "SetTxHash must not be called when claim is lost")
}

// TestValidator_HandleEpochChanged_Cleanup verifies that DeleteClaimsByEpoch is called
// with newEpochID-2 when the epoch advances far enough.
func TestValidator_HandleEpochChanged_Cleanup(t *testing.T) {
	store := &stubClaimStore{}
	v := newTestValidator(store)

	v.HandleEpochChanged(context.Background(), 10)

	require.True(t, store.deleteClaimsCalled, "DeleteClaimsByEpoch should have been called")
	assert.Equal(t, uint64(8), store.deleteClaimsEpochID)
}

// TestValidator_HandleEpochChanged_TooEarlyEpoch verifies that DeleteClaimsByEpoch is NOT
// called when newEpochID is less than 2 (would underflow or be meaningless).
func TestValidator_HandleEpochChanged_TooEarlyEpoch(t *testing.T) {
	for _, epoch := range []uint64{0, 1} {
		store := &stubClaimStore{}
		v := newTestValidator(store)

		v.HandleEpochChanged(context.Background(), epoch)

		assert.False(t, store.deleteClaimsCalled, "DeleteClaimsByEpoch must not be called for epoch %d", epoch)
	}
}
