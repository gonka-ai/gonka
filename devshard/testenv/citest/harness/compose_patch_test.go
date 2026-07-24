package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPatchComposeEnvKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	require.NoError(t, os.WriteFile(path, []byte(`
services:
  versiond-0:
    environment:
      DEVSHARD_STORAGE_MODE: postgres
      VERSIOND_HOSTS: "versiond-0 versiond-1"
      VERSIOND_LEGACY_HOST: "versiond-0"
`), 0o644))

	PatchVersiondStorageMode(t, path, "sqlite")
	PatchRouterVersiondHosts(t, path, "versiond-1")

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "DEVSHARD_STORAGE_MODE: sqlite")
	require.NotContains(t, text, "DEVSHARD_STORAGE_MODE: postgres")
	require.Contains(t, text, `VERSIOND_HOSTS: "versiond-1"`)
	require.NotContains(t, text, `VERSIOND_HOSTS: "versiond-0 versiond-1"`)
	require.Contains(t, text, `VERSIOND_LEGACY_HOST: "versiond-1"`)
}
