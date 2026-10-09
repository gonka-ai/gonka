package apiconfig_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func TestDevshardVersionOverridesExactMatch(t *testing.T) {
	cm := testConfigManager(t)
	a := apiconfig.DevshardBinary{Binary: "https://example.com/a.tar.gz", SHA256: strings.Repeat("a", 64)}
	b := apiconfig.DevshardBinary{Binary: "https://example.com/b.tar.gz", SHA256: strings.Repeat("b", 64)}
	c := apiconfig.DevshardBinary{Binary: "https://example.com/c.tar.gz", SHA256: strings.Repeat("c", 64)}
	// Register before the chain approves either pair.
	require.NoError(t, cm.SetDevshardVersionOverride(context.Background(), apiconfig.DevshardVersionOverride{From: a, To: b}))
	require.NoError(t, cm.SetDevshardVersionOverride(context.Background(), apiconfig.DevshardVersionOverride{From: b, To: c}))
	for _, tc := range []struct {
		name        string
		input, want apiconfig.DevshardBinary
	}{
		{"exact match, no chaining", a, b},
		{"URL changed", apiconfig.DevshardBinary{Binary: c.Binary, SHA256: a.SHA256}, apiconfig.DevshardBinary{Binary: c.Binary, SHA256: a.SHA256}},
		{"checksum changed", apiconfig.DevshardBinary{Binary: a.Binary, SHA256: c.SHA256}, apiconfig.DevshardBinary{Binary: a.Binary, SHA256: c.SHA256}},
		{"new approved pair", c, c},
		{"checksum hex case", apiconfig.DevshardBinary{Binary: a.Binary, SHA256: strings.ToUpper(a.SHA256)}, b},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := apiconfig.DevshardVersionsCache{Versions: []apiconfig.DevshardVersion{{Name: "v1", Binary: tc.input.Binary, SHA256: tc.input.SHA256}}, MaxNonce: 42, DevshardRequestsEnabled: true}
			cm.SetDevshardVersions(original)
			want := original
			want.Versions = []apiconfig.DevshardVersion{{Name: "v1", Binary: tc.want.Binary, SHA256: tc.want.SHA256}}
			require.Equal(t, want, cm.GetEffectiveDevshardVersions())
			require.Equal(t, original, cm.GetDevshardVersions())
			got := cm.GetEffectiveDevshardVersions()
			got.Versions[0].Binary = "mutated"
			require.Equal(t, want, cm.GetEffectiveDevshardVersions())
		})
	}
	// Match the same checksum fallback used by versiond; explicit SHA takes precedence.
	a.Binary += "?checksum=sha256:" + a.SHA256
	require.NoError(t, cm.SetDevshardVersionOverride(context.Background(), apiconfig.DevshardVersionOverride{From: a, To: b}))
	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{Versions: []apiconfig.DevshardVersion{{Name: "v2", Binary: a.Binary}}})
	require.Equal(t, b.Binary, cm.GetEffectiveDevshardVersions().Versions[0].Binary)
	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{Versions: []apiconfig.DevshardVersion{{Name: "v2", Binary: a.Binary, SHA256: c.SHA256}}})
	require.Equal(t, a.Binary, cm.GetEffectiveDevshardVersions().Versions[0].Binary)
}

func TestDevshardVersionOverridesPersistence(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("api: {}\n"), 0600))
	load := func() *apiconfig.ConfigManager {
		cm, err := apiconfig.LoadConfigManagerWithPaths(configPath, filepath.Join(dir, "config.db"), "")
		require.NoError(t, err)
		return cm
	}
	cm := load()
	o := apiconfig.DevshardVersionOverride{
		From: apiconfig.DevshardBinary{Binary: "https://example.com/a", SHA256: strings.Repeat("a", 64)},
		To:   apiconfig.DevshardBinary{Binary: "https://example.com/b", SHA256: strings.Repeat("b", 64)},
	}
	ctx := context.Background()
	require.NoError(t, cm.SetDevshardVersionOverride(ctx, o))
	o.To.Binary = "https://example.com/c"
	require.NoError(t, cm.SetDevshardVersionOverride(ctx, o))
	cm = load()
	require.Equal(t, []apiconfig.DevshardVersionOverride{o}, cm.GetDevshardVersionOverrides())
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	changed := o
	changed.To.Binary = "https://example.com/failed"
	require.Error(t, cm.SetDevshardVersionOverride(canceled, changed))
	require.Error(t, cm.DeleteDevshardVersionOverride(canceled, o.From))
	require.Equal(t, []apiconfig.DevshardVersionOverride{o}, cm.GetDevshardVersionOverrides())
	require.Equal(t, cm.GetDevshardVersionOverrides(), load().GetDevshardVersionOverrides())
	require.NoError(t, cm.DeleteDevshardVersionOverride(ctx, o.From))
	require.NoError(t, cm.DeleteDevshardVersionOverride(ctx, o.From))
	require.Empty(t, load().GetDevshardVersionOverrides())
}
