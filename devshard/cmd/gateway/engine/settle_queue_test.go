package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/config"
)

// queueHarness fires the queue's timers on command, so nothing in a test waits on the wall clock.
type queueHarness struct {
	queue  *settleQueue
	posted chan string

	mu      sync.Mutex
	clock   time.Time
	pending []scheduled
}

type scheduled struct {
	delay time.Duration
	fire  func()
}

func newQueueHarness() *queueHarness {
	harness := &queueHarness{posted: make(chan string, 32), clock: time.Unix(1_700_000_000, 0)}
	harness.queue = newSettleQueue(harness.now)
	harness.queue.after = harness.schedule
	return harness
}

func (h *queueHarness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock
}

func (h *queueHarness) schedule(delay time.Duration, fire func()) *time.Timer {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pending = append(h.pending, scheduled{delay: delay, fire: fire})
	return nil
}

func (h *queueHarness) scheduledDelays() []time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	delays := make([]time.Duration, 0, len(h.pending))
	for _, entry := range h.pending {
		delays = append(delays, entry.delay)
	}
	return delays
}

// fire runs a scheduled timer the way the runtime does, on a goroutine of its own.
func (h *queueHarness) fire(position int) {
	h.mu.Lock()
	entry := h.pending[position]
	h.mu.Unlock()
	go entry.fire()
}

func (h *queueHarness) add(name string, dueIn time.Duration, limit int) {
	deadline := h.now().Add(dueIn)
	h.queue.Add(settleTask{
		deadline: func() time.Time { return deadline },
		post:     func() { h.posted <- name },
	}, limit)
}

// blocking adds a vote whose post holds its poster until the returned release is called.
func (h *queueHarness) blocking(name string, dueIn time.Duration, limit int) (release func()) {
	held := make(chan struct{})
	deadline := h.now().Add(dueIn)
	h.queue.Add(settleTask{
		deadline: func() time.Time { return deadline },
		post:     func() { h.posted <- name; <-held },
	}, limit)
	return func() { close(held) }
}

func (h *queueHarness) nextPosted(t *testing.T) string {
	t.Helper()
	select {
	case name := <-h.posted:
		return name
	case <-time.After(2 * time.Second):
		t.Fatal("no vote was posted")
		return ""
	}
}

func (h *queueHarness) assertNothingPosted(t *testing.T) {
	t.Helper()
	select {
	case name := <-h.posted:
		t.Fatalf("posted %q, want nothing yet", name)
	case <-time.After(50 * time.Millisecond):
	}
}

// Test flow:
//  1. Add two votes to the queue harness with different due-in durations.
//  2. Assert the queue scheduled timers matching each vote's own deadline.
//  3. Assert nothing has been posted yet.
func TestAVoteWaitsOnATimerSetToItsOwnDeadline(t *testing.T) {
	harness := newQueueHarness()

	harness.add("execution-in-an-hour", time.Hour, 4)
	harness.add("refused-in-a-minute", time.Minute, 4)

	require.Equal(t, []time.Duration{time.Hour, time.Minute}, harness.scheduledDelays())
	harness.assertNothingPosted(t)
}

// Test flow:
//  1. Add a vote due in an hour and a vote due now, then fire the due-now timer.
//  2. Assert only the due-now vote is posted, unblocked by the vote still waiting.
func TestAVoteStillWaitingHoldsNoPoster(t *testing.T) {
	harness := newQueueHarness()

	harness.add("due-in-an-hour", time.Hour, 1)
	harness.add("due-now", 0, 1)
	harness.fire(1)

	if posted := harness.nextPosted(t); posted != "due-now" {
		t.Fatalf("posted %q, want the due vote through the only poster", posted)
	}
}

// Test flow:
//  1. Add five votes all due now, sharing a poster limit of one, then fire every timer.
//  2. Collect every posted vote.
//  3. Assert all five votes were posted and the queue owes nothing afterward.
func TestEveryVotePastTheLimitIsStillPosted(t *testing.T) {
	harness := newQueueHarness()
	names := []string{"first", "second", "third", "fourth", "fifth"}

	for _, name := range names {
		harness.add(name, 0, 1)
	}
	for position := range names {
		harness.fire(position)
	}

	posted := map[string]bool{}
	for range names {
		posted[harness.nextPosted(t)] = true
	}
	require.Len(t, posted, len(names), "every vote the queue was given must be posted")
	require.Zero(t, harness.queue.Owed(), "a posted vote is no longer owed")
}

// Test flow:
//  1. Add a blocking vote holding the single poster slot, then add and fire a second vote waiting for a place.
//  2. Assert the first vote posts and the second stays unposted while the place is held.
//  3. Release the first vote's poster and assert the second vote posts once the place frees up.
func TestTheLimitBoundsTheVotesInFlight(t *testing.T) {
	harness := newQueueHarness()

	release := harness.blocking("holding-the-place", 0, 1)
	harness.add("waiting-for-a-place", 0, 1)
	harness.fire(0)
	if posted := harness.nextPosted(t); posted != "holding-the-place" {
		t.Fatalf("posted %q, want the first vote", posted)
	}
	harness.fire(1)

	harness.assertNothingPosted(t)
	release()
	if posted := harness.nextPosted(t); posted != "waiting-for-a-place" {
		t.Fatalf("posted %q, want the queued vote once the place was free", posted)
	}
}

// duePoster answers that every vote may be posted at once, so a test measures the queue and not a deadline.
type duePoster struct {
	mu    sync.Mutex
	posts []uint64
}

func (p *duePoster) VoteDeadline(uint64, time.Time) time.Time { return time.Time{} }

func (p *duePoster) SettleTimeout(_ context.Context, step TimeoutStep) (TimeoutVote, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.posts = append(p.posts, step.Nonce)
	return TimeoutVote{Kind: TimeoutKindRefused}, nil
}

func (p *duePoster) posted() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.posts)
}

// Test flow:
//  1. Build an engine whose settle queue allows only one concurrent timeout vote, backed by a `duePoster`.
//  2. Settle six races with unsettled attempts, each owing a timeout vote, then stop the engine.
//  3. Assert every one of the six votes reached the poster and the queue owes nothing once stopped.
func TestNoVoteIsLostWhenRacesOweMoreThanTheLimit(t *testing.T) {
	poster := &duePoster{}
	settings := engineSettings(EscalationPolicy{}, config.Modes{})
	settings.Engine.MaxConcurrentTimeoutVotes = 1
	deps := completeEngineDeps(t)
	deps.Config = config.NewHolder(settings)
	deps.Timeouts = func(string, any) (TimeoutPoster, bool) { return poster, true }
	races, err := NewEngine(deps)
	require.NoError(t, err)

	for nonce := range uint64(6) {
		attempt := unsettledAttempt()
		attempt.Nonce = nonce + 11
		races.settle(race(attempt), nil, races.admit())
	}
	races.Stop()

	require.Equal(t, 6, poster.posted(), "every vote a race owed must reach the chain")
	require.Zero(t, races.settles.Owed(), "no vote may be left owed once the engine has stopped")
}

// Test flow:
//  1. Add a vote due in a minute, then move its deadline out to an hour before its timer fires.
//  2. Fire the original timer; assert nothing posts and the queue rearmed a new timer for the later deadline.
//  3. Move the deadline back into the past and fire the new timer; assert the vote now posts.
func TestAVoteWhoseDeadlineMovedOutIsArmedAgainRatherThanPosted(t *testing.T) {
	harness := newQueueHarness()
	var deadline time.Time
	deadline = harness.now().Add(time.Minute)

	harness.queue.Add(settleTask{
		deadline: func() time.Time { return deadline },
		post:     func() { harness.posted <- "posted" },
	}, 1)
	deadline = harness.now().Add(time.Hour)
	harness.fire(0)

	harness.assertNothingPosted(t)
	require.Equal(t, []time.Duration{time.Minute, time.Hour}, harness.scheduledDelays(),
		"the vote must be armed again for the deadline it now has")
	deadline = harness.now().Add(-time.Second)
	harness.fire(1)
	require.Equal(t, "posted", harness.nextPosted(t))
}

// Test flow:
//  1. Add a vote with no deadline at all, as a race whose escrow resolved no poster owes.
//  2. Fire its timer.
//  3. Assert the timer was armed to fire at once rather than at a saturated negative delay, and the vote was still posted.
func TestAVoteWithNoDeadlineIsArmedToFireAtOnce(t *testing.T) {
	harness := newQueueHarness()

	harness.queue.Add(settleTask{
		deadline: func() time.Time { return time.Time{} },
		post:     func() { harness.posted <- "no-poster" },
	}, 4)

	require.Equal(t, []time.Duration{0}, harness.scheduledDelays())
	harness.fire(0)
	require.Equal(t, "no-poster", harness.nextPosted(t))
}
