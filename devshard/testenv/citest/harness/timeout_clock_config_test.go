package harness

import (
	"path/filepath"
	"testing"

	"devshard/testenv/mockchain/seed"

	"github.com/stretchr/testify/require"
)

func TestWriteMultiConfig_HeightsyncTimeoutClock(t *testing.T) {
	dir := t.TempDir()
	WriteMultiConfig(t, dir, MultiConfigOpts{Hosts: 2, EscrowSlots: 2})
	st, err := seed.Load(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	require.False(t, st.GetParams().DevshardEscrowParams.HeightsyncTimeoutClock)
	require.False(t, st.GetEscrow(1).HeightsyncTimeoutClock)

	header := true
	WriteMultiConfig(t, dir, MultiConfigOpts{
		Hosts:                  2,
		EscrowSlots:            2,
		HeightsyncTimeoutClock: &header,
	})
	st, err = seed.Load(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	require.True(t, st.GetParams().DevshardEscrowParams.HeightsyncTimeoutClock)
	require.True(t, st.GetEscrow(1).HeightsyncTimeoutClock)
}
