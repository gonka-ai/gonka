package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDevshardValidationRateForCreate(t *testing.T) {
	require.Equal(t, DefaultDevshardValidationRate, DevshardValidationRateForCreate(nil))
	require.Equal(t, DefaultDevshardValidationRate, DevshardValidationRateForCreate(&DevshardEscrowParams{}))
	require.Equal(t, uint32(3000), DevshardValidationRateForCreate(&DevshardEscrowParams{ValidationRate: 3000}))
	require.Equal(t, uint32(10000), DevshardValidationRateForCreate(&DevshardEscrowParams{ValidationRate: 10000}))
}
