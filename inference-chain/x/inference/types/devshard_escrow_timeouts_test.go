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
