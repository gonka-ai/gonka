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

func BootObservabilityStack(t *testing.T, prefix string) (*Stack, *config.File, Endpoints, ObservabilityEndpoints) {
	t.Helper()
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteStackConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	requireTwoVersiondHosts(t, cfg)
	stack.UpWithObservability(t, cfg)
	return stack, cfg, stack.Endpoints(t, cfg), ObservabilityEndpointsFor(stack.ObsProfile)
}

// BootObservabilityStackHASolo is the 3×versiond observability stack: HA pair
// (versiond-0/1, one on-chain participant) plus a solo executor (versiond-2).
// Stopping versiond-2 is the citest-reachable path that produces ghost burns;
// a 2-host HA-only stack cannot, because both hosts share one participant.
func BootObservabilityStackHASolo(t *testing.T, prefix string) (*Stack, *config.File, Endpoints, ObservabilityEndpoints) {
	t.Helper()
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteMultiConfig(t, stack.WorkDir, MultiConfigOpts{Hosts: 3, EscrowSlots: 4})
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	requireThreeVersiondHosts(t, cfg)
	stack.UpWithObservability(t, cfg)
	return stack, cfg, stack.Endpoints(t, cfg), ObservabilityEndpointsFor(stack.ObsProfile)
}

// BootPayloadCaptureStack boots an observability stack with DEVSHARD_LOG_PAYLOADS*
// enabled for the payload-capture citests. level should be "full" (testenv only)
// so partial response bodies appear on the payload_captured line.
func BootPayloadCaptureStack(t *testing.T, prefix, level string) (*Stack, *config.File, Endpoints, ObservabilityEndpoints) {
	t.Helper()
	if level == "" {
		level = "full"
	}
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteStackConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	requireTwoVersiondHosts(t, cfg)
	EnablePayloadCapture(t, stack, level)
	stack.UpWithObservability(t, cfg)
	return stack, cfg, stack.Endpoints(t, cfg), ObservabilityEndpointsFor(stack.ObsProfile)
}

// BootMLNodePoolStack boots a 2-host stack with N mock-openai instances.
func BootMLNodePoolStack(t *testing.T, prefix string, mlNodes int) (*Stack, *config.File, Endpoints) {
	t.Helper()
	if mlNodes < 2 {
		t.Fatalf("BootMLNodePoolStack requires mlNodes >= 2, got %d", mlNodes)
	}
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteMultiConfig(t, stack.WorkDir, MultiConfigOpts{Hosts: 2, EscrowSlots: 2, MLNodes: mlNodes})
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	requireTwoVersiondHosts(t, cfg)
	require.Equal(t, mlNodes, cfg.ResolvedMLNodes())
	stack.Up(t)
	return stack, cfg, stack.Endpoints(t, cfg)
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
