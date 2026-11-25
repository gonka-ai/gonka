package warmup

import (
	"context"
	"time"

	"golang.org/x/time/rate"
)

// Pacing bounds of a probe's vote round. See README.md, "Vote pacing".
const (
	VoteRoundsPerSecond rate.Limit = 10
	VoteRoundBurst                 = 20
	VoteMaxWait                    = 2 * time.Minute
	voteRoundCost                  = 1
)

// VotePacer spaces the vote rounds of failed probes, so the verify calls a round fans out stay within what a verifier host grants this gateway while the backlog is under VoteMaxWait.
type VotePacer struct {
	limiter *rate.Limiter
	now     func() time.Time
	sleep   func(ctx context.Context, delay time.Duration) error
}

func NewVotePacer(now func() time.Time, sleep func(ctx context.Context, delay time.Duration) error) *VotePacer {
	return &VotePacer{limiter: rate.NewLimiter(VoteRoundsPerSecond, VoteRoundBurst), now: now, sleep: sleep}
}

// Wait returns when a round may start, or with the context's error; a round that would wait longer than VoteMaxWait starts at once.
func (p *VotePacer) Wait(ctx context.Context) error {
	reservation := p.limiter.ReserveN(p.now(), voteRoundCost)
	delay := reservation.DelayFrom(p.now())
	if delay > VoteMaxWait {
		reservation.CancelAt(p.now())
		return nil
	}
	if err := p.sleep(ctx, delay); err != nil {
		reservation.CancelAt(p.now())
		return err
	}
	return nil
}

// SleepFor waits for delay on the real clock and returns early with the context's error.
func SleepFor(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
