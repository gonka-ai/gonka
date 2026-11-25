package registry

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/types"
	"devshard/user"
)

type gatedVoter struct {
	acceptingVoter
	entered     chan struct{}
	enteredOnce *sync.Once
	release     chan struct{}
}

func (v gatedVoter) VerifyTimeout(ctx context.Context, inferenceID uint64, reason types.TimeoutReason, payload *host.InferencePayload, diffs []types.Diff, artifacts host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	v.enteredOnce.Do(func() { close(v.entered) })
	<-v.release
	return v.acceptingVoter.VerifyTimeout(ctx, inferenceID, reason, payload, diffs, artifacts)
}

type waitSignallingContext struct {
	context.Context
	waited     chan struct{}
	waitedOnce *sync.Once
}

func (c waitSignallingContext) Done() <-chan struct{} {
	c.waitedOnce.Do(func() { close(c.waited) })
	return c.Context.Done()
}

func overdueInsideGrace(grace time.Duration) int64 {
	window := time.Duration(testutil.DefaultConfig(3).ExecutionTimeout)*time.Second + user.TimeoutBuffer
	return time.Now().Add(-window - grace/4).Unix()
}

type sweepResult struct {
	counts [3]int
	err    error
}

// Test flow:
//  1. Publish a real escrow session whose host votes block until released, with one record confirmed two hours ago and one overdue by a quarter of a 120 s race grace, and share the chain's max_nonce.
//  2. Run the periodic sweep with that grace and wait until it is mid-vote on the first record, holding the escrow's finalize lock.
//  3. Run the pre-finalize sweep on a context that signals when it is waited on; assert the sweep waits on it instead of returning.
//  4. Release the votes; assert the periodic sweep voted the first record alone and the pre-finalize sweep then voted the second.
//  5. Assert both records timed out and the escrow got both reserves back.
func TestThePreFinalizeSweepWaitsForAPeriodicSweepAndVotesWithNoGrace(t *testing.T) {
	entered, release, once := make(chan struct{}), make(chan struct{}), &sync.Once{}
	escrow := publishVotingEscrow(t, func(voter acceptingVoter) user.HostClient {
		return gatedVoter{acceptingVoter: voter, entered: entered, enteredOnce: once, release: release}
	})
	escrow.registry.ShareChainMaxNonce(1_700)
	balance := escrow.machine.Balance()
	const grace = 120 * time.Second
	overdueNonce := startedConfirmedAt(t, escrow, time.Now().Add(-2*time.Hour).Unix(), false)
	insideGraceNonce := startedConfirmedAt(t, escrow, overdueInsideGrace(grace), false)
	periodic := make(chan [3]int, 1)
	go func() {
		due, applied, failed := escrow.registry.SweepExecutionTimeouts(context.Background(), grace, 8)
		periodic <- [3]int{due, applied, failed}
	}()
	<-entered

	waiting := waitSignallingContext{Context: context.Background(), waited: make(chan struct{}), waitedOnce: &sync.Once{}}
	beforeFinalize := make(chan sweepResult, 1)
	go func() {
		due, applied, failed, err := escrow.registry.SweepBeforeFinalize(waiting, longResponseEscrowID, 8)
		beforeFinalize <- sweepResult{counts: [3]int{due, applied, failed}, err: err}
	}()
	select {
	case <-waiting.waited:
	case swept := <-beforeFinalize:
		t.Fatalf("the pre-finalize sweep returned %+v while the periodic sweep held the escrow, want it to wait", swept)
	}
	close(release)

	require.Equal(t, [3]int{1, 1, 0}, <-periodic)
	swept := <-beforeFinalize
	require.NoError(t, swept.err)
	require.Equal(t, [3]int{1, 1, 0}, swept.counts)
	for _, nonce := range []uint64{overdueNonce, insideGraceNonce} {
		record, tracked := escrow.machine.GetInference(nonce)
		require.True(t, tracked)
		require.Equal(t, types.StatusTimedOut, record.Status, "nonce %d", nonce)
	}
	require.Equal(t, balance, escrow.machine.Balance())
}

// Test flow:
//  1. Publish a real escrow session with one inference started two hours ago and never finished, and share the chain's max_nonce.
//  2. Take the escrow's finalize lock, as a running sweep or Finalize does.
//  3. Run the pre-finalize sweep on a cancelled context; assert it returns the context's error.
//  4. Assert the record is still started with its reserve held.
func TestThePreFinalizeSweepGivesUpWaitingWhenItsContextEnds(t *testing.T) {
	escrow := publishLongResponseEscrow(t)
	escrow.registry.ShareChainMaxNonce(1_700)
	nonce := startedTwoHoursAgo(t, escrow)
	escrow.handle.finalizing.Lock()
	defer escrow.handle.finalizing.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	due, applied, failed, err := escrow.registry.SweepBeforeFinalize(ctx, longResponseEscrowID, 8)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, [3]int{0, 0, 0}, [3]int{due, applied, failed})
	record, tracked := escrow.machine.GetInference(nonce)
	require.True(t, tracked)
	require.Equal(t, types.StatusStarted, record.Status)
}

// Test flow:
//  1. Publish a real escrow session, share the chain's max_nonce and take its finalize lock, as a running sweep does.
//  2. Finalize the escrow on a cancelled context.
//  3. Assert Finalize returns the context's error and the session is still active.
func TestFinalizeGivesUpWaitingForTheEscrowWhenItsContextEnds(t *testing.T) {
	escrow := publishLongResponseEscrow(t)
	escrow.registry.ShareChainMaxNonce(1_700)
	escrow.handle.finalizing.Lock()
	defer escrow.handle.finalizing.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := escrow.registry.Finalize(ctx, longResponseEscrowID)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, types.PhaseActive, escrow.machine.Phase())
}

// Test flow:
//  1. Unlock a finalize lock nobody holds.
//  2. Assert it did not panic and the lock can still be taken once and only once.
func TestUnlockingAFreeFinalizeLockLeavesItUsable(t *testing.T) {
	lock := newFinalizeLock()

	require.NotPanics(t, lock.Unlock)

	require.True(t, lock.TryLock())
	require.False(t, lock.TryLock())
}
