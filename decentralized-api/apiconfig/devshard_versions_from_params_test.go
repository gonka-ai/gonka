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

	cache := apiconfig.DevshardVersionsCacheFromParams(dep, nil)
	require.Equal(t, int64(77), cache.RefusalTimeout)
	require.Equal(t, int64(999), cache.ExecutionTimeout)
	require.Equal(t, uint32(4500), cache.ValidationRate)
	require.Equal(t, uint32(60), cache.VoteThresholdFactor)
}

func TestDevshardVersionsCacheFromParams_NilReturnsEmpty(t *testing.T) {
	require.Equal(t, apiconfig.DevshardVersionsCache{}, apiconfig.DevshardVersionsCacheFromParams(nil, nil))
}

func TestDevshardVersionsCacheFromParams_UsesApprovedListNotParamsField(t *testing.T) {
	dep := types.DefaultDevshardEscrowParams()
	dep.ApprovedVersions = []*types.DevshardApprovedVersion{{
		Name:   "from-params",
		Binary: "https://example.com/params.zip",
		Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	approved := []*types.DevshardApprovedVersion{{
		Name:   "from-query",
		Binary: "https://example.com/query.zip",
		Sha256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}}

	cache := apiconfig.DevshardVersionsCacheFromParams(dep, approved)
	require.Len(t, cache.Versions, 1)
	require.Equal(t, "from-query", cache.Versions[0].Name)
	require.Equal(t, "https://example.com/query.zip", cache.Versions[0].Binary)
}
