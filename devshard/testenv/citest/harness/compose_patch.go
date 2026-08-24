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
	patchComposeInsertEnvAfter(t, composePath, afterKey, false, lines...)
}

// PatchComposeInsertEnvAfterAll adds environment lines after every matching env key
// (e.g. the same DEVSHARD_* key on each versiond-* service).
func PatchComposeInsertEnvAfterAll(t *testing.T, composePath, afterKey string, lines ...string) {
	t.Helper()
	patchComposeInsertEnvAfter(t, composePath, afterKey, true, lines...)
}

func patchComposeInsertEnvAfter(t *testing.T, composePath, afterKey string, all bool, lines ...string) {
	t.Helper()
	body, err := os.ReadFile(composePath)
	require.NoError(t, err)
	re := regexp.MustCompile(`(?m)^(\s*)` + regexp.QuoteMeta(afterKey) + `:\s*.*$`)
	locs := re.FindAllIndex(body, -1)
	require.NotEmpty(t, locs, "compose %s: env key %q not found", composePath, afterKey)
	if !all {
		locs = locs[:1]
	}
	// Insert from last match to first so earlier offsets stay valid.
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
