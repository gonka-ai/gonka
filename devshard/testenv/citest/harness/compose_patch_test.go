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

func TestPatchComposeInsertEnvAfterAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	require.NoError(t, os.WriteFile(path, []byte(`
services:
  versiond-0:
    environment:
      VERSIOND_ORACLE_URL: http://mock-dapi:9100/versions
  versiond-1:
    environment:
      VERSIOND_ORACLE_URL: http://mock-dapi:9100/versions
  devshardctl:
    environment:
      DEVSHARD_PUBLIC_API: http://mock-dapi:9100
`), 0o644))

	EnableHeightSyncCompose(t, path)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(body)
	require.Equal(t, 3, strings.Count(text, "DEVSHARD_CHAINORACLE_URL: http://mock-dapi:9100"))
	require.Equal(t, 3, strings.Count(text, "DEVSHARD_LOG_LEVEL: debug"))
}

func TestEnableLegacyDapiCompose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	require.NoError(t, os.WriteFile(path, []byte(`
services:
  mock-dapi:
    environment:
      MOCK_DAPI_HTTP_ADDR: ":9100"
      MOCK_DAPI_GRPC_ADDR: ":9400"
`), 0o644))

	EnableLegacyDapiCompose(t, path)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(body), `MOCK_DAPI_OMIT_BLOCK_ROUTES: "1"`)
}

func TestEnableHeightSyncPeerMatrixCompose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	require.NoError(t, os.WriteFile(path, []byte(`
services:
  devshardctl:
    environment:
      DEVSHARD_PUBLIC_API: http://mock-dapi:9100
`), 0o644))

	EnableHeightSyncPeerMatrixCompose(t, path)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(body), `DEVSHARD_GATEWAY_HEIGHTSYNC_PEER_MATRIX: "1"`)
}
