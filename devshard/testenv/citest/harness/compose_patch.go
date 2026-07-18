package harness

import (
	"os"
	"regexp"
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
