package user

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/storage"
)

type recordedSteps struct {
	mu    sync.Mutex
	steps map[string]int
}

func (r *recordedSteps) record(step string, _ time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps[step]++
}

func (r *recordedSteps) seen() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.steps
}

// Test flow:
//  1. Open a stored session with a step timer.
//  2. Serve one inference the way the gateway does: prepare, send, apply the response, then close.
//  3. Assert every lock and store step was timed: each one a request's throughput depends on is visible without a profiler.
func TestSession_AStepTimerSeesTheLockAndTheStore(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	steps := &recordedSteps{steps: map[string]int{}}
	session.SetStepTimer(steps.record)

	prepared, err := session.PrepareInferenceFn(func(HostBinding) (InferenceParams, bool, context.Context, error) {
		return signatureTestParams(), false, nil, nil
	})
	require.NoError(t, err)
	reply, err := session.SendOnly(context.Background(), prepared, nil, nil)
	require.NoError(t, err)
	require.NoError(t, session.ProcessResponse(prepared.HostIdx(), reply, prepared.Nonce()))
	require.NoError(t, session.Close())

	seen := steps.seen()
	for _, step := range []string{StepLockWaitPrepare, StepLockHeldPrepare, StepStoreAppendDiff, StepLockWaitProcess, StepLockHeldProcess, StepStoreAddSignature} {
		require.Positive(t, seen[step], "step %s was never timed; timed: %v", step, seen)
	}
}

// Test flow:
//  1. Serve one inference on a stored session, then hold the session lock as a nonce commit writing to a slow store does.
//  2. Read the published nonce while the lock is held.
//  3. Assert the read returns the served nonce without waiting: routing and status read it on every request and every poll.
func TestSession_PublishedNonceIsReadWhileTheLockIsHeld(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	served, err := session.SendInference(context.Background(), signatureTestParams())
	require.NoError(t, err)

	session.mu.Lock()
	defer session.mu.Unlock()
	read := make(chan uint64, 1)
	go func() { read <- session.PublishedNonce() }()

	select {
	case nonce := <-read:
		require.Equal(t, served.Nonce, nonce)
	case <-time.After(5 * time.Second):
		t.Fatal("reading the published nonce waited for the session lock")
	}
}
