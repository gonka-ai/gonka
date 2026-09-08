package harness

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// VersiondEndpoint is a member of the routers' explicit endpoint file.
type VersiondEndpoint struct {
	ID   string `json:"id"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

func (e VersiondEndpoint) Address() string {
	return net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

// UnjoinedRouterFleet separates real PostgreSQL-backed versionds from routers
// into two Compose projects. Neither project includes a gateway/public proxy.
type UnjoinedRouterFleet struct {
	Versionds  *Stack
	Routers    *Stack
	Version    string
	RouterHTTP []string
	Endpoints  []VersiondEndpoint
}

// BootUnjoinedRouterFleet reaches versionds only through Docker-host published
// ports. A single literal host address keeps the addr-keyed rings comparable;
// comparing container IPs or matching only the port would hide a wrong route.
func BootUnjoinedRouterFleet(t *testing.T) *UnjoinedRouterFleet {
	t.Helper()
	versionds := NewStack(t, "citest-unjoined-versionds-*")
	RequireLinuxDevshardd(t, versionds.TestenvDir)
	WriteStackConfig(t, versionds.WorkDir)
	versionds.RunGencompose(t)
	cfg := versionds.LoadConfig(t)
	requireTwoVersiondHosts(t, cfg)
	require.True(t, cfg.Postgres.Enabled, "unjoined routers require the real HA pair")
	prepareUnjoinedVersionds(t, versionds.ComposePath)
	versionds.UpServices(t, false, "versiond-0", "versiond-1")
	t.Cleanup(func() {
		if t.Failed() {
			DumpComposeLogs(t, versionds)
		}
	})

	// On the Linux CI daemon the default bridge gateway is the Docker host,
	// not either project's network. Ports must bind beyond loopback so routers
	// can dial this address. The test itself still probes them on localhost.
	host := strings.TrimSpace(unjoinedDockerOutput(t, "network", "inspect", "bridge",
		"--format", "{{(index .IPAM.Config 0).Gateway}}"))
	ip := net.ParseIP(host)
	require.NotNil(t, ip, "Docker bridge gateway must be a literal IP: %q", host)
	require.False(t, ip.IsLoopback() || ip.IsUnspecified(), "invalid Docker host address %q", host)
	fleet := &UnjoinedRouterFleet{Versionds: versionds, Version: cfg.Versiond.VersionName}
	client := HTTPClient()
	for _, member := range cfg.Hosts {
		addr := versionds.composePublishedAddr(t, member.ID, 8080)
		_, port, err := net.SplitHostPort(addr)
		require.NoError(t, err)
		p, err := strconv.Atoi(port)
		require.NoError(t, err)
		fleet.Endpoints = append(fleet.Endpoints, VersiondEndpoint{ID: member.ID, Host: host, Port: p})
		WaitGETOK(t, client, "http://"+addr+"/readyz", 5*time.Minute, member.ID+" readiness")
		WaitGETOK(t, client, "http://"+addr+"/"+fleet.Version+"/healthz", 5*time.Minute, member.ID+" child health")
	}

	routers := NewStack(t, "citest-unjoined-routers-*")
	fleet.Routers = routers
	body, err := os.ReadFile(filepath.Join(routers.TestenvDir, "docker-compose.unjoined-routers.yml"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(routers.ComposePath, body, 0o644))
	fixComposePaths(t, routers.ComposePath, routers.TestenvDir)
	require.NoError(t, os.WriteFile(filepath.Join(routers.WorkDir, ".env"),
		[]byte("TESTENV_UNJOINED_VERSION="+fleet.Version+"\n"), 0o644))
	endpoints, err := json.MarshalIndent(fleet.Endpoints, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(routers.WorkDir, "versiond-endpoints.json"), endpoints, 0o644))
	Step(t, "explicit versiond membership: %s", endpoints)
	routers.UpServices(t, false, "versiond-router-0", "versiond-router-1")
	t.Cleanup(func() {
		if t.Failed() {
			DumpComposeLogs(t, routers)
		}
	})
	for _, service := range []string{"versiond-router-0", "versiond-router-1"} {
		fleet.requireUnjoinedRouter(t, service, endpoints)
		url := "http://" + routers.composePublishedAddr(t, service, 8080)
		fleet.RouterHTTP = append(fleet.RouterHTTP, url)
		WaitGETOK(t, client, url+"/healthz", 5*time.Minute, service+" health")
		admin := "http://" + routers.composePublishedAddr(t, service, 8404)
		WaitGETOK(t, client, admin+"/readyz?version="+fleet.Version, 5*time.Minute, service+" version readiness")
		fleet.waitFullPool(t, service)
	}
	return fleet
}

func prepareUnjoinedVersionds(t *testing.T, path string) {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	var model map[string]any
	require.NoError(t, yaml.Unmarshal(body, &model))
	services := model["services"].(map[string]any)
	delete(services, "versiond-router")
	delete(services, gatewayComposeService)
	if volumes, ok := model["volumes"].(map[string]any); ok {
		delete(volumes, "versiond-router-state")
	}
	for _, name := range []string{"versiond-0", "versiond-1"} {
		service := services[name].(map[string]any)
		service["ports"] = []string{"8080"} // Random host port on all interfaces.
		service["environment"].(map[string]any)["GONKA_HA"] = "true"
	}
	body, err = yaml.Marshal(model)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, body, 0o644))
}

func (f *UnjoinedRouterFleet) requireUnjoinedRouter(t *testing.T, service string, endpoints []byte) {
	t.Helper()
	routerID := f.Routers.ContainerID(t, service)
	routerNetworks := unjoinedContainerNetworks(t, routerID)
	require.Len(t, routerNetworks, 1, "router must have only its own network")
	for _, endpoint := range f.Endpoints {
		for network := range unjoinedContainerNetworks(t, f.Versionds.ContainerID(t, endpoint.ID)) {
			require.NotContains(t, routerNetworks, network, "%s shares a network with %s", service, endpoint.ID)
		}
	}
	// Check that name lookup actually works before asserting pool DNS fails.
	_, err := f.Routers.ComposeExecOutput(service, "getent", "hosts", "localhost")
	require.NoError(t, err)
	out, err := f.Routers.ComposeExecOutput(service, "getent", "hosts", "versiond-pool")
	require.Error(t, err, "%s unexpectedly resolved versiond-pool: %s", service, out)
	out, err = f.Routers.ComposeExecOutput(service, "cat", "/etc/haproxy/versiond-endpoints.json")
	require.NoError(t, err)
	require.JSONEq(t, string(endpoints), out, "%s must use the canonical membership file", service)
	Step(t, "%s has no network shared with versionds and cannot resolve versiond-pool", service)
}

func unjoinedContainerNetworks(t *testing.T, id string) map[string]json.RawMessage {
	t.Helper()
	out := unjoinedDockerOutput(t, "inspect", "--format", "{{json .NetworkSettings.Networks}}", id)
	var networks map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(out), &networks))
	require.NotEmpty(t, networks)
	return networks
}

func unjoinedDockerOutput(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	require.NoError(t, err, "docker %v: %s", args, out)
	return string(out)
}

func (f *UnjoinedRouterFleet) waitFullPool(t *testing.T, service string) {
	t.Helper()
	var diagnostic string
	ok := AssertEventually(t, time.Minute, time.Second, func() bool {
		out, err := f.Routers.ComposeExecOutput(service, "sh", "-c", routerVersionMapQuery)
		if err != nil {
			diagnostic = out
			return false
		}
		backend, err := parseRouterVersionBackend(out, f.Version)
		if err != nil {
			diagnostic = err.Error()
			return false
		}
		out, err = f.Routers.ComposeExecOutput(service, routerPoolStatusBin)
		diagnostic = out
		if err != nil {
			return false
		}
		// pool-status reports an IP without its port, so both published
		// endpoints have the same Address here. Count ready server slots;
		// the test checks full canonical addresses in X-Upstream-Addr.
		ready := 0
		for _, slot := range parseRouterPool(out) {
			if slot.Backend == backend && slot.State == RouterSlotUp {
				ready++
			}
		}
		return ready == len(f.Endpoints)
	})
	require.True(t, ok, "%s did not admit both endpoint slots:\n%s", service, diagnostic)
}
