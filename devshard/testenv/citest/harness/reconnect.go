package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"testing"
	"time"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// BootReconnectStack boots a 2×versiond stack stamped with versionName
// (typically "v5" for same-nonce reconnect, or "v2" for the protocol-gate negative path).
// Requires a linux build/devshardd built with matching DEVSHARD_VERSION.
func BootReconnectStack(t *testing.T, prefix, versionName string) (*Stack, *config.File, Endpoints) {
	t.Helper()
	return bootReconnectStack(t, prefix, versionName, 0)
}

// BootReconnectStackWithPrimaryDetach is BootReconnectStack plus
// DEVSHARD_TEST_DETACH_PRIMARY_AFTER_WRITES patched before the first Up.
func BootReconnectStackWithPrimaryDetach(t *testing.T, prefix, versionName string, detachAfterWrites int) (*Stack, *config.File, Endpoints) {
	t.Helper()
	return bootReconnectStack(t, prefix, versionName, detachAfterWrites)
}

func bootReconnectStack(t *testing.T, prefix, versionName string, detachAfterWrites int) (*Stack, *config.File, Endpoints) {
	t.Helper()
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteMultiConfig(t, stack.WorkDir, MultiConfigOpts{
		Hosts:       2,
		EscrowSlots: 2,
		VersionName: versionName,
	})
	stack.RunGencompose(t)
	if detachAfterWrites > 0 {
		PatchComposeDetachPrimaryAfterWrites(t, stack.ComposePath, detachAfterWrites)
	}
	cfg := stack.LoadConfig(t)
	requireTwoVersiondHosts(t, cfg)
	require.Equal(t, versionName, cfg.Versiond.VersionName)
	stack.UpBuild(t)
	client := GatewayChatClient()
	eps := stack.Endpoints(t, cfg)
	WaitStackHealthy(t, stack, eps)
	WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz", 5*time.Minute, "devshardd health via router", stack)
	return stack, cfg, eps
}

// PatchGatewayAttemptReconnect enables/disables same-nonce reconnect via
// POST /v1/admin/settings (in-memory ApplyRedundancySettings for the process).
func PatchGatewayAttemptReconnect(t *testing.T, client *http.Client, gatewayURL string, enabled bool, budgetMS int64, maxTries int) {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	body := map[string]any{
		"redundancy": map[string]any{
			"attempt_reconnect_enabled":  enabled,
			"attempt_reconnect_budget_ms": budgetMS,
			"attempt_reconnect_max_tries": maxTries,
		},
	}
	data, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, gatewayURL+"/v1/admin/settings", bytes.NewReader(data))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+TestenvAdminAPIKey)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST /v1/admin/settings: %s", string(raw))

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	red, _ := got["redundancy"].(map[string]any)
	require.NotNil(t, red, "admin settings response missing redundancy")
	require.Equal(t, enabled, red["attempt_reconnect_enabled"])
}

// GatewaySessionVersion returns session_version from GET /v1/status when present.
// Empty is allowed: older/single-runtime clients and ≤v4 paths must not depend on
// this field. Reconnect e2e that needs a protocol bind should use
// RequireGatewaySessionVersion (v5) or cfg.Versiond.VersionName.
func GatewaySessionVersion(t *testing.T, client *http.Client, gatewayURL string) string {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	resp, err := client.Get(gatewayURL + "/v1/status")
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "GET /v1/status: %s", string(raw))
	var status struct {
		SessionVersion string `json:"session_version"`
	}
	require.NoError(t, json.Unmarshal(raw, &status))
	return status.SessionVersion
}

// RequireGatewaySessionVersion requires a non-empty session_version (v5 reconnect
// citest only). Production reconnect uses the in-process SM bind tag, not this field.
func RequireGatewaySessionVersion(t *testing.T, client *http.Client, gatewayURL string) string {
	t.Helper()
	ver := GatewaySessionVersion(t, client, gatewayURL)
	require.NotEmpty(t, ver, "session_version empty (required for v5 reconnect citest)")
	return ver
}

// RequireExactlyOneFinishedInference asserts /v1/debug/inferences has exactly one
// finished inference entry (one paid MsgFinishInference path).
func RequireExactlyOneFinishedInference(t *testing.T, client *http.Client, gatewayURL string) {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	deadline := time.Now().Add(2 * time.Minute)
	var last string
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, gatewayURL+"/v1/debug/inferences", nil)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		req.Header.Set("Authorization", "Bearer "+TestenvAdminAPIKey)
		resp, err := client.Do(req)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		last = string(raw)
		if resp.StatusCode != http.StatusOK {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		var payload struct {
			Inferences map[string]json.RawMessage `json:"inferences"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if len(payload.Inferences) == 1 {
			return
		}
		if len(payload.Inferences) > 1 {
			require.Fail(t, fmt.Sprintf("expected exactly one finished inference, got %d: %s", len(payload.Inferences), last))
		}
		time.Sleep(500 * time.Millisecond)
	}
	require.Fail(t, "timed out waiting for exactly one finished inference; last="+last)
}

// RequireGatewayReconnectResumed asserts gateway logs show a same-nonce resume
// (winner_reconnect_resumed) for the request under test.
func RequireGatewayReconnectResumed(t *testing.T, s *Stack) {
	t.Helper()
	out, err := s.ComposeLogsTail(400, "devshardctl")
	require.NoError(t, err)
	require.Contains(t, out, "winner_reconnect_resumed",
		"expected same-nonce reconnect resume in gateway logs")
}

// PatchComposeDetachPrimaryAfterWrites inserts
// DEVSHARD_TEST_DETACH_PRIMARY_AFTER_WRITES on every versiond service so a
// mid-stream primary detach is injected after N successful LiveStream primary
// writes. Caller must recreate versiond for the env to take effect.
func PatchComposeDetachPrimaryAfterWrites(t *testing.T, composePath string, n int) {
	t.Helper()
	key := "DEVSHARD_TEST_DETACH_PRIMARY_AFTER_WRITES"
	quoted := fmt.Sprintf("%q", fmt.Sprintf("%d", n))
	body, err := os.ReadFile(composePath)
	require.NoError(t, err)
	if regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `:`).Match(body) {
		PatchComposeEnvKey(t, composePath, key, quoted)
		return
	}
	PatchComposeInsertEnvAfterAll(t, composePath, "DEVSHARD_VALIDATION_LEASE_TTL", key+": "+quoted)
}
