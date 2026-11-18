package user

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Read the shipped poll and heartbeat intervals.
// 2. Assert the poll is 5 s and the heartbeat is 10 minutes.
func TestTimeoutWaitIntervals(t *testing.T) {
	require.Equal(t, 5*time.Second, timeoutClosePollInterval)
	require.Equal(t, 10*time.Minute, TimeoutHeartbeatInterval)
}

// Test flow:
// 1. Shrink the poll to 10 ms and set an hour-long heartbeat, with a deadline an hour away.
// 2. Start the wait with a close check that reports only from its fourth call, so only polling can reach it.
// 3. Assert the wait returns true within one second, long before the deadline, and logged no heartbeat.
func TestSleepUntilDeadline_NoticesDoneWithinOnePoll(t *testing.T) {
	prevPoll, prevBeat := timeoutClosePollInterval, TimeoutHeartbeatInterval
	timeoutClosePollInterval, TimeoutHeartbeatInterval = 10*time.Millisecond, time.Hour
	t.Cleanup(func() { timeoutClosePollInterval, TimeoutHeartbeatInterval = prevPoll, prevBeat })

	var checks, beats atomic.Int32
	returned := make(chan bool, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		returned <- sleepUntilDeadlineWithHeartbeat(ctx, time.Now().Add(time.Hour), func() { beats.Add(1) }, func() bool { return checks.Add(1) > 3 })
	}()

	select {
	case ok := <-returned:
		require.True(t, ok)
		require.Zero(t, beats.Load())
	case <-time.After(time.Second):
		t.Fatal("the execution wait did not notice the closed record within a poll")
	}
}

// Test flow:
// 1. Set a 60 ms heartbeat, a 10 ms poll and a deadline 200 ms away.
// 2. Run the wait to its deadline with a close check that never reports.
// 3. Assert it returned true at the deadline having logged at most one heartbeat per interval.
func TestSleepUntilDeadline_HeartbeatNotMoreOftenThanItsInterval(t *testing.T) {
	prevPoll, prevBeat := timeoutClosePollInterval, TimeoutHeartbeatInterval
	timeoutClosePollInterval, TimeoutHeartbeatInterval = 10*time.Millisecond, 60*time.Millisecond
	t.Cleanup(func() { timeoutClosePollInterval, TimeoutHeartbeatInterval = prevPoll, prevBeat })

	var beats atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ok := sleepUntilDeadlineWithHeartbeat(ctx, time.Now().Add(200*time.Millisecond), func() { beats.Add(1) }, func() bool { return false })

	require.True(t, ok)
	require.LessOrEqual(t, beats.Load(), int32(3), "the poll must not drive the heartbeat")
}

// Test flow:
// 1. Pend a nonce in a session, set an hour-long heartbeat and a 10 ms poll, and start a refusal wait with an hour-away deadline.
// 2. Give the wait a close check that reports only from its fourth call, so only polling can reach it.
// 3. Assert the wait returns false within one second though no receipt arrived and the heartbeat is an hour away.
func TestSleepRefusal_NoticesClosedWithinOnePoll(t *testing.T) {
	prevPoll, prevBeat := timeoutClosePollInterval, TimeoutHeartbeatInterval
	timeoutClosePollInterval, TimeoutHeartbeatInterval = 10*time.Millisecond, time.Hour
	t.Cleanup(func() { timeoutClosePollInterval, TimeoutHeartbeatInterval = prevPoll, prevBeat })

	session, _, _ := setupSession(t, 3, 100000, 10)
	prepared, err := session.PrepareInference(context.Background(), InferenceParams{
		Model: "llama", Prompt: timeoutVotePayload().Prompt, InputLength: 100, MaxTokens: 100, StartedAt: 1000,
	})
	require.NoError(t, err)

	var checks atomic.Int32
	returned := make(chan bool, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		returned <- session.sleepRefusal(ctx, prepared.Nonce(), time.Unix(1000, 0), time.Now().Add(time.Hour), func() {}, func() bool { return checks.Add(1) > 3 })
	}()

	select {
	case ok := <-returned:
		require.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("the refusal wait did not notice the closed record within a poll")
	}
}
