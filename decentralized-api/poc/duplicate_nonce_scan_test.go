package poc

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"decentralized-api/cosmosclient"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// errorFetcher implements proofFetcher and always fails with a fixed error.
type errorFetcher struct {
	err error
}

func (f *errorFetcher) FetchAndVerifyProofs(_ context.Context, _ string, _ ProofRequest) ([]VerifiedArtifact, error) {
	return nil, f.err
}

// newManualScanner returns a scanner without background workers so tests can
// drive nextChunk/runChunk deterministically.
func newManualScanner(coordinator *PoCValidationCoordinator) *duplicateNonceScanner {
	return &duplicateNonceScanner{
		coordinator: coordinator,
		inFlightURL: make(map[string]bool),
		nextURL:     make(map[string]time.Time),
		now:         time.Now,
	}
}

func TestSampleDuplicateScanIndices_BoundsAndNoDuplicates(t *testing.T) {
	indices, err := sampleDuplicateScanIndices(10_000)
	require.NoError(t, err)
	require.Len(t, indices, duplicateScanSampleSize)

	seen := make(map[uint32]bool, len(indices))
	for _, idx := range indices {
		require.Less(t, idx, uint32(10_000))
		require.False(t, seen[idx], "duplicate index: %d", idx)
		seen[idx] = true
	}
}

func TestSampleDuplicateScanIndices_UsesRandomOrderForFullSample(t *testing.T) {
	indices, err := sampleDuplicateScanIndices(20)
	require.NoError(t, err)
	require.Len(t, indices, 20)

	sorted := true
	for i, idx := range indices {
		if idx != uint32(i) {
			sorted = false
			break
		}
	}
	require.False(t, sorted, "duplicate scan chunks must not be based on sorted indexes")
}

func TestPoCValidationCoordinator_ReleaseDueRequiresScanOK(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("SubmitPocValidationsV2", mock.MatchedBy(func(msg *types.MsgSubmitPocValidationsV2) bool {
		return validationWeight(msg) == -1
	})).Return(nil).Once()

	coordinator := NewPoCValidationCoordinator(recorder, nil)
	require.NoError(t, coordinator.HandleValidationResult(100, "participant-a", "model-a", 10))

	recorder.AssertExpectations(t)
}

func TestPoCValidationCoordinator_ReleasesValidWhenScanOK(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("SubmitPocValidationsV2", mock.MatchedBy(func(msg *types.MsgSubmitPocValidationsV2) bool {
		return validationWeight(msg) == 10
	})).Return(nil).Once()

	coordinator := NewPoCValidationCoordinator(recorder, nil)
	key := pocValidationKey{pocHeight: 100, participant: "participant-a", modelID: "model-a"}
	coordinator.scans[key] = &duplicateScanState{status: duplicateScanOK}

	require.NoError(t, coordinator.HandleValidationResult(100, "participant-a", "model-a", 10))
	recorder.AssertExpectations(t)
}

func TestPoCValidationCoordinator_DuplicateAcrossChunksSubmitsInvalid(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("SubmitPocValidationsV2", mock.MatchedBy(func(msg *types.MsgSubmitPocValidationsV2) bool {
		return validationWeight(msg) == -1
	})).Return(nil).Once()

	coordinator := NewPoCValidationCoordinator(recorder, nil)
	key := pocValidationKey{pocHeight: 100, participant: "participant-a", modelID: "model-a"}
	coordinator.scans[key] = &duplicateScanState{
		status:    duplicateScanPending,
		seen:      make(map[int32]uint32),
		remaining: 3,
	}

	coordinator.recordScanArtifacts(key, []VerifiedArtifact{{LeafIndex: 1, Nonce: 7}})
	recorder.AssertNumberOfCalls(t, "SubmitPocValidationsV2", 0)

	coordinator.recordScanArtifacts(key, []VerifiedArtifact{{LeafIndex: 9, Nonce: 7}})
	recorder.AssertExpectations(t)
	require.Equal(t, duplicateScanFailed, coordinator.scans[key].status)
}

func TestDuplicateNonceScanner_PermanentErrorMarksScanFailed(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}
	coordinator := NewPoCValidationCoordinator(recorder, nil)
	key := pocValidationKey{pocHeight: 100, participant: "participant-a", modelID: "model-a"}
	coordinator.scans[key] = &duplicateScanState{status: duplicateScanPending, seen: make(map[int32]uint32), remaining: 1}

	scanner := newManualScanner(coordinator)
	scanner.runChunk(duplicateScanChunk{
		fetcher: &errorFetcher{err: fmt.Errorf("%w: leaf 3", ErrProofVerificationFailed)},
		job:     DuplicateScanJob{PocHeight: 100, Participant: "participant-a", ModelID: "model-a", ParticipantURL: "http://a"},
		key:     key,
		indices: []uint32{0},
	})

	require.Empty(t, scanner.queue, "permanent proof errors must not be retried")
	require.Equal(t, duplicateScanFailed, coordinator.scans[key].status)

	// A later positive ML validation result must be converted to invalid.
	recorder.On("SubmitPocValidationsV2", mock.MatchedBy(func(msg *types.MsgSubmitPocValidationsV2) bool {
		return validationWeight(msg) == -1
	})).Return(nil).Once()
	require.NoError(t, coordinator.HandleValidationResult(100, "participant-a", "model-a", 10))
	recorder.AssertExpectations(t)
}

func TestDuplicateNonceScanner_TransientErrorRequeuesOnlyWhilePending(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}
	coordinator := NewPoCValidationCoordinator(recorder, nil)
	key := pocValidationKey{pocHeight: 100, participant: "participant-a", modelID: "model-a"}
	coordinator.scans[key] = &duplicateScanState{status: duplicateScanPending, seen: make(map[int32]uint32), remaining: 1}

	scanner := newManualScanner(coordinator)
	chunk := duplicateScanChunk{
		fetcher: &errorFetcher{err: errors.New("HTTP request failed: connection refused")},
		job:     DuplicateScanJob{PocHeight: 100, Participant: "participant-a", ModelID: "model-a", ParticipantURL: "http://a"},
		key:     key,
		indices: []uint32{0},
	}

	scanner.runChunk(chunk)
	require.Len(t, scanner.queue, 1, "transient errors must be requeued while the scan is pending")

	scanner.queue = nil
	coordinator.scans[key].status = duplicateScanOK
	scanner.runChunk(chunk)
	require.Empty(t, scanner.queue, "decided scans must not be requeued")
}

func TestDuplicateNonceScanner_DropsChunksForDecidedScans(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}
	coordinator := NewPoCValidationCoordinator(recorder, nil)

	pendingKey := pocValidationKey{pocHeight: 100, participant: "participant-pending", modelID: "model-a"}
	decidedKey := pocValidationKey{pocHeight: 100, participant: "participant-decided", modelID: "model-a"}
	prunedKey := pocValidationKey{pocHeight: 100, participant: "participant-pruned", modelID: "model-a"}

	coordinator.scans[pendingKey] = &duplicateScanState{status: duplicateScanPending}
	coordinator.scans[decidedKey] = &duplicateScanState{status: duplicateScanFailed}
	// prunedKey intentionally has no scan state (simulates stage pruning).

	scanner := newManualScanner(coordinator)
	scanner.enqueue([]duplicateScanChunk{
		{key: decidedKey, job: DuplicateScanJob{ParticipantURL: "http://decided"}, indices: []uint32{0}},
		{key: prunedKey, job: DuplicateScanJob{ParticipantURL: "http://pruned"}, indices: []uint32{0}},
		{key: pendingKey, job: DuplicateScanJob{ParticipantURL: "http://pending"}, indices: []uint32{0}},
	})

	chunk, ok := scanner.nextChunk()
	require.True(t, ok)
	require.Equal(t, pendingKey, chunk.key, "only the still-pending scan chunk must be dispatched")
	require.Empty(t, scanner.queue, "decided and pruned chunks must be dropped, not retried")
}

func TestDuplicateNonceScanner_URLCooldown(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}
	coordinator := NewPoCValidationCoordinator(recorder, nil)
	key := pocValidationKey{pocHeight: 100, participant: "participant-a", modelID: "model-a"}
	coordinator.scans[key] = &duplicateScanState{status: duplicateScanPending}

	now := time.Now()
	scanner := newManualScanner(coordinator)
	scanner.now = func() time.Time { return now }
	scanner.enqueue([]duplicateScanChunk{
		{key: key, job: DuplicateScanJob{ParticipantURL: "http://a"}, indices: []uint32{0}},
		{key: key, job: DuplicateScanJob{ParticipantURL: "http://a"}, indices: []uint32{1}},
	})

	_, ok := scanner.nextChunk()
	require.True(t, ok)

	// Same URL is in flight: nothing to dispatch.
	_, ok = scanner.nextChunk()
	require.False(t, ok)

	// Request finished but the 5s cooldown has not elapsed yet.
	scanner.mu.Lock()
	delete(scanner.inFlightURL, "http://a")
	scanner.mu.Unlock()
	_, ok = scanner.nextChunk()
	require.False(t, ok)

	now = now.Add(duplicateScanURLCooldown)
	chunk, ok := scanner.nextChunk()
	require.True(t, ok)
	require.Equal(t, []uint32{1}, chunk.indices)
}

func TestIsRetryableDuplicateScanError(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"proof verification failed", fmt.Errorf("%w: leaf 3", ErrProofVerificationFailed), false},
		{"incomplete coverage", fmt.Errorf("%w: expected 5 proofs, got 4", ErrIncompleteCoverage), false},
		{"invalid vector data", fmt.Errorf("%w: leaf 1: NaN", ErrInvalidVectorData), false},
		{"http 400", errors.New("proof request failed with status 400: root_hash does not match"), false},
		{"http 404", errors.New("proof request failed with status 404: not found"), false},
		{"http 429", errors.New("proof request failed with status 429: rate limited"), true},
		{"http 503", errors.New("proof request failed with status 503: unavailable"), true},
		{"transport error", errors.New("HTTP request failed: connection refused"), true},
		{"unknown error", errors.New("failed to decode response: unexpected EOF"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.retryable, isRetryableDuplicateScanError(tc.err))
		})
	}
}

func validationWeight(msg *types.MsgSubmitPocValidationsV2) int64 {
	if msg == nil || len(msg.Validations) != 1 {
		return 0
	}
	return msg.Validations[0].ValidatedWeight
}
