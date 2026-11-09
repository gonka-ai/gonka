package headertime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCompareLegacyShadowMissingIsNotAMismatch(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	anchor := now.Add(-time.Minute)
	wall := anchor.Add(time.Minute)
	got := CompareLegacyShadow(now, anchor, wall, 0, true)
	require.Equal(t, LegacyShadow{Cause: "stamp_missing"}, got)
}

func TestCompareLegacyShadowEqualAnchorsAgree(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	anchor := now.Add(-time.Hour)
	wall := anchor.Add(time.Minute)
	got := CompareLegacyShadow(now, anchor, wall, anchor.UnixMilli(), false)
	require.Equal(t, LegacyShadow{StampDue: true}, got)
}

func TestCompareLegacyShadowFewSecondsAgreeOnBothSlacks(t *testing.T) {
	anchor := time.Unix(1_700_000_000, 0)
	interval := time.Minute
	stamp := anchor.Add(-3 * time.Second)
	gatewayWall := anchor.Add(interval + LegacyShadowEpsilon)
	verifierWall := anchor.Add(interval)

	gateway := CompareLegacyShadow(gatewayWall, anchor, gatewayWall, stamp.UnixMilli(), false)
	verifier := CompareLegacyShadow(verifierWall, anchor, verifierWall, stamp.UnixMilli(), false)
	require.Equal(t, LegacyShadow{StampDue: true}, gateway)
	require.Equal(t, gateway, verifier)
}

func TestCompareLegacyShadowSameSkewSameCause(t *testing.T) {
	anchor := time.Unix(1_700_000_000, 0)
	interval := time.Minute
	stamp := anchor.Add(-10 * time.Second)
	gatewayWall := anchor.Add(interval + LegacyShadowEpsilon)
	verifierWall := anchor.Add(interval)
	want := LegacyShadow{Cause: "stamp_early", StampDue: true, Mismatch: true}

	gateway := CompareLegacyShadow(gatewayWall, anchor, gatewayWall, stamp.UnixMilli(), false)
	verifier := CompareLegacyShadow(verifierWall, anchor, verifierWall, stamp.UnixMilli(), false)
	require.Equal(t, want, gateway)
	require.Equal(t, gateway, verifier)
}

func TestCompareLegacyShadowStampLate(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	anchor := now.Add(-time.Hour)
	wall := anchor.Add(time.Minute)
	stamp := anchor.Add(time.Hour)
	got := CompareLegacyShadow(now, anchor, wall, stamp.UnixMilli(), false)
	require.Equal(t, LegacyShadow{Cause: "stamp_late", Mismatch: true}, got)
}

func TestCompareLegacyShadowBothDueWhenTheSampleIsLate(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	wall := now.Add(-2 * time.Hour)
	anchor := wall.Add(-time.Minute)
	stamp := anchor.Add(-time.Hour)
	got := CompareLegacyShadow(now, anchor, wall, stamp.UnixMilli(), false)
	require.Equal(t, LegacyShadow{Cause: "both_due", StampDue: true, Mismatch: true}, got)
}
