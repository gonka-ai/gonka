package apiconfig_test

import (
	"testing"

	"decentralized-api/apiconfig"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestDevshardVersionsCacheFromParams_MapsPhase4Fields(t *testing.T) {
	dep := types.DefaultDevshardEscrowParams()
	dep.RefusalTimeout = 77
	dep.ExecutionTimeout = 999
	dep.ValidationRate = 4500
	dep.VoteThresholdFactor = 60

	cache := apiconfig.DevshardVersionsCacheFromParams(dep)
	require.Equal(t, int64(77), cache.RefusalTimeout)
	require.Equal(t, int64(999), cache.ExecutionTimeout)
	require.Equal(t, uint32(4500), cache.ValidationRate)
	require.Equal(t, uint32(60), cache.VoteThresholdFactor)
}

func TestDevshardVersionsCacheFromParams_NilReturnsEmpty(t *testing.T) {
	require.Equal(t, apiconfig.DevshardVersionsCache{}, apiconfig.DevshardVersionsCacheFromParams(nil))
}
