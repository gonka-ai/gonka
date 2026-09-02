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

// PatchRouterHADeployment changes the authoritative HA declaration. The router
// also has a runtime multi-host fallback, so tests that disable this declaration
// must keep only one pool host usable. Recreate the router afterwards for the
// declaration to take effect.
func PatchRouterHADeployment(t *testing.T, composePath string, ha bool) {
	t.Helper()
	value := `""`
	if ha {
		value = `"true"`
	}
	PatchComposeEnvKey(t, composePath, "GONKA_HA", value)
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

var mockDapiHTTPOriginRe = regexp.MustCompile(`(?m)^\s*(?:DEVSHARD_PUBLIC_API|VERSIOND_ORACLE_URL):\s*(http://mock-dapi:\d+)`)

// mockDapiHTTPOriginFromCompose copies the generated mock-dapi HTTP origin
// (host:port). Citest picks a free container port; :9100 is only the default
// YAML and is often not listening.
func mockDapiHTTPOriginFromCompose(body []byte) string {
	m := mockDapiHTTPOriginRe.FindSubmatch(body)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// EnableHeightSyncCompose injects DEVSHARD_CHAINORACLE_URL (dapi /block/*)
// into a generated compose file. Height-sync itself is always on when
// NODE_RPC_URL / chain RPC is present; this patch only adds the dapi lookup.
// The oracle URL copies the generated mock-dapi HTTP port rather than
// hardcoding :9100.
func EnableHeightSyncCompose(t *testing.T, composePath string) {
	t.Helper()
	body, err := os.ReadFile(composePath)
	require.NoError(t, err)
	require.NotContains(t, string(body), "DEVSHARD_CHAINORACLE_URL:",
		"default compose must not set DEVSHARD_CHAINORACLE_URL; citest-height-sync patches the dapi origin")
	origin := mockDapiHTTPOriginFromCompose(body)
	require.NotEmpty(t, origin, "compose %s: no mock-dapi HTTP origin on DEVSHARD_PUBLIC_API / VERSIOND_ORACLE_URL", composePath)
	PatchComposeInsertEnvAfterAll(t, composePath, "VERSIOND_ORACLE_URL",
		"DEVSHARD_CHAINORACLE_URL: "+origin,
		"DEVSHARD_LOG_LEVEL: debug",
	)
	PatchComposeInsertEnvAfterAll(t, composePath, "DEVSHARD_PUBLIC_API",
		"DEVSHARD_CHAINORACLE_URL: "+origin,
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

// PatchComposeServiceEnv replaces one `KEY: ...` line inside a single service
// block and returns the value it replaced. Unlike PatchComposeEnvKey this does
// not touch the other services, which is what makes it usable for putting one
// host into a state its siblings are not in.
func PatchComposeServiceEnv(t *testing.T, composePath, service, key, value string) string {
	t.Helper()
	body, err := os.ReadFile(composePath)
	require.NoError(t, err)

	lines := strings.Split(string(body), "\n")
	start := -1
	for i, line := range lines {
		if line == "  "+service+":" {
			start = i
			break
		}
	}
	require.GreaterOrEqual(t, start, 0, "compose %s: service %q not found", composePath, service)

	entry := regexp.MustCompile(`^(\s+` + regexp.QuoteMeta(key) + `:\s*)(.*)$`)
	for i := start + 1; i < len(lines); i++ {
		// A line indented by two or less starts the next service or a top-level
		// key, so the service block has ended.
		if trimmed := strings.TrimLeft(lines[i], " "); trimmed != "" &&
			len(lines[i])-len(trimmed) <= 2 {
			break
		}
		m := entry.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		lines[i] = m[1] + value
		require.NoError(t, os.WriteFile(composePath, []byte(strings.Join(lines, "\n")), 0o644))
		return strings.TrimSpace(m[2])
	}
	t.Fatalf("compose %s: service %q has no %q entry", composePath, service, key)
	return ""
}
