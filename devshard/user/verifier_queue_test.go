package user

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVerifierHostQueue_SnapshotShowsBeginInflight(t *testing.T) {
	saved := MaxConcurrentVerifierRPCs
	MaxConcurrentVerifierRPCs = 1
	t.Cleanup(func() { MaxConcurrentVerifierRPCs = saved })

	q := newVerifierHostQueue()
	addr := "verifier-a"
	require.NoError(t, q.acquire(context.Background(), addr))
	t.Cleanup(func() { q.release(addr) })

	sentAt := time.Now().Add(-2 * time.Second)
	rec := inflightVerify{
		RequestID: "req-holder",
		Escrow:    "escrow-1",
		Nonce:     12707,
		Reason:    "execution",
		SentAt:    sentAt,
	}
	q.beginInflight(addr, rec)

	inflight, waiters := q.snapshot(addr)
	require.Zero(t, waiters)
	require.Len(t, inflight, 1)
	require.Equal(t, uint64(12707), inflight[0].Nonce)
	require.Equal(t, "escrow-1", inflight[0].Escrow)
	require.Equal(t, "req-holder", inflight[0].RequestID)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	waiting := make(chan struct{})
	go func() {
		close(waiting)
		_ = q.acquire(ctx, addr)
	}()
	require.Eventually(t, func() bool {
		_, w := q.snapshot(addr)
		return w >= 1
	}, time.Second, time.Millisecond, "waiter should appear in snapshot while blocked on the semaphore")

	q.endInflight(addr, rec)
	inflight, _ = q.snapshot(addr)
	require.Empty(t, inflight)
}

func TestFormatInflightSnapshot_OldestFirstCappedAtEight(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	recs := make([]inflightVerify, 0, 10)
	for i := range 10 {
		recs = append(recs, inflightVerify{
			RequestID: "req",
			Escrow:    "e",
			Nonce:     uint64(10 - i),
			Reason:    "execution",
			SentAt:    now.Add(-time.Duration(i+1) * time.Second),
		})
	}
	got := formatInflightSnapshot(recs, now)
	require.Contains(t, got, "nonce=1 ")
	require.Contains(t, got, "nonce=8 ")
	require.NotContains(t, got, "nonce=9 ")
	require.NotContains(t, got, "nonce=10 ")
	require.True(t, strings.HasPrefix(got, "request=req escrow=e nonce=1 reason=execution sent_age_ms=10000;"))
}

func TestFormatErrorClasses_Sorted(t *testing.T) {
	require.Empty(t, formatErrorClasses(nil))
	require.Equal(t, "verifier_queue_expired:4,verifier_rpc_timeout:2", formatErrorClasses(map[string]int{
		VoteErrorRPCTimeout:   2,
		VoteErrorQueueExpired: 4,
	}))
}
