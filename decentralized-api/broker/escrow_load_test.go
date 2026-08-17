package broker_test

import (
	"testing"
	"time"

	"decentralized-api/broker"

	"github.com/stretchr/testify/require"
)

func TestEscrowLoadTracker_RollingWindow(t *testing.T) {
	tr := broker.NewEscrowLoadTracker(30 * time.Minute)
	now := time.Unix(1_700_000_000, 0)
	tr.SetNowForTest(func() time.Time { return now })

	for i := 0; i < 30; i++ {
		tr.Record("42")
	}
	snap := tr.Snapshot()
	require.Len(t, snap, 1)
	require.Equal(t, uint64(42), snap[0].EscrowID)
	require.InDelta(t, 1.0, snap[0].RequestsPerMin, 1e-9) // 30 / 30m

	// Age out: advance past the window.
	now = now.Add(31 * time.Minute)
	snap = tr.Snapshot()
	require.Empty(t, snap)
}

func TestEscrowLoadTracker_DecaysWithinWindow(t *testing.T) {
	tr := broker.NewEscrowLoadTracker(30 * time.Minute)
	now := time.Unix(1_700_000_000, 0)
	tr.SetNowForTest(func() time.Time { return now })

	// Burst of 30 at t0 → weighted count 30 → rate 1.0/min.
	for i := 0; i < 30; i++ {
		tr.Record("42")
	}
	require.InDelta(t, 1.0, tr.Snapshot()[0].RequestsPerMin, 1e-9)

	// Half a window later with no new acquires, the weighted count decays by
	// exp(-0.5); the escrow is still active (last acquire within window).
	now = now.Add(15 * time.Minute)
	snap := tr.Snapshot()
	require.Len(t, snap, 1)
	require.InDelta(t, 1.0*0.60653066, snap[0].RequestsPerMin, 1e-6)

	// Past the window since the last acquire → idle → evicted.
	now = now.Add(16 * time.Minute)
	require.Empty(t, tr.Snapshot())
}

func TestEscrowLoadTracker_IgnoresEmptyAndNonNumeric(t *testing.T) {
	tr := broker.NewEscrowLoadTracker(time.Minute)
	tr.Record("")
	tr.Record("not-a-number")
	tr.Record("escrow-1")
	require.Empty(t, tr.Snapshot())
}

func TestEscrowLoadTracker_OmitsIdleEscrow(t *testing.T) {
	tr := broker.NewEscrowLoadTracker(30 * time.Minute)
	now := time.Unix(1_700_000_000, 0)
	tr.SetNowForTest(func() time.Time { return now })

	tr.Record("1")
	tr.Record("2")
	require.Len(t, tr.Snapshot(), 2)

	now = now.Add(31 * time.Minute)
	tr.Record("2") // only 2 is active again
	snap := tr.Snapshot()
	require.Len(t, snap, 1)
	require.Equal(t, uint64(2), snap[0].EscrowID)
}
