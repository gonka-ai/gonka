package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// Legacy tags (floorDriver pins v6) still accept observed_time_ms = 0.
func TestObservedTimeMsZeroStillApplies(t *testing.T) {
	d := newFloorDriver(t)
	require.False(t, types.StampRequiresTime(d.sm.ProtocolVersion()))
	hash := []byte{0x01, 0x00}
	d.raise(100, hash)
	id := d.next()

	start := divStartTx(id, 100, hash)
	require.Zero(t, start.GetStartInference().GetObservedTimeMs())
	require.NoError(t, d.apply(start))

	confirm := d.confirm(id, 100, hash)
	require.Zero(t, confirm.GetConfirmStart().GetObservedTimeMs())
	require.NoError(t, d.apply(confirm))

	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status)
}
