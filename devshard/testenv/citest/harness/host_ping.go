package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// ExpectedHostPingDial is the in-network dial base the gateway probes
// (escrow slot URL origin, no RoutePrefix).
const ExpectedHostPingDial = config.DefaultEscrowSlotURL

// BootHostPingStack boots the standard stack with a fast gateway host-ping
// interval so freshness advances inside citest timeouts.
func BootHostPingStack(t *testing.T, prefix string) (*Stack, *config.File, Endpoints) {
	t.Helper()
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteStackConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	PatchComposeEnvKey(t, stack.ComposePath, "DEVSHARD_GATEWAY_HOST_PING_INTERVAL", `"3s"`)
	PatchComposeEnvKey(t, stack.ComposePath, "DEVSHARD_GATEWAY_HOST_PING_TIMEOUT", `"1s"`)
	cfg := stack.LoadConfig(t)
	requireTwoVersiondHosts(t, cfg)
	stack.Up(t)
	eps := stack.Endpoints(t, cfg)
	client := GatewayChatClient()
	WaitStackHealthy(t, stack, eps)
	WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz", 5*time.Minute, "devshardd health via router", stack)
	return stack, cfg, eps
}

// GatewayMetricsURL is the gateway Prometheus exposition.
func GatewayMetricsURL(eps Endpoints) string {
	return eps.GatewayHTTP + "/metrics"
}

// FetchChildPingHeaders GETs /{version}/clock via the router and returns status + headers.
func FetchChildPingHeaders(t *testing.T, client *http.Client, routerHTTP, version string) (status int, recv, send string) {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	url := fmt.Sprintf("%s/%s/clock", routerHTTP, version)
	resp, err := client.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("X-Server-Recv-Ns"), resp.Header.Get("X-Server-Send-Ns")
}

// PostAdminCreateEscrow creates a registered escrow via gateway admin API.
func PostAdminCreateEscrow(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, modelID string, amount uint64) map[string]any {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	payload, err := json.Marshal(map[string]any{
		"amount":   amount,
		"model_id": modelID,
		"register": true,
	})
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, gatewayURL+"/v1/admin/escrows", bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+adminAPIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST /v1/admin/escrows: %s", string(body))
	var created map[string]any
	require.NoError(t, json.Unmarshal(body, &created))
	require.NotZero(t, created["escrow_id"])
	return created
}
