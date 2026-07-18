package harness

import (
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// BootS1Stack renders the 2×versiond citest config, starts compose, and returns handles.
func BootS1Stack(t *testing.T, prefix string) (*Stack, *config.File, Endpoints) {
	t.Helper()
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteS1Config(t, stack.WorkDir)
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	requireTwoVersiondHosts(t, cfg)
	stack.Up(t)
	return stack, cfg, stack.Endpoints(t, cfg)
}

// BootS1StackBuild is like BootS1Stack but rebuilds compose images first (devshardctl gRPC wiring).
func BootS1StackBuild(t *testing.T, prefix string) (*Stack, *config.File, Endpoints) {
	t.Helper()
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteS1Config(t, stack.WorkDir)
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	requireTwoVersiondHosts(t, cfg)
	RequireGatewayGRPCOnlyCompose(t, stack.ComposePath)
	stack.UpBuild(t)
	return stack, cfg, stack.Endpoints(t, cfg)
}

func BootS1ObsStack(t *testing.T, prefix string) (*Stack, *config.File, Endpoints, ObservabilityEndpoints) {
	t.Helper()
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteS1Config(t, stack.WorkDir)
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	requireTwoVersiondHosts(t, cfg)
	stack.UpWithObservability(t, cfg)
	return stack, cfg, stack.Endpoints(t, cfg), DefaultObservabilityEndpoints()
}

// WaitS1Healthy polls the S1 boundary health endpoints (chain, dapi, router, gateway).
func WaitS1Healthy(t *testing.T, stack *Stack, eps Endpoints) {
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

// RouterSessionURL builds the sticky-routed path nginx hashes on the session id segment.
func RouterSessionURL(routerHTTP, version, sessionID, suffix string) string {
	return routerHTTP + "/" + version + "/sessions/" + sessionID + suffix
}

// GetResponseHeader performs GET and returns the named response header (body discarded).
func GetResponseHeader(client *http.Client, url, header string) (string, error) {
	value, _, err := getResponseHeader(client, url, header)
	return value, err
}

// GetSuccessfulResponseHeader also requires a successful HTTP response.
func GetSuccessfulResponseHeader(client *http.Client, url, header string) (string, error) {
	value, status, err := getResponseHeader(client, url, header)
	if err != nil {
		return "", err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return "", fmt.Errorf("GET %s returned status %d", url, status)
	}
	return value, nil
}

func getResponseHeader(client *http.Client, url, header string) (string, int, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Header.Get(header), resp.StatusCode, nil
}

// RequireResponseHeader GETs url and requires a non-empty header value.
func RequireResponseHeader(t *testing.T, client *http.Client, url, header string) string {
	t.Helper()
	value, err := GetResponseHeader(client, url, header)
	require.NoError(t, err)
	require.NotEmpty(t, value, "missing response header %q from %s (rebuild versiond-router?)", header, url)
	return value
}

// RequireSuccessfulResponseHeader requires both 2xx and a non-empty header.
func RequireSuccessfulResponseHeader(
	t *testing.T,
	client *http.Client,
	url string,
	header string,
) string {
	t.Helper()
	value, err := GetSuccessfulResponseHeader(client, url, header)
	require.NoError(t, err)
	require.NotEmpty(t, value, "missing response header %q from %s", header, url)
	return value
}
