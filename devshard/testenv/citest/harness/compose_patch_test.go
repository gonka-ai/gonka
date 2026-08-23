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
`), 0o644))

	PatchVersiondStorageMode(t, path, "sqlite")
	PatchRouterVersiondHosts(t, path, "versiond-0")

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "DEVSHARD_STORAGE_MODE: sqlite")
	require.NotContains(t, text, "DEVSHARD_STORAGE_MODE: postgres")
	require.Contains(t, text, `VERSIOND_HOSTS: "versiond-0"`)
	require.NotContains(t, text, `VERSIOND_HOSTS: "versiond-0 versiond-1"`)
}
