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
