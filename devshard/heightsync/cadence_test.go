package heightsync_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
)

func TestComputeCadenceSwallow_ScenarioC(t *testing.T) {
	until, fe, ok := heightsync.ComputeCadenceSwallow(7, 10, 8, 4)
	require.True(t, ok)
	require.Equal(t, uint64(11), until)
	require.Equal(t, uint64(10), fe)
}

func TestComputeCadenceSwallow_ScenarioD_NoSwallow(t *testing.T) {
	_, _, ok := heightsync.ComputeCadenceSwallow(5, 8, 8, 4)
	require.False(t, ok)
}

// A large anchorK (e.g. from a crafted MsgForceHeightSyncTurn) makes i*anchorK
// overflow uint64; without the overflow guard the loop never terminates. Run it
// with a watchdog so a regression surfaces as a failure instead of a hung suite.
func TestComputeCadenceSwallow_LargeAnchorKTerminates(t *testing.T) {
	for _, anchorK := range []uint64{1 << 63, 1<<64 - 1, (1 << 63) + 7, 1 << 62} {
		anchorK := anchorK
		done := make(chan bool, 1)
		go func() {
			_, _, ok := heightsync.ComputeCadenceSwallow(7, 10, anchorK, 4)
			done <- ok
		}()
		select {
		case ok := <-done:
			// A window that starts at anchorK >= 1<<62 cannot intersect the
			// bounded forced range, so no suppression applies.
			require.False(t, ok, "anchorK=%d should not suppress", anchorK)
		case <-time.After(2 * time.Second):
			t.Fatalf("ComputeCadenceSwallow did not terminate for anchorK=%d", anchorK)
		}
	}
}
