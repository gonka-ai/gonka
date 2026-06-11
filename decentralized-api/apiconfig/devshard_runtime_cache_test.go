package apiconfig_test

import (
	"sync"
	"testing"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func newTestConfigManager() *apiconfig.ConfigManager {
	return &apiconfig.ConfigManager{}
}

func TestDevshardRuntimeCache_DefaultsZero(t *testing.T) {
	cm := newTestConfigManager()
	got := cm.GetDevshardVersions()
	require.Empty(t, got.Versions)
	require.False(t, got.DevshardRequestsEnabled)
	require.Equal(t, uint32(0), got.MaxNonce)
}

func TestDevshardRuntimeCache_SetGetRoundTrip(t *testing.T) {
	cm := newTestConfigManager()
	cache := apiconfig.DevshardVersionsCache{
		Versions: []apiconfig.DevshardVersion{
			{Name: "v1", Binary: "https://example/v1", SHA256: "abc"},
		},
		DevshardRequestsEnabled: true,
		MaxNonce:                20000,
		ValidationRate:          5000,
	}
	cm.SetDevshardVersions(cache)

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			got := cm.GetDevshardVersions()
			require.Len(t, got.Versions, 1)
			require.Equal(t, "v1", got.Versions[0].Name)
			require.True(t, got.DevshardRequestsEnabled)
			require.Equal(t, uint32(20000), got.MaxNonce)
			require.Equal(t, uint32(5000), got.ValidationRate)
		}()
	}
	wg.Wait()
}
