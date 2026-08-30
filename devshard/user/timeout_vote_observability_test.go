package user

import (
	"bytes"
	"context"
	"log"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/logging"
	"devshard/types"
)

func captureStdLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

type delayedTimeoutVerifier struct {
	inner mockTimeoutVerifier
	delay time.Duration
	err   error
}

func (d *delayedTimeoutVerifier) VerifyTimeout(ctx context.Context, inferenceID uint64, reason types.TimeoutReason, payload *host.InferencePayload, diffs []types.Diff) (bool, []byte, uint32, error) {
	if d.delay > 0 {
		select {
		case <-time.After(d.delay):
		case <-ctx.Done():
			return false, nil, 0, ctx.Err()
		}
	}
	if d.err != nil {
		return false, nil, 0, d.err
	}
	return d.inner.VerifyTimeout(ctx, inferenceID, reason, payload, diffs)
}

type errTimeoutVerifier struct {
	err error
}

func (m *errTimeoutVerifier) VerifyTimeout(context.Context, uint64, types.TimeoutReason, *host.InferencePayload, []types.Diff) (bool, []byte, uint32, error) {
	return false, nil, 0, m.err
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
	defer close(releaseFirst)

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
	go func() {
		_, _ = session.CollectTimeoutVotes(holderCtx, nonce, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, payload, firstVerifiers, nil)
	}()

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
	votes, class, err := session.collectTimeoutVotes(waiterCtx, nonce, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, payload, waitingVerifiers, nil)
	require.NoError(t, err)
	require.Empty(t, votes)
	require.Zero(t, secondEntered.Load())
	require.Equal(t, VoteErrorQueueExpired, class)

	logs := buf.String()
	require.Contains(t, logs, "stage=timeout_vote_queue_expired")
	require.Contains(t, logs, "inflight=1")
	require.Contains(t, logs, "nonce=1")
	require.Contains(t, logs, "error_classes=queue_expired:2")
	require.NotContains(t, logs, "request=req-waiter stage=timeout_vote_sent")
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
	defer close(releaseFirst)
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

	go func() {
		_, _ = session.CollectTimeoutVotes(ctx, nonce, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, payload, firstVerifiers, nil)
	}()
	require.Eventually(t, func() bool { return firstEntered.Load() == int32(len(session.group)-1) }, time.Second, 5*time.Millisecond)

	buf := captureStdLog(t)
	waiterCtx, _ := logging.WithRequestID(ctx, "req-waiter")
	_, class, err := session.collectTimeoutVotes(waiterCtx, nonce, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, payload, waitingVerifiers, nil)
	require.NoError(t, err)
	require.Equal(t, VoteErrorQueueExpired, class)
	logs := buf.String()
	require.Contains(t, logs, "request=req-waiter stage=timeout_vote_requested")
	require.NotContains(t, logs, "request=req-waiter stage=timeout_vote_sent")
	require.NotContains(t, logs, "stage=timeout_vote_rpc_timeout")
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
	votes, err := session.CollectTimeoutVotes(ctx, nonce, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, payload, verifiers, nil)
	require.NoError(t, err)
	require.NotEmpty(t, votes)
	require.Contains(t, buf.String(), "stage=timeout_vote_slow")
	require.Contains(t, buf.String(), "outcome=accept")
	require.Contains(t, buf.String(), "stage=timeout_vote_sent")
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
	votes, class, err := session.collectTimeoutVotes(ctx, nonce, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, payload, verifiers, nil)
	require.NoError(t, err)
	require.Empty(t, votes)
	require.Equal(t, VoteErrorRPCTimeout, class)
	logs := buf.String()
	require.Contains(t, logs, "stage=timeout_vote_sent")
	require.Contains(t, logs, "stage=timeout_vote_rpc_timeout")
	require.Contains(t, logs, "error_classes=rpc_timeout:2")
	require.NotContains(t, logs, "stage=timeout_vote_queue_expired")
	require.NotContains(t, logs, "error_classes=queue_expired")
}
