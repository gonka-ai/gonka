package validationpool

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWFQ_DeficitRoundRobin(t *testing.T) {
	q := newWFQ()
	for i := uint64(0); i < 6; i++ {
		q.Push(Job{EscrowID: "light", InferenceID: i, Weight: 1})
		q.Push(Job{EscrowID: "heavy", InferenceID: 100 + i, Weight: 3})
	}
	var order []string
	for q.Len() > 0 {
		job, ok := q.Pop()
		require.True(t, ok)
		order = append(order, job.EscrowID)
	}
	// Visit pattern: light(1), heavy(3), light(1), heavy(3), ...
	require.GreaterOrEqual(t, len(order), 8)
	assert.Equal(t, []string{"light", "heavy", "heavy", "heavy", "light", "heavy", "heavy", "heavy"}, order[:8])
	assert.Empty(t, q.order)
	assert.Empty(t, q.buckets)
}

func TestWFQ_PrunesEmptyBuckets(t *testing.T) {
	q := newWFQ()
	q.Push(Job{EscrowID: "gone", InferenceID: 1, Weight: 1})
	q.Push(Job{EscrowID: "stay", InferenceID: 2, Weight: 1})
	q.Push(Job{EscrowID: "stay", InferenceID: 3, Weight: 1})
	require.Equal(t, 2, len(q.buckets))

	job, ok := q.Pop()
	require.True(t, ok)
	assert.Equal(t, "gone", job.EscrowID)
	assert.NotContains(t, q.buckets, "gone")
	assert.Equal(t, []string{"stay"}, q.order)
	assert.Equal(t, 1, len(q.buckets))

	// Re-offer after prune must recreate the bucket.
	q.Push(Job{EscrowID: "gone", InferenceID: 4, Weight: 2})
	assert.Equal(t, 2, len(q.buckets))
	assert.Equal(t, uint32(2), q.buckets["gone"].weight)
}

func TestWFQ_DropEscrowRemovesEmptyBucket(t *testing.T) {
	q := newWFQ()
	q.Push(Job{EscrowID: "e1", InferenceID: 1, Weight: 1})
	_, ok := q.Pop()
	require.True(t, ok)
	// Bucket already pruned by Pop; DropEscrow is a no-op.
	assert.Nil(t, q.DropEscrow("e1"))
	assert.Empty(t, q.buckets)

	q.Push(Job{EscrowID: "e2", InferenceID: 2, Weight: 1})
	q.Push(Job{EscrowID: "e3", InferenceID: 3, Weight: 1})
	dropped := q.DropEscrow("e2")
	require.Len(t, dropped, 1)
	assert.NotContains(t, q.buckets, "e2")
	assert.Equal(t, []string{"e3"}, q.order)
}
