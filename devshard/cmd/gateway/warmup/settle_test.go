package warmup

import (
	"context"
	"errors"
	"testing"
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/registry"
	"devshard/user"
)

type stubPoster struct {
	vote  string
	err   error
	calls int
}

func (p *stubPoster) VoteDeadline(uint64, time.Time) time.Time { return time.Time{} }

func (p *stubPoster) SettleTimeout(context.Context, engine.TimeoutStep) (engine.TimeoutVote, error) {
	p.calls++
	return engine.TimeoutVote{Kind: p.vote}, p.err
}

type spyTimeouts struct {
	events []engine.TimeoutEvent
}

func (s *spyTimeouts) RecordTimeout(event engine.TimeoutEvent) {
	s.events = append(s.events, event)
}

func refusedWarmup(poster *stubPoster, timeouts *spyTimeouts, resolved bool) *Prober {
	return unfinishedWarmup(poster, timeouts, resolved, false)
}

func unfinishedWarmup(poster *stubPoster, timeouts *spyTimeouts, resolved, acknowledged bool) *Prober {
	return &Prober{
		escrows:  &stubEscrows{session: stubSession{}, live: true},
		ledger:   &spyLedger{},
		timeouts: timeouts,
		posters: func(string, any) (engine.TimeoutPoster, bool) {
			return poster, resolved
		},
		probe: func(_ context.Context, _ registry.EscrowSession, _ user.InferenceParams, nonceCommitted func()) (uint64, bool, error) {
			nonceCommitted()
			return 7, acknowledged, errors.New("host stopped answering")
		},
		catchUp: func(context.Context, registry.EscrowSession) error { return nil },
		stop:    make(chan struct{}),
		now:     warmupClock(),
	}
}

// Test flow:
//  1. Build a refused warmup whose probe fails and whose poster answers a "refused" vote.
//  2. Warm one escrow.
//  3. Assert `SettleTimeout` was called exactly once.
//  4. Assert two timeout events were recorded: a started one followed by a completed one naming the nonce the probe spent.
func TestARefusedWarmupProbeIsVotedOn(t *testing.T) {
	poster := &stubPoster{vote: "refused"}
	timeouts := &spyTimeouts{}

	refusedWarmup(poster, timeouts, true).warm("escrow-1", "model-a")

	if poster.calls != 1 {
		t.Fatalf("SettleTimeout calls = %d, want 1", poster.calls)
	}
	if len(timeouts.events) != 2 {
		t.Fatalf("recorded %d timeout events, want a started and a completed one: %+v", len(timeouts.events), timeouts.events)
	}
	if got := timeouts.events[0].Action; got != engine.TimeoutActionStarted {
		t.Errorf("first event action = %q, want %q", got, engine.TimeoutActionStarted)
	}
	if got := timeouts.events[1].Action; got != engine.TimeoutActionCompleted {
		t.Errorf("second event action = %q, want %q", got, engine.TimeoutActionCompleted)
	}
	if got := timeouts.events[1].Nonce; got != 7 {
		t.Errorf("event nonce = %d, want the nonce the probe spent", got)
	}
}

// Test flow:
//  1. Build an unfinished warmup whose probe commits a nonce, gets acknowledged, and then fails.
//  2. Warm one escrow.
//  3. Assert the recorded timeout event's kind is `TimeoutKindExecution`, since the host took the work before it hung.
func TestAReceiptedWarmupProbeIsVotedAnExecutionTimeout(t *testing.T) {
	poster := &stubPoster{vote: "refused"}
	timeouts := &spyTimeouts{}

	unfinishedWarmup(poster, timeouts, true, true).warm("escrow-1", "model-a")

	if len(timeouts.events) == 0 {
		t.Fatal("no timeout event was recorded for the probe")
	}
	if got := timeouts.events[0].Kind; got != engine.TimeoutKindExecution {
		t.Errorf("kind = %q, want %q: the probe was receipted", got, engine.TimeoutKindExecution)
	}
}

// Test flow:
//  1. Build an unfinished warmup whose probe commits a nonce with no acknowledgement and then fails.
//  2. Warm one escrow.
//  3. Assert the recorded timeout event's kind is `TimeoutKindRefused`, since no receipt was ever seen.
func TestAnUnreceiptedWarmupProbeStaysARefusal(t *testing.T) {
	timeouts := &spyTimeouts{}

	unfinishedWarmup(&stubPoster{vote: "refused"}, timeouts, true, false).warm("escrow-1", "model-a")

	if got := timeouts.events[0].Kind; got != engine.TimeoutKindRefused {
		t.Errorf("kind = %q, want %q: no receipt was seen", got, engine.TimeoutKindRefused)
	}
}

// Test flow:
//  1. Build a refused warmup, then replace its probe with one that commits a nonce and returns a served answer.
//  2. Warm one escrow.
//  3. Assert `SettleTimeout` was never called and no timeout event was recorded, since the host served it.
func TestAServedWarmupProbeIsNotVotedOn(t *testing.T) {
	poster := &stubPoster{}
	timeouts := &spyTimeouts{}
	warmup := refusedWarmup(poster, timeouts, true)
	warmup.probe = func(_ context.Context, _ registry.EscrowSession, _ user.InferenceParams, nonceCommitted func()) (uint64, bool, error) {
		nonceCommitted()
		return 7, true, nil
	}

	warmup.warm("escrow-1", "model-a")

	if poster.calls != 0 {
		t.Errorf("SettleTimeout calls = %d, want 0: the host served it", poster.calls)
	}
	if len(timeouts.events) != 0 {
		t.Errorf("recorded %+v, want no timeout event for a served probe", timeouts.events)
	}
}

// Test flow:
//  1. Build a refused warmup whose poster resolution fails to find one.
//  2. Warm one escrow.
//  3. Assert `SettleTimeout` was never called.
//  4. Assert one skipped timeout event was recorded with the reason `TimeoutReasonNoPoster`.
func TestAWarmupProbeWithNoPosterIsRecordedAsSkipped(t *testing.T) {
	poster := &stubPoster{}
	timeouts := &spyTimeouts{}

	refusedWarmup(poster, timeouts, false).warm("escrow-1", "model-a")

	if poster.calls != 0 {
		t.Errorf("SettleTimeout calls = %d, want 0: no poster resolved", poster.calls)
	}
	if len(timeouts.events) != 1 || timeouts.events[0].Action != engine.TimeoutActionSkipped {
		t.Fatalf("recorded %+v, want one skipped event", timeouts.events)
	}
	if got := timeouts.events[0].Reason; got != engine.TimeoutReasonNoPoster {
		t.Errorf("reason = %q, want %q", got, engine.TimeoutReasonNoPoster)
	}
}
