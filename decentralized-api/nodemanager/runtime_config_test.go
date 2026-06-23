package nodemanager

import (
	"context"
	"testing"

	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"devshard/nodemanager/gen"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func testEpochParams() types.EpochParams {
	return types.EpochParams{
		EpochLength:           100,
		EpochShift:            0,
		EpochMultiplier:       1,
		PocStageDuration:      20,
		PocExchangeDuration:   2,
		PocValidationDelay:    2,
		PocValidationDuration: 10,
	}
}

func testPhaseTrackerWithEpoch(epochIndex uint64) *chainphase.ChainPhaseTracker {
	pt := &chainphase.ChainPhaseTracker{}
	epoch := types.Epoch{Index: epochIndex, PocStartBlockHeight: 1}
	params := testEpochParams()
	pt.Update(chainphase.BlockInfo{Height: 50, Hash: "h"}, &epoch, &params, true, nil)
	return pt
}

func testConfigManager(t *testing.T) *apiconfig.ConfigManager {
	t.Helper()
	cm := &apiconfig.ConfigManager{}
	cm.EnsureRuntimeConfigNotifier()
	return cm
}

func populateRuntimeConfig(t *testing.T, cm *apiconfig.ConfigManager, height int64, epochID uint64) {
	t.Helper()
	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "full"}))
	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{
		Versions: []apiconfig.DevshardVersion{
			{Name: "v1", Binary: "https://example/v1", SHA256: "abc123"},
		},
		DevshardRequestsEnabled: true,
		MaxNonce:                20000,
		RefusalTimeout:                    60,
		ExecutionTimeout:                  1200,
		ValidationRate:                    5000,
		VoteThresholdFactor:               50,
	})
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(height, epochID))
}

func TestNodeManager_GetRuntimeConfig_FullResponse(t *testing.T) {
	cm := testConfigManager(t)
	pt := testPhaseTrackerWithEpoch(7)
	populateRuntimeConfig(t, cm, 100, 7)

	srv := NewServer(&mockBroker{}, cm, pt)
	resp, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 0,
	})
	require.NoError(t, err)
	require.False(t, resp.Unchanged)
	require.NotNil(t, resp.Config)
	require.Equal(t, int64(100), resp.Config.ParamsBlockHeight)
	require.Equal(t, uint64(7), resp.Config.CurrentEpochId)
	require.Equal(t, "full", resp.Config.LogprobsMode)
	require.True(t, resp.Config.DevshardRequestsEnabled)
	require.Equal(t, uint32(20000), resp.Config.MaxNonce)
	require.Len(t, resp.Config.ApprovedVersions, 1)
	require.Equal(t, "v1", resp.Config.ApprovedVersions[0].Name)
	require.NotZero(t, resp.Config.ServedAtUnix)
	require.Equal(t, int64(60), resp.Config.RefusalTimeout)
	require.Equal(t, int64(1200), resp.Config.ExecutionTimeout)
	require.Equal(t, uint32(5000), resp.Config.ValidationRate)
	require.Equal(t, uint32(50), resp.Config.VoteThresholdFactor)
}

func TestNodeManager_GetRuntimeConfig_Unchanged(t *testing.T) {
	cm := testConfigManager(t)
	pt := testPhaseTrackerWithEpoch(3)
	populateRuntimeConfig(t, cm, 100, 3)

	srv := NewServer(&mockBroker{}, cm, pt)
	resp, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 100,
	})
	require.NoError(t, err)
	require.True(t, resp.Unchanged)
	require.Nil(t, resp.Config)
}

func TestNodeManager_GetRuntimeConfig_ChangedAfterNewBlock(t *testing.T) {
	cm := testConfigManager(t)
	pt := testPhaseTrackerWithEpoch(5)
	populateRuntimeConfig(t, cm, 100, 5)

	srv := NewServer(&mockBroker{}, cm, pt)

	unchanged, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 100,
	})
	require.NoError(t, err)
	require.True(t, unchanged.Unchanged)

	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "raw"}))
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(101, 5))
	updated, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 100,
	})
	require.NoError(t, err)
	require.False(t, updated.Unchanged)
	require.Equal(t, int64(101), updated.Config.ParamsBlockHeight)
}

func TestNodeManager_GetRuntimeConfig_NilEpochTracker_ZeroEpochID(t *testing.T) {
	cm := testConfigManager(t)
	populateRuntimeConfig(t, cm, 50, 0)

	srv := NewServer(&mockBroker{}, cm, nil)
	resp, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{})
	require.NoError(t, err)
	require.False(t, resp.Unchanged)
	require.Equal(t, uint64(0), resp.Config.CurrentEpochId)
}

func TestNodeManager_GetRuntimeConfig_NoConfigManager(t *testing.T) {
	srv := NewServer(&mockBroker{}, nil, nil)
	_, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestNodeManager_AcquireStillWorks(t *testing.T) {
	srv := NewServer(&mockBroker{
		acquireFunc: func(_ context.Context, _ string, _ []string) (string, string, string, error) {
			return "lock-1", "http://node/v1", "node-a", nil
		},
	}, testConfigManager(t), testPhaseTrackerWithEpoch(1))
	resp, err := srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{Model: "m"})
	require.NoError(t, err)
	require.Equal(t, "lock-1", resp.LockId)
}
