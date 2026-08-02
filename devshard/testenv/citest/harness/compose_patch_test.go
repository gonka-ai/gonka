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
  versiond-router:
    environment:
      VERSIOND_POOL_HOST: "versiond-pool"
      GONKA_HA: "true"
`), 0o644))

	PatchVersiondStorageMode(t, path, "sqlite")
	PatchRouterHADeployment(t, path, false)

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "DEVSHARD_STORAGE_MODE: sqlite")
	require.NotContains(t, text, "DEVSHARD_STORAGE_MODE: postgres")
	require.Contains(t, text, `GONKA_HA: ""`)
	require.NotContains(t, text, `GONKA_HA: "true"`)
}

func TestParseRouterPool(t *testing.T) {
	slots := parseRouterPool("SLOT\tADDRESS\t\tSTATE\n" +
		"versiond1\t172.30.0.10\tUP\n" +
		"versiond2\t172.30.0.11\tDRAIN\n" +
		"2 server(s) taking traffic in versiond_ha_pool\n")
	require.Equal(t, []RouterSlot{
		{Name: "versiond1", Address: "172.30.0.10", State: RouterSlotUp},
		{Name: "versiond2", Address: "172.30.0.11", State: RouterSlotDrain},
	}, slots)
}

func TestPatchComposeServiceEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	require.NoError(t, os.WriteFile(path, []byte(`
services:
  versiond-0:
    environment:
      VERSIOND_ORACLE_URL: http://mock-dapi:9100/versions
      KEY_NAME: versiond-0
  versiond-1:
    environment:
      VERSIOND_ORACLE_URL: http://mock-dapi:9100/versions
      KEY_NAME: versiond-0
volumes:
  VERSIOND_ORACLE_URL: not-an-env-entry
`), 0o644))

	previous := PatchComposeServiceEnv(t, path, "versiond-1", "VERSIOND_ORACLE_URL", "http://127.0.0.1:1/versions")
	require.Equal(t, "http://mock-dapi:9100/versions", previous)

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(body)

	// Exactly one host moved; the sibling and the unrelated block are untouched,
	// which is the whole point of scoping the patch to one service.
	require.Equal(t, 1, strings.Count(text, "VERSIOND_ORACLE_URL: http://127.0.0.1:1/versions"))
	require.Equal(t, 1, strings.Count(text, "VERSIOND_ORACLE_URL: http://mock-dapi:9100/versions"))
	require.Contains(t, text, "VERSIOND_ORACLE_URL: not-an-env-entry")

	restored := PatchComposeServiceEnv(t, path, "versiond-1", "VERSIOND_ORACLE_URL", previous)
	require.Equal(t, "http://127.0.0.1:1/versions", restored)
	body, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(body), "VERSIOND_ORACLE_URL: http://mock-dapi:9100/versions"))
}
