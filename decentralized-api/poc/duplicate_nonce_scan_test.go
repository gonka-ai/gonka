package poc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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

type fixedFetcher struct {
	artifacts []VerifiedArtifact
}

func (f *fixedFetcher) FetchAndVerifyProofs(_ context.Context, _ string, _ ProofRequest) ([]VerifiedArtifact, error) {
	return f.artifacts, nil
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

func TestDuplicateScanWorkerCount(t *testing.T) {
	require.Equal(t, 20, duplicateScanWorkers)
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

func TestDuplicateNonceScanner_TransientErrorMovesParticipantChunksToBack(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}
	coordinator := NewPoCValidationCoordinator(recorder, nil)
	keyA := pocValidationKey{pocHeight: 100, participant: "participant-a", modelID: "model-a"}
	keyB := pocValidationKey{pocHeight: 100, participant: "participant-b", modelID: "model-a"}
	coordinator.scans[keyA] = &duplicateScanState{status: duplicateScanPending, seen: make(map[int32]uint32), remaining: 3}
	coordinator.scans[keyB] = &duplicateScanState{status: duplicateScanPending, seen: make(map[int32]uint32), remaining: 1}

	scanner := newManualScanner(coordinator)
	failedA := duplicateScanChunk{
		fetcher: &errorFetcher{err: errors.New("HTTP request failed: connection refused")},
		job:     DuplicateScanJob{PocHeight: 100, Participant: "participant-a", ModelID: "model-a", ParticipantURL: "http://a"},
		key:     keyA,
		indices: []uint32{0},
	}
	scanner.enqueue([]duplicateScanChunk{
		{key: keyA, job: DuplicateScanJob{ParticipantURL: "http://a"}, indices: []uint32{1}},
		{key: keyB, job: DuplicateScanJob{ParticipantURL: "http://b"}, indices: []uint32{0}},
		{key: keyA, job: DuplicateScanJob{ParticipantURL: "http://a"}, indices: []uint32{2}},
	})

	scanner.runChunk(failedA)

	require.Len(t, scanner.queue, 4)
	require.Equal(t, keyB, scanner.queue[0].key, "other participants must stay ahead of the retrying participant")
	require.Equal(t, []uint32{1}, scanner.queue[1].indices)
	require.Equal(t, []uint32{2}, scanner.queue[2].indices)
	require.Equal(t, []uint32{0}, scanner.queue[3].indices, "failed chunk must be appended after the participant's queued chunks")
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
	coordinator.scans[key] = &duplicateScanState{status: duplicateScanPending, seen: make(map[int32]uint32), remaining: 2}

	now := time.Now()
	scanner := newManualScanner(coordinator)
	scanner.now = func() time.Time { return now }
	scanner.enqueue([]duplicateScanChunk{
		{
			fetcher: &fixedFetcher{artifacts: []VerifiedArtifact{{LeafIndex: 0, Nonce: 10}}},
			key:     key,
			job:     DuplicateScanJob{ParticipantURL: "http://a"},
			indices: []uint32{0},
		},
		{key: key, job: DuplicateScanJob{ParticipantURL: "http://a"}, indices: []uint32{1}},
	})

	first, ok := scanner.nextChunk()
	require.True(t, ok)
	require.True(t, scanner.nextURL["http://a"].IsZero(), "dispatch must not start the cooldown")

	// Same URL is in flight: nothing to dispatch.
	_, ok = scanner.nextChunk()
	require.False(t, ok)

	// Request completion starts the 5s cooldown.
	scanner.runChunk(first)
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

// flakyFetcher alternates between retryable failures and successes so the
// stress test exercises both the requeue path and the record path.
type flakyFetcher struct {
	calls atomic.Int64
}

func (f *flakyFetcher) FetchAndVerifyProofs(_ context.Context, _ string, req ProofRequest) ([]VerifiedArtifact, error) {
	n := f.calls.Add(1)
	if n%2 == 0 {
		return nil, errors.New("HTTP request failed: connection refused")
	}
	artifacts := make([]VerifiedArtifact, len(req.LeafIndices))
	for i, leaf := range req.LeafIndices {
		artifacts[i] = VerifiedArtifact{LeafIndex: leaf, Nonce: int32(leaf)}
	}
	return artifacts, nil
}

// TestPoCValidationCoordinator_ConcurrentStress drives the real worker pool,
// coordinator submissions, and queue pruning concurrently. It exists to catch
// deadlocks between the scanner mutex and the coordinator mutex (the scanner
// takes s.mu then c.mu via isScanPending; coordinator paths must release c.mu
// before touching the scanner queue) and data races under -race.
func TestPoCValidationCoordinator_ConcurrentStress(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("SubmitPocValidationsV2", mock.Anything).Return(nil).Maybe()

	coordinator := NewPoCValidationCoordinator(recorder, nil)
	fetcher := &flakyFetcher{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for p := 0; p < 8; p++ {
			participant := fmt.Sprintf("participant-%d", p)
			wg.Add(1)
			go func() {
				defer wg.Done()
				coordinator.StartDuplicateScan(fetcher, DuplicateScanJob{
					PocHeight:      100,
					Participant:    participant,
					ModelID:        "model-a",
					ParticipantURL: "http://" + participant,
					Count:          uint32(duplicateScanChunkSize * 3),
				})
				// Exercise submitAndMark -> pruneQueue while workers are scanning.
				_ = coordinator.HandleValidationResult(100, participant, "model-a", 10)
				coordinator.ReleaseDue()
			}()
		}
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock: concurrent scanner/coordinator operations did not finish")
	}
}

func validationWeight(msg *types.MsgSubmitPocValidationsV2) int64 {
	if msg == nil || len(msg.Validations) != 1 {
		return 0
	}
	return msg.Validations[0].ValidatedWeight
}
