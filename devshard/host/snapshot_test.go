package host

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnmarshalStateSnapshot_LegacyRevealedSeedsIgnored(t *testing.T) {
	data := []byte(`{
		"state": {
			"escrowID": "escrow-1",
			"version": "v1",
			"phase": 1,
			"finalizeNonce": 7,
			"latestNonce": 9,
			"revealedSeeds": {
				"0": 123,
				"1": 456
			},
			"hostStats": {
				"0": {"missed": 1},
				"1": {"invalid": 2}
			},
			"warmKeys": {
				"0": "warm-0"
			}
		}
	}`)

	state, err := UnmarshalStateSnapshot(data)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, "escrow-1", state.EscrowID)
	require.Equal(t, uint64(7), state.FinalizeNonce)
	require.Equal(t, uint64(9), state.LatestNonce)
	require.Equal(t, "warm-0", state.WarmKeys[0])
	require.Equal(t, uint32(1), state.HostStats[0].Missed)
	require.Equal(t, uint32(2), state.HostStats[1].Invalid)
}
