package host

import (
	"bytes"
	"context"
	"log"
	"testing"
	"time"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks/headertime"
	"devshard/types"
)

func verifierMismatchCount(kind string) float64 {
	return promtestutil.ToFloat64(headertime.TimeoutClockMismatchCounter().WithLabelValues("verifier", kind))
}

// A stable mismatch counts once per reason. The other reason still counts.
func TestLegacyVerifierMismatchOncePerReason(t *testing.T) {
	now := time.Unix(1_800_000_600, 0)
	interval := 5 * time.Minute
	wallUnix := now.Add(-(interval + time.Second)).Unix()
	m := &scriptMap{
		now:         now,
		missing:     true,
		interval:    interval,
		legacyClock: true,
	}
	rec := &types.InferenceRecord{
		Status:            types.StatusStarted,
		StartedAt:         wallUnix,
		StartedAtHeight:   100,
		ConfirmedAt:       wallUnix,
		ConfirmedAtHeight: 100,
	}

	beforeRefused := verifierMismatchCount("refused")
	beforeExecution := verifierMismatchCount("execution")
	ready, err := awaitVerifierDue(context.Background(), m, 40, rec, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.NoError(t, err)
	require.True(t, ready)
	ready, err = awaitVerifierDue(context.Background(), m, 40, rec, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, beforeRefused+1, verifierMismatchCount("refused"))
	require.Equal(t, beforeExecution, verifierMismatchCount("execution"))

	ready, err = awaitVerifierDue(context.Background(), m, 40, rec, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)
	require.NoError(t, err)
	require.True(t, ready)
	ready, err = awaitVerifierDue(context.Background(), m, 40, rec, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, beforeRefused+1, verifierMismatchCount("refused"))
	require.Equal(t, beforeExecution+1, verifierMismatchCount("execution"))
}

// Legacy: wall due + stamp missing → challenge may proceed without AnswerWait;
// mismatch is recorded.
func TestLegacyVerifierOpensOnWallDespiteStampMiss(t *testing.T) {
	now := time.Unix(1_800_000_600, 0)
	interval := 5 * time.Minute
	wallUnix := now.Add(-(interval + time.Second)).Unix()

	m := &scriptMap{
		now:         now,
		missing:     true,
		interval:    interval,
		legacyClock: true,
	}
	rec := &types.InferenceRecord{
		Status:          types.StatusPending,
		StartedAt:       wallUnix,
		StartedAtHeight: 100,
	}

	before := verifierMismatchCount("refused")
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prev)

	started := time.Now()
	ready, err := awaitVerifierDue(context.Background(), m, 7, rec, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.NoError(t, err)
	require.True(t, ready, "legacy Active opens on wall without waiting for the stamp")
	require.Less(t, time.Since(started), 200*time.Millisecond, "no AnswerWait for legacy Active")
	require.Greater(t, verifierMismatchCount("refused"), before)
	require.Contains(t, logs.String(), "timeout clock mismatch")
}

// Legacy: wall not due → not ready even if stamp would be due; no AnswerWait.
func TestLegacyVerifierDoesNotOpenWhenWallNotDue(t *testing.T) {
	now := time.Unix(1_800_000_600, 0)
	interval := 5 * time.Minute
	wallUnix := now.Unix() // wall not past interval
	stamp := now.Add(-(interval + time.Second))

	m := &scriptMap{
		now:         now,
		stamp:       stamp,
		interval:    interval,
		legacyClock: true,
	}
	rec := &types.InferenceRecord{
		Status:          types.StatusPending,
		StartedAt:       wallUnix,
		StartedAtHeight: 100,
	}

	before := verifierMismatchCount("refused")
	ready, err := awaitVerifierDue(context.Background(), m, 8, rec, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.NoError(t, err)
	require.False(t, ready)
	require.Greater(t, verifierMismatchCount("refused"), before, "stamp due vs wall not due is a mismatch")
}

// Header: stamp miss still AnswerWait / feed_behind (no wall short-circuit).
func TestHeaderVerifierStampMissStillWaitsAnswerWait(t *testing.T) {
	now := time.Unix(1_800_000_600, 0)
	m := &scriptMap{
		now:      now,
		missing:  true,
		interval: headertime.AnswerWait,
		// legacyClock false → header mode
	}
	rec := &types.InferenceRecord{
		Status:          types.StatusPending,
		StartedAt:       now.Add(-time.Hour).Unix(), // wall would be due
		StartedAtHeight: 100,
	}

	before := verifierMismatchCount("refused")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var ready bool
	var err error
	go func() {
		ready, err = awaitVerifierDue(ctx, m, 9, rec, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
		close(done)
	}()

	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.timers) > 0 || len(m.watches) > 0
	}, time.Second, 5*time.Millisecond, "header mode waits on a missing stamp")

	// Advance past AnswerWait → feed_behind, not due.
	m.mu.Lock()
	m.now = now.Add(headertime.AnswerWait)
	for _, ch := range m.timers {
		select {
		case ch <- m.now:
		default:
		}
	}
	m.mu.Unlock()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("awaitVerifierDue did not return after AnswerWait")
	}
	require.NoError(t, err)
	require.False(t, ready, "feed_behind does not open the challenge")
	require.Equal(t, before, verifierMismatchCount("refused"), "header mode has no wall shadow mismatch")
}

// Header execution uses stamp; wall past alone does not open.
func TestHeaderVerifierExecutionIgnoresWallAlone(t *testing.T) {
	now := time.Unix(1_800_000_600, 0)
	interval := 5 * time.Minute
	m := &scriptMap{
		now:      now,
		stamp:    now, // stamp not yet past interval
		interval: interval,
	}
	rec := &types.InferenceRecord{
		Status:            types.StatusStarted,
		ConfirmedAt:       now.Add(-(interval + time.Second)).Unix(),
		ConfirmedAtHeight: 50,
	}
	before := verifierMismatchCount("execution")
	ready, err := awaitVerifierDue(context.Background(), m, 10, rec, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)
	require.NoError(t, err)
	require.False(t, ready)
	require.Equal(t, before, verifierMismatchCount("execution"))
}

// Duplicate-challenge memory still applies after a legacy Active due accept path.
func TestLegacyVerifierDuplicateChallengeMemoryUnchanged(t *testing.T) {
	now := time.Unix(1_800_000_600, 0)
	interval := time.Minute
	wallUnix := now.Add(-(interval + time.Second)).Unix()
	m := &scriptMap{
		now:         now,
		missing:     true,
		interval:    interval,
		legacyClock: true,
	}
	st := stateWithPendingFull(11, 1)
	rec := st.Inferences[11]
	rec.StartedAt = wallUnix
	rec.StartedAtHeight = 1

	// Unreachable executor accepts the refusal and remembers the vote (nil
	// client is a preflight that drops the hold without voting).
	executor := &pendingExecutor{}
	accept, err := VerifyRefusedTimeout(context.Background(), st, 11, rec, testPayload(), nil, executor, nil, m)
	require.NoError(t, err)
	require.True(t, accept)
	require.Equal(t, 1, executor.calls)

	// Second challenge while the vote is remembered → duplicate.
	_, err = VerifyRefusedTimeout(context.Background(), st, 11, rec, testPayload(), nil, executor, nil, m)
	require.ErrorIs(t, err, ErrDuplicateChallenge)
}

// Legacy execution opens on wall ConfirmedAt when stamp is not due.
func TestLegacyVerifierExecutionOpensOnWallConfirmedAt(t *testing.T) {
	now := time.Unix(1_800_000_600, 0)
	interval := 5 * time.Minute
	m := &scriptMap{
		now:         now,
		stamp:       now.Add(time.Hour), // stamp not due
		interval:    interval,
		legacyClock: true,
	}
	rec := &types.InferenceRecord{
		Status:            types.StatusStarted,
		ConfirmedAt:       now.Add(-(interval + time.Second)).Unix(),
		ConfirmedAtHeight: 50,
	}
	before := verifierMismatchCount("execution")
	ready, err := awaitVerifierDue(context.Background(), m, 12, rec, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)
	require.NoError(t, err)
	require.True(t, ready)
	require.Greater(t, verifierMismatchCount("execution"), before)
}
