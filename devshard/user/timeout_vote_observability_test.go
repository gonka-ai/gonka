package user

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/logging"
	"devshard/types"
)

// stageLog captures logging.Stage output. A collection left holding the queue keeps logging after the
// assertions run, and log's writer is process-wide, so the buffer is written by goroutines this test
// does not own and every access has to be guarded.
type stageLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *stageLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// forRequest returns only the lines this request emitted. Leaked goroutines from other tests share
// the same writer, so an unfiltered NotContains would fail on output that was never ours.
func (l *stageLog) forRequest(requestID string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var kept []string
	for _, line := range strings.Split(l.buf.String(), "\n") {
		if strings.Contains(line, "request="+requestID+" ") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func captureStdLog(t *testing.T) *stageLog {
	t.Helper()
	captured := &stageLog{}
	log.SetOutput(captured)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return captured
}

type delayedTimeoutVerifier struct {
	inner mockTimeoutVerifier
	delay time.Duration
	err   error
}

func (d *delayedTimeoutVerifier) wait(ctx context.Context) error {
	if d.delay > 0 {
		select {
		case <-time.After(d.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return d.err
}

func (d *delayedTimeoutVerifier) VerifyTimeout(ctx context.Context, inferenceID uint64, reason types.TimeoutReason, payload *host.InferencePayload, diffs []types.Diff, artifacts host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	if err := d.wait(ctx); err != nil {
		return false, nil, 0, nil, "", err
	}
	return d.inner.VerifyTimeout(ctx, inferenceID, reason, payload, diffs, artifacts)
}

func (d *delayedTimeoutVerifier) VerifyErrorMiss(ctx context.Context, inferenceID uint64, diffs []types.Diff, artifacts host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	if err := d.wait(ctx); err != nil {
		return false, nil, 0, nil, "", err
	}
	return d.inner.VerifyErrorMiss(ctx, inferenceID, diffs, artifacts)
}

type errTimeoutVerifier struct {
	err error
}

func (m *errTimeoutVerifier) VerifyTimeout(context.Context, uint64, types.TimeoutReason, *host.InferencePayload, []types.Diff, host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	return false, nil, 0, nil, "", m.err
}

func (m *errTimeoutVerifier) VerifyErrorMiss(context.Context, uint64, []types.Diff, host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	return false, nil, 0, nil, "", m.err
}

// awaitHolder releases a blocked collection and joins its goroutine during cleanup. Without the join
// the holder outlives the test and keeps reading VerifyTimeoutSlowLog and friends while the next test
// writes them, which the race detector reports against whichever test happens to be running.
func awaitHolder(t *testing.T, release chan struct{}, done <-chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
		close(release)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("blocked collection did not return after its verifiers were released")
		}
	})
}

func timeoutVotePayload() *host.InferencePayload {
	return &host.InferencePayload{
		Prompt: testutil.TestPrompt, Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
}

func TestQueueExpired_LogsInflightSnapshot(t *testing.T) {
	savedCap := MaxConcurrentVerifierRPCs
	MaxConcurrentVerifierRPCs = 1
	savedWait := VerifierQueueWaitTimeout
	VerifierQueueWaitTimeout = 50 * time.Millisecond
	t.Cleanup(func() {
		MaxConcurrentVerifierRPCs = savedCap
		VerifierQueueWaitTimeout = savedWait
	})

	session, hosts, _ := setupSessionWithOptions(t, 3, 100000, 10, WithVerifierQueue(newVerifierHostQueue()))
	ctx := context.Background()
	_, err := session.SendInference(ctx, InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)

	nonce := uint64(1)
	executorIdx := int(nonce % uint64(len(session.group)))
	payload := timeoutVotePayload()

	perSlotActive := make(map[int]*atomic.Int32)
	perSlotMax := make(map[int]*atomic.Int32)
	for i := range session.group {
		perSlotActive[i] = &atomic.Int32{}
		perSlotMax[i] = &atomic.Int32{}
	}

	releaseFirst := make(chan struct{})
	var firstEntered, secondEntered atomic.Int32
	firstVerifiers := make(map[int]TimeoutVerifier)
	waitingVerifiers := make(map[int]TimeoutVerifier)
	for i, slot := range session.group {
		if i == executorIdx {
			continue
		}
		firstVerifiers[i] = &concurrencyMockVerifier{
			slotIdx:       i,
			group:         session.group,
			signer:        signerForSlot(t, hosts, slot),
			perSlotActive: perSlotActive,
			perSlotMax:    perSlotMax,
			totalEntered:  &firstEntered,
			release:       releaseFirst,
		}
		waitingVerifiers[i] = &concurrencyMockVerifier{
			slotIdx:       i,
			group:         session.group,
			signer:        signerForSlot(t, hosts, slot),
			perSlotActive: perSlotActive,
			perSlotMax:    perSlotMax,
			totalEntered:  &secondEntered,
		}
	}

	holderCtx, _ := logging.WithRequestID(ctx, "req-holder")
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_, _, _, _ = session.CollectTimeoutVotes(holderCtx, nonce, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, payload, firstVerifiers, nil)
	}()
	// The holder must finish before the cleanups above restore the package globals it still reads,
	// so this is registered after them: cleanups run last-registered-first.
	awaitHolder(t, releaseFirst, holderDone)

	expectedVerifiers := int32(len(session.group) - 1)
	require.Eventually(t, func() bool {
		return firstEntered.Load() == expectedVerifiers
	}, time.Second, 5*time.Millisecond, "first call should occupy every verifier slot")

	for i, slot := range session.group {
		if i == executorIdx {
			continue
		}
		inflight, _ := session.verifierQueue.snapshot(slot.ValidatorAddress)
		require.Len(t, inflight, 1, "holder should be inflight on %s", slot.ValidatorAddress)
		require.Equal(t, nonce, inflight[0].Nonce)
		require.Equal(t, session.escrowID, inflight[0].Escrow)
		require.Equal(t, "execution", inflight[0].Reason)
		require.Equal(t, "req-holder", inflight[0].RequestID)
	}

	buf := captureStdLog(t)
	waiterCtx, _ := logging.WithRequestID(ctx, "req-waiter")
	votes, _, _, class, err := session.collectTimeoutVotes(waiterCtx, nonce, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, payload, waitingVerifiers, nil)
	require.NoError(t, err)
	require.Empty(t, votes)
	require.Zero(t, secondEntered.Load())
	require.Equal(t, VoteErrorQueueExpired, class)

	logs := buf.forRequest("req-waiter")
	require.Contains(t, logs, "stage=timeout_vote_queue_expired")
	require.Contains(t, logs, "inflight=1")
	require.Contains(t, logs, "waiters=0",
		"the expiring round has already left the queue, so waiters counts only others still behind the holder")
	// The whole point: the blocked request's own line names the holder it could never see.
	require.Contains(t, logs, "request=req-holder escrow=escrow-1 nonce=1 reason=execution sent_age_ms=")
	require.Contains(t, logs, "error_classes=verifier_queue_expired:2")
	require.NotContains(t, logs, "stage=timeout_vote_sent")
}

func TestTimeoutVoteSent_OnlyAfterAcquire(t *testing.T) {
	savedCap := MaxConcurrentVerifierRPCs
	MaxConcurrentVerifierRPCs = 1
	savedWait := VerifierQueueWaitTimeout
	VerifierQueueWaitTimeout = 50 * time.Millisecond
	t.Cleanup(func() {
		MaxConcurrentVerifierRPCs = savedCap
		VerifierQueueWaitTimeout = savedWait
	})

	session, hosts, _ := setupSessionWithOptions(t, 3, 100000, 10, WithVerifierQueue(newVerifierHostQueue()))
	ctx := context.Background()
	_, err := session.SendInference(ctx, InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)

	nonce := uint64(1)
	executorIdx := int(nonce % uint64(len(session.group)))
	payload := timeoutVotePayload()
	perSlotActive := make(map[int]*atomic.Int32)
	perSlotMax := make(map[int]*atomic.Int32)
	for i := range session.group {
		perSlotActive[i] = &atomic.Int32{}
		perSlotMax[i] = &atomic.Int32{}
	}
	releaseFirst := make(chan struct{})
	var firstEntered atomic.Int32
	firstVerifiers := make(map[int]TimeoutVerifier)
	waitingVerifiers := make(map[int]TimeoutVerifier)
	for i, slot := range session.group {
		if i == executorIdx {
			continue
		}
		firstVerifiers[i] = &concurrencyMockVerifier{
			slotIdx: i, group: session.group, signer: signerForSlot(t, hosts, slot),
			perSlotActive: perSlotActive, perSlotMax: perSlotMax, totalEntered: &firstEntered, release: releaseFirst,
		}
		waitingVerifiers[i] = &errTimeoutVerifier{err: context.DeadlineExceeded}
	}

	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_, _, _, _ = session.CollectTimeoutVotes(ctx, nonce, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, payload, firstVerifiers, nil)
	}()
	awaitHolder(t, releaseFirst, holderDone)
	require.Eventually(t, func() bool { return firstEntered.Load() == int32(len(session.group)-1) }, time.Second, 5*time.Millisecond)

	buf := captureStdLog(t)
	waiterCtx, _ := logging.WithRequestID(ctx, "req-waiter")
	_, _, _, class, err := session.collectTimeoutVotes(waiterCtx, nonce, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, payload, waitingVerifiers, nil)
	require.NoError(t, err)
	require.Equal(t, VoteErrorQueueExpired, class)
	logs := buf.forRequest("req-waiter")
	require.Contains(t, logs, "stage=timeout_vote_requested")
	require.NotContains(t, logs, "stage=timeout_vote_sent",
		"a vote that never won a slot must not claim it was sent")
	require.NotContains(t, logs, "stage=timeout_vote_rpc_timeout",
		"a queue wait that expired is not a verifier that failed to answer")
}

func TestVerifyTimeout_SlowLog(t *testing.T) {
	saved := VerifyTimeoutSlowLog
	VerifyTimeoutSlowLog = 10 * time.Millisecond
	t.Cleanup(func() { VerifyTimeoutSlowLog = saved })

	session, hosts, _ := setupSessionWithOptions(t, 3, 100000, 10, WithVerifierQueue(newVerifierHostQueue()))
	ctx := context.Background()
	_, err := session.SendInference(ctx, InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)

	nonce := uint64(1)
	executorIdx := int(nonce % uint64(len(session.group)))
	payload := timeoutVotePayload()
	verifiers := make(map[int]TimeoutVerifier)
	for i, slot := range session.group {
		if i == executorIdx {
			continue
		}
		verifiers[i] = &delayedTimeoutVerifier{
			delay: 20 * time.Millisecond,
			inner: mockTimeoutVerifier{accept: true, signer: signerForSlot(t, hosts, slot), group: session.group, slotIdx: i},
		}
	}

	buf := captureStdLog(t)
	slowCtx, _ := logging.WithRequestID(ctx, "req-slow")
	votes, _, _, err := session.CollectTimeoutVotes(slowCtx, nonce, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, payload, verifiers, nil)
	require.NoError(t, err)
	require.NotEmpty(t, votes)

	logs := buf.forRequest("req-slow")
	require.Contains(t, logs, "stage=timeout_vote_sent")
	require.Contains(t, logs, "stage=timeout_vote_slow")
	require.Contains(t, logs, "outcome=accept")
	require.NotContains(t, logs, "stage=timeout_vote_rpc_timeout",
		"a slow call that answered is not a timeout")
}

func TestVerifyTimeout_RPCTimeoutLog(t *testing.T) {
	session, _, _ := setupSessionWithOptions(t, 3, 100000, 10, WithVerifierQueue(newVerifierHostQueue()))
	ctx := context.Background()
	_, err := session.SendInference(ctx, InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)

	nonce := uint64(1)
	executorIdx := int(nonce % uint64(len(session.group)))
	payload := timeoutVotePayload()
	verifiers := make(map[int]TimeoutVerifier)
	for i := range session.group {
		if i == executorIdx {
			continue
		}
		verifiers[i] = &errTimeoutVerifier{err: context.DeadlineExceeded}
	}

	buf := captureStdLog(t)
	rpcCtx, _ := logging.WithRequestID(ctx, "req-rpc")
	votes, _, _, class, err := session.collectTimeoutVotes(rpcCtx, nonce, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, payload, verifiers, nil)
	require.NoError(t, err)
	require.Empty(t, votes)
	require.Equal(t, VoteErrorRPCTimeout, class)

	logs := buf.forRequest("req-rpc")
	require.Contains(t, logs, "stage=timeout_vote_sent")
	require.Contains(t, logs, "stage=timeout_vote_rpc_timeout")
	require.Contains(t, logs, "error_classes=verifier_rpc_timeout:2")
	require.NotContains(t, logs, "stage=timeout_vote_queue_expired",
		"the slot was won, so nothing expired on the queue")
	require.NotContains(t, logs, "verifier_queue_expired")
}
