package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ProxyRoutingDump is the machine-readable output of proxy/entrypoint.sh when
// PROXY_ROUTING_DUMP_DIR is set (exits before writing /etc/nginx).
type ProxyRoutingDump struct {
	FinalEdgeAPIService string
	EdgeAPIUpstream     string
	VersiondUpstream    string
	ObsProxyPass        string
	DevshardLocations   string
}

// ProxyRoutingEnv selects the backends the join proxy is pointed at. Zero
// values mean the canonical single-instance services.
type ProxyRoutingEnv struct {
	EdgeAPIService  string // EDGE_API_SERVICE_NAME; "" = edge-api not configured
	EdgeAPIPort     string // default 18080
	VersiondService string // default versiond; "versiond-router" in HA
	VersiondPort    string // default 8080
}

// DumpProxyRouting runs proxy/entrypoint.sh with PROXY_ROUTING_DUMP_DIR and the
// given EDGE_API_SERVICE_NAME (empty string clears it).
func DumpProxyRouting(t *testing.T, edgeAPIServiceName string) ProxyRoutingDump {
	t.Helper()
	return DumpProxyRoutingEnv(t, ProxyRoutingEnv{EdgeAPIService: edgeAPIServiceName})
}

// DumpProxyRoutingEnv is DumpProxyRouting with the versiond backend selectable,
// so HA topologies (versiond-router, edge-api-router) can be asserted too.
func DumpProxyRoutingEnv(t *testing.T, env ProxyRoutingEnv) ProxyRoutingDump {
	t.Helper()

	repoRoot := RepoRoot(t)
	entrypoint := filepath.Join(repoRoot, "proxy", "entrypoint.sh")
	require.FileExists(t, entrypoint)

	edgeAPIPort := env.EdgeAPIPort
	if edgeAPIPort == "" {
		edgeAPIPort = "18080"
	}
	versiondService := env.VersiondService
	if versiondService == "" {
		versiondService = "versiond"
	}
	versiondPort := env.VersiondPort
	if versiondPort == "" {
		versiondPort = "8080"
	}

	dumpDir := t.TempDir()
	cmd := exec.Command("/bin/sh", entrypoint)
	cmd.Env = append(os.Environ(),
		"PROXY_ROUTING_DUMP_DIR="+dumpDir,
		"NGINX_MODE=http",
		"DISABLE_DEVSHARD_PROXY=false",
		"JAEGER_ENABLED=false",
		"GRAFANA_ENABLED=false",
		"KEY_NAME=",
		"EDGE_API_SERVICE_NAME="+env.EdgeAPIService,
		"EDGE_API_PORT="+edgeAPIPort,
		"API_SERVICE_NAME=api",
		"VERSIOND_SERVICE_NAME="+versiondService,
		"VERSIOND_PORT="+versiondPort,
		"GONKA_API_PORT=9000",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "entrypoint dump failed:\n%s", out)

	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dumpDir, name))
		require.NoError(t, err, "missing dump file %s", name)
		return string(b)
	}
	return ProxyRoutingDump{
		FinalEdgeAPIService: read("final_edge_api_service"),
		EdgeAPIUpstream:     read("edge_api_upstream"),
		VersiondUpstream:    read("versiond_upstream"),
		ObsProxyPass:        read("obs_proxy_pass"),
		DevshardLocations:   read("devshard_locations"),
	}
}

// RepoRoot returns the gonka repository root (parent of devshard/).
func RepoRoot(t *testing.T) string {
	t.Helper()
	// citest runs with cwd = …/devshard/testenv (go test ./citest) or
	// …/devshard/testenv/citest depending on invocation; walk up for go.mod.
	wd, err := os.Getwd()
	require.NoError(t, err)
	dir := wd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "proxy", "entrypoint.sh")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "common", "go.mod")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not find repo root from cwd %s", wd)
	return ""
}

// LocationBlock returns the nginx location block whose header contains needle,
// or empty if not found. Handles one level of nested braces (e.g. CORS if).
func LocationBlock(locations, needle string) string {
	idx := strings.Index(locations, needle)
	if idx < 0 {
		return ""
	}
	start := idx
	if !strings.HasPrefix(strings.TrimLeft(locations[idx:], " \t"), "location ") {
		const marker = "location "
		back := strings.LastIndex(locations[:idx], marker)
		if back >= 0 {
			start = back
		}
	} else {
		// Needle may sit mid-line; walk back to line start.
		if nl := strings.LastIndex(locations[:idx], "\n"); nl >= 0 {
			start = nl + 1
		} else {
			start = 0
		}
	}
	rest := locations[start:]
	depth := 0
	seenOpen := false
	for i, r := range rest {
		switch r {
		case '{':
			depth++
			seenOpen = true
		case '}':
			depth--
			if seenOpen && depth == 0 {
				return strings.TrimSpace(rest[:i+1])
			}
		}
	}
	return ""
}
