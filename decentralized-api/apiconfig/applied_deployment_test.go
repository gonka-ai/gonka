package apiconfig_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func TestAppliedDeploymentFingerprintPersistence(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("api:\n  port: 8080\n"), 0o644))
	manager, err := apiconfig.LoadConfigManagerWithPaths(
		configPath,
		filepath.Join(dir, "gonka.db"),
		"",
	)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, manager.SetAppliedDeploymentFingerprint(ctx, "node/1", "org/model", "fingerprint"))
	got, found, err := manager.GetAppliedDeploymentFingerprint(ctx, "node/1", "org/model")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "fingerprint", got)

	require.NoError(t, manager.DeleteAppliedDeploymentFingerprint(ctx, "node/1", "org/model"))
	_, found, err = manager.GetAppliedDeploymentFingerprint(ctx, "node/1", "org/model")
	require.NoError(t, err)
	require.False(t, found)

	require.NoError(t, manager.SetAppliedDeploymentFingerprint(ctx, "node/1", "model-a", "a"))
	require.NoError(t, manager.SetAppliedDeploymentFingerprint(ctx, "node/1", "model-b", "b"))
	require.NoError(t, manager.DeleteAppliedDeploymentsForNode(ctx, "node/1"))
	_, found, err = manager.GetAppliedDeploymentFingerprint(ctx, "node/1", "model-a")
	require.NoError(t, err)
	require.False(t, found)
}
