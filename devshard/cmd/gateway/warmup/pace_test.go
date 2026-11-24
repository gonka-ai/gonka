package warmup

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/user"
)

type pacedClock struct {
	current time.Time
	sleeps  []time.Duration
}

func (c *pacedClock) now() time.Time { return c.current }

func (c *pacedClock) sleep(_ context.Context, delay time.Duration) error {
	c.sleeps = append(c.sleeps, delay)
	c.current = c.current.Add(delay)
	return nil
}

var roundInterval = time.Duration(float64(time.Second) / float64(VoteRoundsPerSecond))

func newPacedClock() *pacedClock { return &pacedClock{current: time.Unix(0, 0)} }

func waitRounds(t *testing.T, pacer *VotePacer, rounds int) {
	t.Helper()
	for range rounds {
		if err := pacer.Wait(context.Background()); err != nil {
			t.Fatalf("Wait() = %v, want nil", err)
		}
	}
}

// Test flow:
//  1. Build a pacer on a virtual clock and wait for the burst's worth of rounds.
//  2. Assert none of them slept.
//  3. Wait for ten more rounds and assert the clock moved by ten round intervals, so the rounds are spaced at the configured rate.
func TestVotePacerLetsABurstThroughThenSpacesRounds(t *testing.T) {
	clock := newPacedClock()
	pacer := NewVotePacer(clock.now, clock.sleep)

	waitRounds(t, pacer, VoteRoundBurst)
	burstSleeps := slices.Clone(clock.sleeps)
	waitRounds(t, pacer, 10)

	for _, delay := range burstSleeps {
		if delay != 0 {
			t.Fatalf("slept %v inside the burst, want no delay", burstSleeps)
		}
	}
	want := time.Duration(10 * float64(time.Second) / float64(VoteRoundsPerSecond))
	if elapsed := clock.current.Sub(time.Unix(0, 0)); elapsed < want-time.Millisecond || elapsed > want+time.Millisecond {
		t.Errorf("ten rounds past the burst took %s, want %s", elapsed, want)
	}
}

// Test flow:
//  1. Build a pacer on a virtual clock and take rounds at one instant until the next would wait longer than VoteMaxWait.
//  2. Assert no round slept longer than VoteMaxWait.
//  3. Take a thousand more rounds at the same instant and assert none slept, so a backlog past the bound is never held longer.
//  4. Advance the clock by VoteMaxWait and assert the next round waits at most one interval, so the rounds started past the bound left no debt in the bucket.
func TestVotePacerStartsARoundAtOnceWhenItsSlotIsPastTheMaxWait(t *testing.T) {
	clock := newPacedClock()
	pacer := NewVotePacer(clock.now, func(ctx context.Context, delay time.Duration) error {
		clock.sleeps = append(clock.sleeps, delay)
		return nil
	})
	withinBound := VoteRoundBurst + int(float64(VoteRoundsPerSecond)*VoteMaxWait.Seconds())

	waitRounds(t, pacer, withinBound)
	for _, delay := range clock.sleeps {
		if delay > VoteMaxWait {
			t.Fatalf("a round slept %s, want at most %s", delay, VoteMaxWait)
		}
	}
	slept := len(clock.sleeps)
	waitRounds(t, pacer, 1000)

	if len(clock.sleeps) != slept {
		t.Fatalf("rounds past the bound slept %v, want none", clock.sleeps[slept:])
	}
	clock.current = clock.current.Add(VoteMaxWait)
	waitRounds(t, pacer, 1)
	if last := clock.sleeps[len(clock.sleeps)-1]; last > roundInterval+time.Millisecond {
		t.Fatalf("the round after the backlog drained slept %s, want at most one interval %s: rounds started past the bound must give their slot back", last, roundInterval)
	}
}

// Test flow:
//  1. Build a pacer on the real sleep and take the burst, then cancel the context.
//  2. Wait once more.
//  3. Assert the wait returns the context's error.
func TestVotePacerReturnsTheContextErrorOfAnInterruptedWait(t *testing.T) {
	pacer := NewVotePacer(time.Now, SleepFor)
	for range VoteRoundBurst {
		if err := pacer.Wait(context.Background()); err != nil {
			t.Fatalf("Wait() = %v, want nil inside the burst", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := pacer.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() = %v, want %v", err, context.Canceled)
	}
}

// Test flow:
//  1. Build a pacer on a virtual clock whose sleep fails with the context's error, take the burst, then cancel the context.
//  2. Wait once and assert the wait returns the context's error.
//  3. Wait again on a sleep that advances the clock and assert it waits one interval, so the interrupted round gave its slot back.
func TestVotePacerGivesBackTheSlotOfAnInterruptedWait(t *testing.T) {
	clock := newPacedClock()
	interrupting := true
	pacer := NewVotePacer(clock.now, func(ctx context.Context, delay time.Duration) error {
		if interrupting {
			return ctx.Err()
		}
		return clock.sleep(ctx, delay)
	})
	waitRounds(t, pacer, VoteRoundBurst)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := pacer.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() = %v, want %v", err, context.Canceled)
	}
	interrupting = false
	waitRounds(t, pacer, 1)

	if last := clock.sleeps[len(clock.sleeps)-1]; last > roundInterval+time.Millisecond {
		t.Fatalf("the round after an interrupted one slept %s, want one interval %s", last, roundInterval)
	}
}

type stepPoster struct {
	steps []engine.TimeoutStep
}

func (p *stepPoster) VoteDeadline(uint64, time.Time) time.Time { return time.Time{} }

func (p *stepPoster) Voteable(uint64) bool { return true }

func (p *stepPoster) SettleTimeout(_ context.Context, step engine.TimeoutStep) (engine.TimeoutVote, error) {
	p.steps = append(p.steps, step)
	return engine.TimeoutVote{}, nil
}

func settleThrough(voter Voter, poster *stepPoster) {
	voter.Posters = func(string, any) (engine.TimeoutPoster, bool) { return poster, true }
	voter.Settle(context.Background(), "escrow-1", "model-a", user.InferenceParams{}, 7, false)
}

// Test flow:
//  1. Settle a failed probe through a voter with a pacer and then through one without.
//  2. Assert the step the first voter posted carries the pacer's wait and the second carries none.
//  3. Run the first step's wait and assert it took a round from the pacer.
func TestVoterHandsItsPacerToTheVoteItPosts(t *testing.T) {
	clock := newPacedClock()
	paced, unpaced := &stepPoster{}, &stepPoster{}

	settleThrough(Voter{Pace: NewVotePacer(clock.now, clock.sleep)}, paced)
	settleThrough(Voter{}, unpaced)

	if len(paced.steps) != 1 || paced.steps[0].Pace == nil {
		t.Fatalf("paced steps = %+v, want one carrying a wait", paced.steps)
	}
	if len(unpaced.steps) != 1 || unpaced.steps[0].Pace != nil {
		t.Fatalf("unpaced steps = %+v, want one carrying none", unpaced.steps)
	}
	if err := paced.steps[0].Pace(context.Background()); err != nil {
		t.Fatalf("Pace() = %v, want nil", err)
	}
}
