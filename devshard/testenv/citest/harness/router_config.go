package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// VersiondRouterEnv configures a versiond-router config render.
type VersiondRouterEnv struct {
	Hosts         []string // VERSIOND_HOSTS (required)
	Port          string   // VERSIOND_PORT, default 8080
	LegacyHost    string   // VERSIOND_LEGACY_HOST, default first host
	NonHAVersions string   // VERSIOND_NON_HA_VERSIONS, e.g. "v1 v2 v3"
}

// RenderVersiondRouterConfig runs versiond-router/entrypoint.sh outside a
// container and returns the nginx config it produces.
func RenderVersiondRouterConfig(t *testing.T, env VersiondRouterEnv) string {
	t.Helper()
	require.NotEmpty(t, env.Hosts, "VERSIOND_HOSTS required")

	dir := filepath.Join(RepoRoot(t), "versiond-router")
	out := filepath.Join(t.TempDir(), "default.conf")
	cmdEnv := []string{
		"VERSIOND_HOSTS=" + strings.Join(env.Hosts, " "),
		"VERSIOND_PORT=" + env.Port,
		"VERSIOND_LEGACY_HOST=" + env.LegacyHost,
		"VERSIOND_NON_HA_VERSIONS=" + env.NonHAVersions,
		"VERSIOND_ROUTER_TEMPLATE=" + filepath.Join(dir, "nginx.conf.template"),
		"VERSIOND_ROUTER_OUT=" + out,
	}
	return renderRouterConfig(t, filepath.Join(dir, "entrypoint.sh"), cmdEnv, out)
}

// RenderEdgeAPIRouterConfig runs edge-api-router/entrypoint.sh outside a
// container and returns the nginx config it produces.
func RenderEdgeAPIRouterConfig(t *testing.T, hosts []string, port string) string {
	t.Helper()
	require.NotEmpty(t, hosts, "EDGE_API_HOSTS required")

	dir := filepath.Join(RepoRoot(t), "edge-api-router")
	out := filepath.Join(t.TempDir(), "default.conf")
	cmdEnv := []string{
		"EDGE_API_HOSTS=" + strings.Join(hosts, " "),
		"EDGE_API_PORT=" + port,
		"EDGE_API_ROUTER_TEMPLATE=" + filepath.Join(dir, "nginx.conf.template"),
		"EDGE_API_ROUTER_OUT=" + out,
	}
	return renderRouterConfig(t, filepath.Join(dir, "entrypoint.sh"), cmdEnv, out)
}

func renderRouterConfig(t *testing.T, entrypoint string, env []string, out string) string {
	t.Helper()
	require.FileExists(t, entrypoint)

	cmd := exec.Command("/bin/sh", entrypoint)
	cmd.Env = append(os.Environ(), env...)
	combined, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "%s failed:\n%s", entrypoint, combined)

	body, err := os.ReadFile(out)
	require.NoError(t, err)
	return string(body)
}

// UpstreamBlock returns `upstream <name> { … }` including braces, or empty.
func UpstreamBlock(conf, name string) string {
	return ConfigBlock(conf, "upstream "+name+" {")
}

// MapBlock returns `map <key> <target> { … }` including braces, or empty.
func MapBlock(conf, target string) string {
	idx := strings.Index(conf, " "+target+" {")
	if idx < 0 {
		return ""
	}
	start := strings.LastIndex(conf[:idx], "map ")
	if start < 0 {
		return ""
	}
	return blockFrom(conf[start:])
}

// ConfigBlock returns the brace-delimited block whose header starts with
// header, or empty when absent.
func ConfigBlock(conf, header string) string {
	idx := strings.Index(conf, header)
	if idx < 0 {
		return ""
	}
	return blockFrom(conf[idx:])
}

func blockFrom(rest string) string {
	depth := 0
	for i, r := range rest {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return strings.TrimSpace(rest[:i+1])
			}
		}
	}
	return ""
}
