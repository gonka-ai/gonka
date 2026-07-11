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
