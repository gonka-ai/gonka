package shadow

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/internal/leakcheck"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/warmup"
	"devshard/host"
	"devshard/user"
)

type blockingPoster struct {
	entered chan struct{}
	proceed chan struct{}
}

func (p *blockingPoster) VoteDeadline(uint64, time.Time) time.Time { return time.Time{} }

func (p *blockingPoster) Voteable(uint64) bool { return true }

func (p *blockingPoster) SettleTimeout(context.Context, engine.TimeoutStep) (engine.TimeoutVote, error) {
	close(p.entered)
	<-p.proceed
	return engine.TimeoutVote{Kind: engine.TimeoutKindRefused}, nil
}

// Test flow:
//  1. Bind a runner whose send fails and whose vote poster blocks until released.
//  2. Probe a committed nonce and assert the failed result is reported while the vote is still waiting.
//  3. Assert by then the quiet settle is armed, the request hold is given back and a vote hold keeps the escrow open.
//  4. Let the vote return and assert the vote hold is released and the quiet settle is armed again, for what the vote queued.
func TestFailedProbeIsReportedBeforeItsVoteReturns(t *testing.T) {
	defer leakcheck.VerifyNoneStarted(t)()
	fixture := newFixture(t, func(context.Context, registry.EscrowSession, *user.PreparedInference, io.Writer, func()) (*host.HostResponse, error) {
		return nil, errors.New("connection refused")
	})
	poster := &blockingPoster{entered: make(chan struct{}), proceed: make(chan struct{})}
	proceed := sync.OnceFunc(func() { close(poster.proceed) })
	defer proceed()
	fixture.runner.Bind(Binding{
		Escrows:     fixture.escrows,
		Lifetime:    t.Context(),
		Voter:       warmup.Voter{Posters: func(string, any) (engine.TimeoutPoster, bool) { return poster, true }},
		Probes:      fixture.probes,
		Outcomes:    fixture.outcomes,
		HardTimeout: func() time.Duration { return fixture.hardTime },
	})

	fixture.runner.Probe(fixture.request())
	result := fixture.awaitReported(t)
	select {
	case <-poster.entered:
	case <-time.After(awaitBudget):
		t.Fatal("timed out waiting for the failed probe's vote to start")
	}

	if result.Outcome != OutcomeFailed {
		t.Fatalf("result = %+v, want a failed probe", result)
	}
	if fixture.session.armed.Load() != 1 || fixture.escrows.released.Load() != 1 || fixture.escrows.settling.Load() != 1 || fixture.escrows.settled.Load() != 0 {
		t.Errorf("quiet settle armed %d, request holds released %d, vote holds taken %d and released %d, want 1, 1, 1 and 0 while the vote waits",
			fixture.session.armed.Load(), fixture.escrows.released.Load(), fixture.escrows.settling.Load(), fixture.escrows.settled.Load())
	}
	proceed()
	fixture.awaitVote(t)
	if fixture.session.armed.Load() != 2 {
		t.Errorf("quiet settle armed %d times after the vote, want 2", fixture.session.armed.Load())
	}
}
