package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDevshardTimeoutsForCreate(t *testing.T) {
	require.Equal(t, DefaultDevshardRefusalTimeout, DevshardRefusalTimeoutForCreate(nil))
	require.Equal(t, DefaultDevshardExecutionTimeout, DevshardExecutionTimeoutForCreate(nil))
	require.Equal(t, DefaultDevshardRefusalTimeout, DevshardRefusalTimeoutForCreate(&DevshardEscrowParams{}))
	require.Equal(t, DefaultDevshardExecutionTimeout, DevshardExecutionTimeoutForCreate(&DevshardEscrowParams{}))

	params := &DevshardEscrowParams{RefusalTimeout: 5, ExecutionTimeout: 17}
	require.Equal(t, int64(5), DevshardRefusalTimeoutForCreate(params))
	require.Equal(t, int64(17), DevshardExecutionTimeoutForCreate(params))
}

func TestDevshardTimeoutBlockTimeForCreate(t *testing.T) {
	require.Equal(t, DefaultDevshardTimeoutBlockTimeMs, DevshardTimeoutBlockTimeMsForCreate(nil))
	require.Equal(t, DefaultDevshardTimeoutBlockTimeMs, DevshardTimeoutBlockTimeMsForCreate(&DevshardEscrowParams{}))
	require.Equal(t, uint32(1000), DevshardTimeoutBlockTimeMsForCreate(&DevshardEscrowParams{TimeoutBlockTimeMs: 1000}))

	params := DefaultDevshardEscrowParams()
	params.TimeoutBlockTimeMs = 0
	require.NoError(t, params.Validate())
	params.TimeoutBlockTimeMs = MaxDevshardTimeoutBlockTimeMs + 1
	require.Error(t, params.Validate())
}
