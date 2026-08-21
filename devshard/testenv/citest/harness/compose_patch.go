package harness

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// PatchComposeEnvKey replaces every `KEY: ...` environment line in a compose file.
func PatchComposeEnvKey(t *testing.T, composePath, key, value string) {
	t.Helper()
	body, err := os.ReadFile(composePath)
	require.NoError(t, err)
	re := regexp.MustCompile(`(?m)^(\s*` + regexp.QuoteMeta(key) + `:\s*).*$`)
	require.True(t, re.Match(body), "compose %s: env key %q not found", composePath, key)
	updated := re.ReplaceAll(body, []byte("${1}"+value))
	require.NoError(t, os.WriteFile(composePath, updated, 0o644))
}

// PatchRouterVersiondHosts sets VERSIOND_HOSTS on versiond-router (quoted).
func PatchRouterVersiondHosts(t *testing.T, composePath, hosts string) {
	t.Helper()
	PatchComposeEnvKey(t, composePath, "VERSIOND_HOSTS", `"`+hosts+`"`)
}

// PatchVersiondStorageMode sets DEVSHARD_STORAGE_MODE on all versiond services.
func PatchVersiondStorageMode(t *testing.T, composePath, mode string) {
	t.Helper()
	PatchComposeEnvKey(t, composePath, "DEVSHARD_STORAGE_MODE", mode)
}

// PatchComposeUseRandomHostPorts lets Docker assign localhost host ports while
// keeping the configured container ports unchanged for in-network service calls.
func PatchComposeUseRandomHostPorts(t *testing.T, composePath string) {
	t.Helper()
	body, err := os.ReadFile(composePath)
	require.NoError(t, err)
	re := regexp.MustCompile(`(?m)^(\s*-\s*")([0-9]+):([0-9]+)(".*)$`)
	require.True(t, re.Match(body), "compose %s: no host-published port lines found", composePath)
	updated := re.ReplaceAll(body, []byte("${1}127.0.0.1::${3}${4}"))
	require.NoError(t, os.WriteFile(composePath, updated, 0o644))
}

// PatchComposeRemoveEnvKey removes every `KEY: ...` environment line in a compose file.
func PatchComposeRemoveEnvKey(t *testing.T, composePath, key string) {
	t.Helper()
	body, err := os.ReadFile(composePath)
	require.NoError(t, err)
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `:\s*.*\n?`)
	require.True(t, re.Match(body), "compose %s: env key %q not found", composePath, key)
	updated := re.ReplaceAll(body, nil)
	require.NoError(t, os.WriteFile(composePath, updated, 0o644))
}

// PatchComposeInsertEnvAfter adds environment lines after the first matching env key.
func PatchComposeInsertEnvAfter(t *testing.T, composePath, afterKey string, lines ...string) {
	t.Helper()
	body, err := os.ReadFile(composePath)
	require.NoError(t, err)
	re := regexp.MustCompile(`(?m)^(\s*)` + regexp.QuoteMeta(afterKey) + `:\s*.*$`)
	loc := re.FindIndex(body)
	require.NotNil(t, loc, "compose %s: env key %q not found", composePath, afterKey)
	lineEnd := loc[1]
	if lineEnd < len(body) && body[lineEnd] == '\n' {
		lineEnd++
	}
	indent := string(re.FindSubmatch(body[loc[0]:loc[1]])[1])
	var insert strings.Builder
	for _, line := range lines {
		insert.WriteString(indent)
		insert.WriteString(line)
		insert.WriteByte('\n')
	}
	updated := append([]byte{}, body[:lineEnd]...)
	updated = append(updated, []byte(insert.String())...)
	updated = append(updated, body[lineEnd:]...)
	require.NoError(t, os.WriteFile(composePath, updated, 0o644))
}

// PatchComposeInsertEnvAfterAll adds environment lines after every matching env key.
func PatchComposeInsertEnvAfterAll(t *testing.T, composePath, afterKey string, lines ...string) {
	t.Helper()
	body, err := os.ReadFile(composePath)
	require.NoError(t, err)
	re := regexp.MustCompile(`(?m)^(\s*)` + regexp.QuoteMeta(afterKey) + `:\s*.*$`)
	locs := re.FindAllIndex(body, -1)
	require.NotEmpty(t, locs, "compose %s: env key %q not found", composePath, afterKey)
	for i := len(locs) - 1; i >= 0; i-- {
		loc := locs[i]
		lineEnd := loc[1]
		if lineEnd < len(body) && body[lineEnd] == '\n' {
			lineEnd++
		}
		indent := string(re.FindSubmatch(body[loc[0]:loc[1]])[1])
		var insert strings.Builder
		for _, line := range lines {
			insert.WriteString(indent)
			insert.WriteString(line)
			insert.WriteByte('\n')
		}
		updated := append([]byte{}, body[:lineEnd]...)
		updated = append(updated, []byte(insert.String())...)
		updated = append(updated, body[lineEnd:]...)
		body = updated
	}
	require.NoError(t, os.WriteFile(composePath, body, 0o644))
}

// EnableHeightSyncCompose injects optional height-sync env into a generated
// compose file. Default gencompose output is left unchanged; only this
// citest suite patches the stack.
func EnableHeightSyncCompose(t *testing.T, composePath string) {
	t.Helper()
	body, err := os.ReadFile(composePath)
	require.NoError(t, err)
	require.NotContains(t, string(body), "DEVSHARD_CHAINORACLE_URL:",
		"default compose must not enable height-sync; patch only in citest-height-sync")
	PatchComposeInsertEnvAfterAll(t, composePath, "VERSIOND_ORACLE_URL",
		"DEVSHARD_CHAINORACLE_URL: http://mock-dapi:9100",
		"DEVSHARD_LOG_LEVEL: debug",
	)
	PatchComposeInsertEnvAfterAll(t, composePath, "DEVSHARD_PUBLIC_API",
		"DEVSHARD_CHAINORACLE_URL: http://mock-dapi:9100",
		"DEVSHARD_LOG_LEVEL: debug",
	)
}

// EnableLegacyDapiCompose makes mock-dapi omit /block/* (0.2.15 / pre-mount
// dapi). Height-sync still points at mock-dapi; hosts failover to direct chain.
func EnableLegacyDapiCompose(t *testing.T, composePath string) {
	t.Helper()
	PatchComposeInsertEnvAfterAll(t, composePath, "MOCK_DAPI_HTTP_ADDR",
		`MOCK_DAPI_OMIT_BLOCK_ROUTES: "1"`,
	)
}

// EnableHeightSyncPeerMatrixCompose turns on the quadratic peer_seen matrix
// series on the gateway only (DEVSHARD_GATEWAY_HEIGHTSYNC_PEER_MATRIX).
func EnableHeightSyncPeerMatrixCompose(t *testing.T, composePath string) {
	t.Helper()
	PatchComposeInsertEnvAfterAll(t, composePath, "DEVSHARD_PUBLIC_API",
		`DEVSHARD_GATEWAY_HEIGHTSYNC_PEER_MATRIX: "1"`,
	)
}
