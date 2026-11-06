package headertime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDecide(t *testing.T) {
	// Inputs are a header time in milliseconds and a missing flag. Decide
	// has no Source and no oracle, so this test cannot call HeaderTimeMs,
	// AwaitHeaderTimeMs, or GetBlockHeader.
	stamp := time.UnixMilli(1_800_000_000_000)
	interval := 5 * time.Minute

	tests := []struct {
		name     string
		now      time.Time
		missing  bool
		interval time.Duration
		waited   time.Duration
		caller   Caller
		want     Decision
	}{
		{
			name:     "gateway one millisecond past the interval is due",
			now:      stamp.Add(interval + time.Millisecond),
			interval: interval,
			caller:   CallerGateway,
			want:     Decision{Outcome: OutcomeDue},
		},
		{
			name:     "verifier one millisecond past the interval is due",
			now:      stamp.Add(interval + time.Millisecond),
			interval: interval,
			caller:   CallerVerifier,
			want:     Decision{Outcome: OutcomeDue},
		},
		{
			name:     "gateway exactly at the interval is not due",
			now:      stamp.Add(interval),
			interval: interval,
			caller:   CallerGateway,
			want:     Decision{Outcome: OutcomeNotYet, Delta: 0},
		},
		{
			name:     "verifier exactly at the interval is not due",
			now:      stamp.Add(interval),
			interval: interval,
			caller:   CallerVerifier,
			want:     Decision{Outcome: OutcomeSchedule, Delta: 0},
		},
		{
			name:     "gateway with ten minutes left waits",
			now:      stamp,
			interval: 10 * time.Minute,
			caller:   CallerGateway,
			want:     Decision{Outcome: OutcomeNotYet, Delta: 10 * time.Minute},
		},
		{
			name:     "verifier with fifty nine seconds left schedules once",
			now:      stamp.Add(interval - 59*time.Second),
			interval: interval,
			caller:   CallerVerifier,
			want:     Decision{Outcome: OutcomeSchedule, Delta: 59 * time.Second},
		},
		{
			name:     "verifier with sixty seconds left is ignored",
			now:      stamp.Add(interval - AnswerWait),
			interval: interval,
			caller:   CallerVerifier,
			want:     Decision{Outcome: OutcomeIgnore},
		},
		{
			name:     "gateway missing at the start stays missing",
			now:      stamp.Add(time.Hour),
			missing:  true,
			interval: interval,
			caller:   CallerGateway,
			want:     Decision{Outcome: OutcomeMissing},
		},
		{
			name:     "gateway missing after sixty seconds stays missing",
			now:      stamp.Add(time.Hour),
			missing:  true,
			interval: interval,
			waited:   AnswerWait,
			caller:   CallerGateway,
			want:     Decision{Outcome: OutcomeMissing},
		},
		{
			name:     "verifier missing at the start waits",
			missing:  true,
			interval: interval,
			now:      stamp,
			caller:   CallerVerifier,
			want:     Decision{Outcome: OutcomeWaiting},
		},
		{
			name:     "verifier missing just under sixty seconds still waits",
			missing:  true,
			interval: interval,
			now:      stamp,
			waited:   AnswerWait - time.Millisecond,
			caller:   CallerVerifier,
			want:     Decision{Outcome: OutcomeWaiting},
		},
		{
			name:     "verifier missing at sixty seconds is behind",
			missing:  true,
			interval: interval,
			now:      stamp,
			waited:   AnswerWait,
			caller:   CallerVerifier,
			want:     Decision{Outcome: OutcomeFeedBehind},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.now, stamp.UnixMilli(), tt.missing, tt.interval, tt.waited, tt.caller)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDecideFarFutureStampDoesNotWrapTheDelta(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	// Far enough ahead that the gap does not fit in a Duration.
	stampMs := now.UnixMilli() + 400*365*24*3600*1000

	got := Decide(now, stampMs, false, time.Minute, 0, CallerVerifier)
	require.Equal(t, OutcomeIgnore, got.Outcome)
	require.Zero(t, got.Delta)

	got = Decide(now, stampMs, false, time.Minute, 0, CallerGateway)
	require.Equal(t, OutcomeNotYet, got.Outcome)
	require.GreaterOrEqual(t, got.Delta, AnswerWait)
}
