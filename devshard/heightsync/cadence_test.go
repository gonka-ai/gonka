package heightsync_test

import (
	"testing"

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
