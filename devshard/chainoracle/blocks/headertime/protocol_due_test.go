package headertime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDue_LegacyDeadlineAddsIntervalAndExtra(t *testing.T) {
	anchor := int64(1_700_000_000)
	now := time.Unix(anchor, 0).Add(9 * time.Second)
	deadline, kind, armed := Due(ClockDueInput{
		Reason:     KindRefused,
		Now:        now,
		WallAnchor: anchor,
		Interval:   5 * time.Second,
		Extra:      5 * time.Second,
		Caller:     CallerGateway,
	})
	want := time.Unix(anchor, 0).Add(10 * time.Second)
	require.True(t, armed)
	require.Equal(t, KindRefused, kind)
	require.Equal(t, want, deadline)
	require.True(t, now.Before(deadline), "one second before the deadline is not due")

	onDeadline := want
	require.False(t, onDeadline.Before(deadline), "the deadline instant is due")
}

func TestDue_LegacyZeroAnchorIsUnarmed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	deadline, _, armed := Due(ClockDueInput{
		Reason:     KindRefused,
		Now:        now,
		WallAnchor: 0,
		Interval:   5 * time.Second,
		Extra:      5 * time.Second,
		Caller:     CallerGateway,
	})
	require.False(t, armed)
	require.True(t, deadline.IsZero())
	require.False(t, deadline.Equal(time.Unix(0, 0)), "a missing anchor is not the Unix epoch")
}

func TestDue_LegacyVerifierExtraZeroMatchesWholeSeconds(t *testing.T) {
	anchor := int64(1_700_000_000)
	interval := 5 * time.Second
	deadline, _, armed := Due(ClockDueInput{
		Reason:     KindExecution,
		Now:        time.Unix(anchor, 0),
		WallAnchor: anchor,
		Interval:   interval,
		Extra:      0,
		Caller:     CallerVerifier,
	})
	require.True(t, armed)
	require.Equal(t, time.Unix(anchor, 0).Add(interval), deadline)

	before := time.Unix(anchor, 0).Add(interval - time.Second)
	require.True(t, before.Before(deadline))
	require.Less(t, before.Unix()-anchor, int64(interval/time.Second))

	on := time.Unix(anchor, 0).Add(interval)
	require.False(t, on.Before(deadline))
	require.GreaterOrEqual(t, on.Unix()-anchor, int64(interval/time.Second))
}

func TestDue_HeaderMissingStaysAheadOfAPastWall(t *testing.T) {
	now := time.Unix(1_700_000_100, 0)
	deadline, _, armed := Due(ClockDueInput{
		HeightsyncTimeoutClock: true,
		Reason:                 KindRefused,
		Now:                    now,
		WallAnchor:             now.Add(-time.Hour).Unix(),
		StampMissing:           true,
		Interval:               5 * time.Second,
		Extra:                  5 * time.Second,
		Caller:                 CallerGateway,
	})
	require.True(t, armed)
	require.Equal(t, now.Add(FeedPoll), deadline)
	require.True(t, now.Before(deadline), "a past wall does not open a missing stamp")
}

func TestDue_HeaderStampDueIgnoresExtra(t *testing.T) {
	interval := 5 * time.Second
	stamp := time.UnixMilli(1_800_000_000_000)
	now := stamp.Add(interval + time.Second)
	deadline, kind, armed := Due(ClockDueInput{
		HeightsyncTimeoutClock: true,
		Reason:                 KindExecution,
		Now:                    now,
		WallAnchor:             now.Unix(),
		StampTimeMs:            stamp.UnixMilli(),
		Interval:               interval,
		Extra:                  5 * time.Second,
		Caller:                 CallerGateway,
	})
	require.True(t, armed)
	require.Equal(t, KindExecution, kind)
	require.Equal(t, now.Add(-time.Millisecond), deadline)
	require.False(t, now.Before(deadline))
}

func TestDue_VerifierUnixEpochWallIsDueOnTheDeadline(t *testing.T) {
	interval := 5 * time.Second
	deadline, _, armed := Due(ClockDueInput{
		Reason:   KindRefused,
		Now:      time.Unix(0, 0).Add(interval),
		Wall:     time.Unix(0, 0),
		Interval: interval,
		Caller:   CallerVerifier,
	})
	require.True(t, armed)
	require.Equal(t, time.Unix(0, 0).Add(interval), deadline)
	on := time.Unix(0, 0).Add(interval)
	require.False(t, on.Before(deadline), "a signed anchor of 0 is the epoch and is due on the deadline")
	require.True(t, on.Add(-time.Second).Before(deadline))
}

func TestDue_LegacyMissingWallIgnoresADueStamp(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	deadline, _, armed := Due(ClockDueInput{
		Reason:      KindRefused,
		Now:         now,
		WallAnchor:  0,
		StampTimeMs: now.Add(-time.Hour).UnixMilli(),
		Interval:    5 * time.Second,
		Caller:      CallerGateway,
	})
	require.False(t, armed)
	require.True(t, deadline.IsZero())
}

func TestDue_HeaderStampNotDueWhileWallIsPast(t *testing.T) {
	interval := 5 * time.Minute
	stamp := time.UnixMilli(1_800_000_000_000)
	now := stamp.Add(interval - time.Minute)
	deadline, _, armed := Due(ClockDueInput{
		HeightsyncTimeoutClock: true,
		Reason:                 KindRefused,
		Now:                    now,
		Wall:                   now.Add(-time.Hour),
		StampTimeMs:            stamp.UnixMilli(),
		Interval:               interval,
		Caller:                 CallerGateway,
	})
	require.True(t, armed)
	require.True(t, now.Before(deadline), "the stamp is still ahead")
}

func TestDue_HeaderStampDueWhileWallIsAhead(t *testing.T) {
	interval := 5 * time.Second
	stamp := time.UnixMilli(1_800_000_000_000)
	now := stamp.Add(interval + time.Second)
	deadline, _, armed := Due(ClockDueInput{
		HeightsyncTimeoutClock: true,
		Reason:                 KindExecution,
		Now:                    now,
		Wall:                   now.Add(time.Hour),
		StampTimeMs:            stamp.UnixMilli(),
		Interval:               interval,
		Extra:                  5 * time.Second,
		Caller:                 CallerVerifier,
	})
	require.True(t, armed)
	require.False(t, now.Before(deadline), "a due stamp opens while the wall is still ahead")
}

func TestDue_DoesNotReadTheClock(t *testing.T) {
	anchor := int64(1_700_000_000)
	// A now far from time.Now() still produces the anchor deadline.
	now := time.Unix(anchor, 0).Add(24 * time.Hour)
	deadline, _, armed := Due(ClockDueInput{
		Now:        now,
		WallAnchor: anchor,
		Interval:   time.Second,
		Caller:     CallerGateway,
	})
	require.True(t, armed)
	require.Equal(t, time.Unix(anchor, 0).Add(time.Second), deadline)
	require.NotEqual(t, time.Now().Truncate(time.Second), deadline)
}
