package apiconfig_test

import (
	"sync"
	"testing"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func TestConfigManager_RuntimeConfigSnapshot_FieldsPopulated(t *testing.T) {
	cm := newTestConfigManager()
	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{
		LogprobsMode: "full",
	}))
	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{
		Versions: []apiconfig.DevshardVersion{
			{Name: "v2", Binary: "https://example/v2", SHA256: "deadbeef"},
		},
		DevshardRequestsEnabled: false,
		MaxNonce:                1000,
	})
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(42_000, 7))

	snap := cm.RuntimeConfigSnapshot(7)
	require.Equal(t, int64(42_000), snap.ParamsBlockHeight)
	require.Equal(t, uint64(7), snap.CurrentEpochID)
	require.Equal(t, "full", snap.LogprobsMode)
	require.False(t, snap.DevshardRequestsEnabled)
	require.Equal(t, uint32(1000), snap.MaxNonce)
	require.Len(t, snap.ApprovedVersions, 1)
	require.Equal(t, "v2", snap.ApprovedVersions[0].Name)
	require.False(t, snap.ServedAt.IsZero())
}

func TestConfigManager_RuntimeConfigSnapshot_StaleUntilPublish(t *testing.T) {
	cm := newTestConfigManager()
	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "raw"}))
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(100, 0))

	snapPublished := cm.RuntimeConfigSnapshot(0)
	require.Equal(t, "raw", snapPublished.LogprobsMode)
	require.Equal(t, int64(100), snapPublished.ParamsBlockHeight)

	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "full"}))
	snapBeforePublish := cm.RuntimeConfigSnapshot(0)
	require.Equal(t, "raw", snapBeforePublish.LogprobsMode, "snapshot must not expose cache ahead of publish")
	require.Equal(t, int64(100), snapBeforePublish.ParamsBlockHeight)

	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(101, 0))
	snapAfterPublish := cm.RuntimeConfigSnapshot(0)
	require.Equal(t, "full", snapAfterPublish.LogprobsMode)
	require.Equal(t, int64(101), snapAfterPublish.ParamsBlockHeight)
}

func TestConfigManager_RuntimeConfigSnapshot_ParamsBlockHeightMonotonic(t *testing.T) {
	cm := newTestConfigManager()

	s0 := cm.RuntimeConfigSnapshot(0)
	require.Equal(t, int64(0), s0.ParamsBlockHeight)

	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "full"}))
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(100, 0))
	s1 := cm.RuntimeConfigSnapshot(0)
	require.Equal(t, int64(100), s1.ParamsBlockHeight)

	// Setters alone do not advance the generation marker.
	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "raw"}))
	s2 := cm.RuntimeConfigSnapshot(0)
	require.Equal(t, int64(100), s2.ParamsBlockHeight)

	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(101, 0))
	s3 := cm.RuntimeConfigSnapshot(0)
	require.Equal(t, int64(101), s3.ParamsBlockHeight)

	// Height does not move backward on publish.
	require.False(t, cm.ApplyRuntimeConfigBlockIfChanged(50, 0))
	s4 := cm.RuntimeConfigSnapshot(0)
	require.Equal(t, int64(101), s4.ParamsBlockHeight)
}

func TestConfigManager_RuntimeConfigSnapshot_ConcurrentSafe(t *testing.T) {
	cm := newTestConfigManager()
	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "raw"}))

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			mode := "raw"
			if i%2 == 0 {
				mode = "full"
			}
			_ = cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: mode})
			cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{
				MaxNonce: uint32(i + 1),
			})
			cm.ApplyRuntimeConfigBlockIfChanged(int64(1000+i), uint64(i))
		}(i)

		go func(epoch uint64) {
			defer wg.Done()
			snap := cm.RuntimeConfigSnapshot(epoch)
			_ = snap.ParamsBlockHeight
			_ = snap.LogprobsMode
			_ = snap.MaxNonce
		}(uint64(i))
	}
	wg.Wait()
}
