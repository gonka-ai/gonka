package harness

import (
	"io"
	"net/http"
	"testing"
	"time"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// BootStack renders the 2×versiond citest config, starts compose, and returns handles.
func BootStack(t *testing.T, prefix string) (*Stack, *config.File, Endpoints) {
	t.Helper()
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteStackConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	requireTwoVersiondHosts(t, cfg)
	stack.Up(t)
	return stack, cfg, stack.Endpoints(t, cfg)
}

// BootStackBuild is like BootStack but rebuilds compose images first (devshardctl gRPC wiring).
func BootStackBuild(t *testing.T, prefix string) (*Stack, *config.File, Endpoints) {
	t.Helper()
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteStackConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	requireTwoVersiondHosts(t, cfg)
	RequireGatewayGRPCOnlyCompose(t, stack.ComposePath)
	stack.UpBuild(t)
	return stack, cfg, stack.Endpoints(t, cfg)
}

// BootHeightSyncStack is BootStack with DEVSHARD_CHAINORACLE_URL pointed at
// mock-dapi. Default compose is not modified; only this generated file is patched.
func BootHeightSyncStack(t *testing.T, prefix string) (*Stack, *config.File, Endpoints) {
	t.Helper()
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteStackConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	EnableHeightSyncCompose(t, stack.ComposePath)
	cfg := stack.LoadConfig(t)
	requireTwoVersiondHosts(t, cfg)
	stack.Up(t)
	return stack, cfg, stack.Endpoints(t, cfg)
}

func BootObservabilityStack(t *testing.T, prefix string) (*Stack, *config.File, Endpoints, ObservabilityEndpoints) {
	t.Helper()
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteStackConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	requireTwoVersiondHosts(t, cfg)
	stack.UpWithObservability(t, cfg)
	return stack, cfg, stack.Endpoints(t, cfg), DefaultObservabilityEndpoints()
}

// WaitStackHealthy polls the chain, dapi, router, and gateway boundaries.
func WaitStackHealthy(t *testing.T, stack *Stack, eps Endpoints) {
	t.Helper()
	client := HTTPClient()
	poll := 5 * time.Minute

	WaitGETOK(t, client, eps.MockChainRPC+"/health", poll, "mock-chain RPC health")
	WaitGETOK(t, client, eps.MockDapiHTTP+"/healthz", poll, "mock-dapi healthz")
	WaitGETOK(t, client, eps.MockDapiHTTP+"/v1/epochs/latest", 30*time.Second, "mock-dapi epochs/latest", stack)
	WaitGETOK(t, client, eps.RouterHTTP+"/healthz", poll, "versiond-router healthz", stack)
	WaitGETOK(t, client, eps.GatewayHTTP+"/v1/status", poll, "gateway /v1/status", stack)
}

func requireTwoVersiondHosts(t *testing.T, cfg *config.File) {
	t.Helper()
	if len(cfg.Hosts) != 2 {
		t.Fatalf("expected 2 versiond hosts, got %d", len(cfg.Hosts))
	}
}

// BootValidationLeaseRaceStack renders the 3×versiond lease-race config (HA pair + solo executor).
func BootValidationLeaseRaceStack(t *testing.T, prefix string) (*Stack, *config.File, Endpoints) {
	t.Helper()
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteValidationLeaseRaceConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	requireThreeVersiondHosts(t, cfg)
	stack.Up(t)
	return stack, cfg, stack.Endpoints(t, cfg)
}

func requireThreeVersiondHosts(t *testing.T, cfg *config.File) {
	t.Helper()
	if len(cfg.Hosts) != 3 {
		t.Fatalf("expected 3 versiond hosts (HA pair + solo), got %d", len(cfg.Hosts))
	}
}

// RouterSessionURL builds the sticky-routed path nginx hashes on the session id segment.
func RouterSessionURL(routerHTTP, version, sessionID, suffix string) string {
	return routerHTTP + "/" + version + "/sessions/" + sessionID + suffix
}

// GetResponseHeader performs GET and returns the named response header (body discarded).
func GetResponseHeader(client *http.Client, url, header string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Header.Get(header), nil
}

// RequireResponseHeader GETs url and requires a non-empty header value.
func RequireResponseHeader(t *testing.T, client *http.Client, url, header string) string {
	t.Helper()
	value, err := GetResponseHeader(client, url, header)
	require.NoError(t, err)
	require.NotEmpty(t, value, "missing response header %q from %s (rebuild versiond-router?)", header, url)
	return value
}
