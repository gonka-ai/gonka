package harness

import (
	"os"
	"path/filepath"
	"strings"
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

func TestPatchComposeInsertEnvAfterAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	require.NoError(t, os.WriteFile(path, []byte(`
services:
  versiond-0:
    environment:
      DEVSHARD_VALIDATION_LEASE_TTL: 30m
  versiond-1:
    environment:
      DEVSHARD_VALIDATION_LEASE_TTL: 30m
`), 0o644))

	PatchComposeInsertEnvAfterAll(t, path, "DEVSHARD_VALIDATION_LEASE_TTL",
		`DEVSHARD_TEST_DETACH_PRIMARY_AFTER_WRITES: "1"`)

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(body), "DEVSHARD_TEST_DETACH_PRIMARY_AFTER_WRITES"))
}
