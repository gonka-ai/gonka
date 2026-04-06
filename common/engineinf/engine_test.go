package engineinf_test

import (
	inference "common/engineinf"
	"common/engineinf/testutil"
	"common/mlnode/gen"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStubChainQuerier_GetInference_Found(t *testing.T) {
	q := testutil.NewStubChainQuerier()
	q.AddInference("inf-1", &inference.Inference{ID: "inf-1", Model: "test-model"})

	got, err := q.GetInference(context.Background(), "inf-1")
	require.NoError(t, err)
	assert.Equal(t, "inf-1", got.ID)
}

func TestStubChainQuerier_GetInference_NotFound(t *testing.T) {
	q := testutil.NewStubChainQuerier()
	_, err := q.GetInference(context.Background(), "missing")
	assert.ErrorIs(t, err, inference.ErrNotFound)
}

func TestStubChainQuerier_GetInferencePayload_Found(t *testing.T) {
	q := testutil.NewStubChainQuerier()
	q.AddPayload("inf-1", 5, &inference.PayloadResponse{
		InferenceID:       "inf-1",
		PromptPayload:     "prompt",
		ResponsePayload:   "response",
		ExecutorSignature: "sig",
	})

	got, err := q.GetInferencePayload(context.Background(), "inf-1", 5)
	require.NoError(t, err)
	assert.Equal(t, "inf-1", got.InferenceID)
}

func TestStubChainQuerier_AdjacentEpochLookup(t *testing.T) {
	q := testutil.NewStubChainQuerier()
	q.AddPayload("inf-1", 5, &inference.PayloadResponse{
		InferenceID:       "inf-1",
		PromptPayload:     "prompt",
		ResponsePayload:   "response",
		ExecutorSignature: "sig",
	})

	// Epoch N-1 should auto-correct to actual epoch N
	got, err := q.GetInferencePayload(context.Background(), "inf-1", 4)
	require.NoError(t, err)
	assert.Equal(t, "inf-1", got.InferenceID)

	// Epoch N+1 should auto-correct to actual epoch N
	got, err = q.GetInferencePayload(context.Background(), "inf-1", 6)
	require.NoError(t, err)
	assert.Equal(t, "inf-1", got.InferenceID)
}

func TestStubChainQuerier_GetInferencePayload_NotFound(t *testing.T) {
	q := testutil.NewStubChainQuerier()
	_, err := q.GetInferencePayload(context.Background(), "missing", 1)
	assert.ErrorIs(t, err, inference.ErrNotFound)
}

func TestStubChainQuerier_GetInferencePayload_EpochFarAway(t *testing.T) {
	q := testutil.NewStubChainQuerier()
	q.AddPayload("inf-1", 5, &inference.PayloadResponse{InferenceID: "inf-1"})

	// Epoch 10 is not within ±1 of stored epoch 5 — should return ErrNotFound.
	_, err := q.GetInferencePayload(context.Background(), "inf-1", 10)
	assert.ErrorIs(t, err, inference.ErrNotFound)
}

func TestStubNodeLock_AcquireRelease(t *testing.T) {
	lock := testutil.NewStubNodeLock("http://ml-node:8080", "lock-abc")

	res, err := lock.Acquire(context.Background(), "llama3", nil)
	require.NoError(t, err)
	assert.Equal(t, "http://ml-node:8080", res.Endpoint)
	assert.Equal(t, "lock-abc", res.LockId)

	err = lock.Release(context.Background(), res.LockId, gen.ReleaseOutcome_SUCCESS)
	require.NoError(t, err)
	assert.True(t, lock.Released())
	assert.Equal(t, gen.ReleaseOutcome_SUCCESS, lock.ReleasedOutcome())
}

func TestValidateTimestamp_TooOld(t *testing.T) {
	old := time.Now().Add(-61 * time.Second).UnixNano()
	err := inference.ValidateTimestamp(old)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too old")
}

func TestValidateTimestamp_JustWithinAge(t *testing.T) {
	recent := time.Now().Add(-59 * time.Second).UnixNano()
	err := inference.ValidateTimestamp(recent)
	require.NoError(t, err)
}

func TestValidateTimestamp_TooFarFuture(t *testing.T) {
	future := time.Now().Add(11 * time.Second).UnixNano()
	err := inference.ValidateTimestamp(future)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "future")
}

func TestValidateTimestamp_JustWithinFuture(t *testing.T) {
	future := time.Now().Add(9 * time.Second).UnixNano()
	err := inference.ValidateTimestamp(future)
	require.NoError(t, err)
}
