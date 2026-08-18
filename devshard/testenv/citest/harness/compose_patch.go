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

// PatchRouterHADeployment changes the authoritative HA declaration. Tests that
// disable it must also keep only one pool member usable, because the router has
// a runtime multi-host fallback.
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
