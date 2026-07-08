package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestFinalizeUnderLockBoundsHoldAndReleasesMutex proves a hung finalize protocol
// cannot hold the process-wide finalizeMu forever: the call is cut off by
// rotationFinalizeTimeout and the mutex is released, so other /v1/finalize
// requests are not blocked indefinitely.
func TestFinalizeUnderLockBoundsHoldAndReleasesMutex(t *testing.T) {
	old := rotationFinalizeTimeout
	rotationFinalizeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { rotationFinalizeTimeout = old })

	g := &Gateway{}

	// A finalize that hangs until its context is cancelled (a stuck protocol).
	hang := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}

	start := time.Now()
	err := g.finalizeUnderLock(context.Background(), hang)
	require.Error(t, err, "a hung finalize must be cut off by the timeout")
	require.Less(t, time.Since(start), 2*time.Second, "the lock hold must be bounded, not indefinite")

	// The mutex must have been released: a subsequent finalize proceeds promptly.
	done := make(chan error, 1)
	go func() {
		done <- g.finalizeUnderLock(context.Background(), func(context.Context) error { return nil })
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("finalizeMu was not released after a bounded finalize; it would block every /v1/finalize")
	}
}

// TestFinalizeUnderLockPassesBoundedContextAndResult confirms the happy path:
// the finalize receives a deadline-bounded context and its result propagates.
func TestFinalizeUnderLockPassesBoundedContextAndResult(t *testing.T) {
	g := &Gateway{}
	var gotDeadline bool
	err := g.finalizeUnderLock(context.Background(), func(ctx context.Context) error {
		_, gotDeadline = ctx.Deadline()
		return nil
	})
	require.NoError(t, err)
	require.True(t, gotDeadline, "finalize must receive a deadline-bounded context")
}
