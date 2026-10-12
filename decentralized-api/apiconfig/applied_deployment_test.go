package apiconfig_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func testConfigManager(t *testing.T) *apiconfig.ConfigManager {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("api:\n  port: 8080\n"), 0o644))
	manager, err := apiconfig.LoadConfigManagerWithPaths(configPath, filepath.Join(dir, "gonka.db"), "")
	require.NoError(t, err)
	return manager
}

func TestAppliedDeploymentTracksCurrentRecordPerNode(t *testing.T) {
	manager := testConfigManager(t)
	ctx := context.Background()

	require.NoError(t, manager.SetAppliedDeployment(ctx, "node/1", apiconfig.AppliedDeploymentState{
		ModelID:     "model-a",
		Fingerprint: "fp-a",
	}))
	require.NoError(t, manager.SetAppliedDeployment(ctx, "node/1", apiconfig.AppliedDeploymentState{
		ModelID:     "model-b",
		Fingerprint: "fp-b",
	}))

	got, found, err := manager.GetAppliedDeployment(ctx, "node/1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "model-b", got.ModelID)
	require.Equal(t, "fp-b", got.Fingerprint)

	other, found, err := manager.GetAppliedDeployment(ctx, "node/2")
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, other.ModelID)

	require.NoError(t, manager.DeleteAppliedDeploymentsForNode(ctx, "node/1"))
	_, found, err = manager.GetAppliedDeployment(ctx, "node/1")
	require.NoError(t, err)
	require.False(t, found)
}
