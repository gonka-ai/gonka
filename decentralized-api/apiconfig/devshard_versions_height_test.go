package apiconfig_test

import (
	"sync"
	"testing"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func devshardCatalog(sha string) apiconfig.DevshardVersionsCache {
	return apiconfig.DevshardVersionsCache{
		Versions: []apiconfig.DevshardVersion{
			{Name: "v4", Binary: "https://example/v4", SHA256: sha},
		},
	}
}

func TestApplyDevshardVersionsIfNewer_RejectsOlderHeight(t *testing.T) {
	cm := newTestConfigManager()

	require.True(t, cm.ApplyDevshardVersionsIfNewer(devshardCatalog("aaa"), 100))
	require.Equal(t, "aaa", cm.GetDevshardVersions().Versions[0].SHA256)

	require.True(t, cm.ApplyDevshardVersionsIfNewer(devshardCatalog("bbb"), 101))
	require.Equal(t, "bbb", cm.GetDevshardVersions().Versions[0].SHA256)

	require.False(t, cm.ApplyDevshardVersionsIfNewer(devshardCatalog("aaa"), 100))
	require.Equal(t, "bbb", cm.GetDevshardVersions().Versions[0].SHA256)
}

func TestApplyDevshardVersionsIfNewer_AcceptsSameHeight(t *testing.T) {
	cm := newTestConfigManager()

	require.True(t, cm.ApplyDevshardVersionsIfNewer(devshardCatalog("aaa"), 100))
	require.True(t, cm.ApplyDevshardVersionsIfNewer(devshardCatalog("bbb"), 100))
	require.Equal(t, "bbb", cm.GetDevshardVersions().Versions[0].SHA256)
}

func TestApplyDevshardVersionsIfNewer_StaleWorkerCannotRegressPublishedContent(t *testing.T) {
	cm := newTestConfigManager()
	cm.EnsureRuntimeConfigNotifier()

	require.True(t, cm.ApplyDevshardVersionsIfNewer(devshardCatalog("aaa"), 100))
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(100, 7))

	require.True(t, cm.ApplyDevshardVersionsIfNewer(devshardCatalog("bbb"), 101))
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(101, 7))

	require.False(t, cm.ApplyDevshardVersionsIfNewer(devshardCatalog("aaa"), 100))
	require.False(t, cm.ApplyRuntimeConfigBlockIfChanged(100, 7))

	snap := cm.RuntimeConfigSnapshot(7)
	require.Equal(t, int64(101), snap.ParamsBlockHeight)
	require.Equal(t, "bbb", snap.ApprovedVersions[0].SHA256)
	require.Equal(t, "bbb", cm.GetDevshardVersions().Versions[0].SHA256)
}

func TestApplyRuntimeConfigBlockIfChanged_StaleHeightDoesNotPublishChangedContent(t *testing.T) {
	cm := newTestConfigManager()
	cm.EnsureRuntimeConfigNotifier()

	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "full"}))
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(101, 7))

	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "raw"}))
	require.False(t, cm.ApplyRuntimeConfigBlockIfChanged(100, 7))

	snap := cm.RuntimeConfigSnapshot(7)
	require.Equal(t, int64(101), snap.ParamsBlockHeight)
	require.Equal(t, "full", snap.LogprobsMode)
}

func TestApplyDevshardVersionsIfNewer_ConcurrentOutOfOrderKeepsNewest(t *testing.T) {
	cm := newTestConfigManager()
	require.True(t, cm.ApplyDevshardVersionsIfNewer(devshardCatalog("bbb"), 101))

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cm.ApplyDevshardVersionsIfNewer(devshardCatalog("aaa"), 100)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cm.GetDevshardVersions()
		}()
	}
	wg.Wait()

	require.Equal(t, "bbb", cm.GetDevshardVersions().Versions[0].SHA256)
}
