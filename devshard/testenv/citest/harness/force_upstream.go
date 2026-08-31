package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// BootStackWithAggregateByteLimits boots the standard 2×versiond stack after
// stamping GATEWAY_AGGREGATE_MAX_* on the generated compose (before first Up).
func BootStackWithAggregateByteLimits(t *testing.T, prefix string, maxMemoryBytes, maxResponseBytes int64) (*Stack, *config.File, Endpoints) {
	t.Helper()
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteStackConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	PatchComposeAggregateByteLimits(t, stack.ComposePath, maxMemoryBytes, maxResponseBytes)
	cfg := stack.LoadConfig(t)
	requireTwoVersiondHosts(t, cfg)
	stack.Up(t)
	client := GatewayChatClient()
	eps := stack.Endpoints(t, cfg)
	WaitStackHealthy(t, stack, eps)
	WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz", 5*time.Minute, "devshardd health via router", stack)
	return stack, cfg, eps
}

// PatchGatewayForceUpstreamStreaming sets redundancy.force_upstream_streaming via
// POST /v1/admin/settings (in-memory ApplyRedundancySettings for the process).
func PatchGatewayForceUpstreamStreaming(t *testing.T, client *http.Client, gatewayURL string, enabled bool) {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	body := map[string]any{
		"redundancy": map[string]any{
			"force_upstream_streaming": enabled,
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
	require.Equal(t, enabled, red["force_upstream_streaming"],
		"force_upstream_streaming not applied: %s", string(raw))
}

// PatchGatewayRedundancySpeedPolicy sets redundancy.speed_policy (legacy / hybrid / pairwise).
func PatchGatewayRedundancySpeedPolicy(t *testing.T, client *http.Client, gatewayURL, policy string) {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	body := map[string]any{
		"redundancy": map[string]any{
			"speed_policy": policy,
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
	require.Equal(t, policy, red["speed_policy"], "speed_policy not applied: %s", string(raw))
}

// PatchComposeAggregateByteLimits inserts GATEWAY_AGGREGATE_* byte caps on
// devshardctl. Caller must RecreateServices(t, "devshardctl") for them to take effect.
func PatchComposeAggregateByteLimits(t *testing.T, composePath string, maxMemoryBytes, maxResponseBytes int64) {
	t.Helper()
	lines := []string{
		fmt.Sprintf(`GATEWAY_AGGREGATE_MAX_MEMORY_BYTES: "%d"`, maxMemoryBytes),
		fmt.Sprintf(`GATEWAY_AGGREGATE_MAX_RESPONSE_BYTES: "%d"`, maxResponseBytes),
	}
	body, err := os.ReadFile(composePath)
	require.NoError(t, err)
	if strings.Contains(string(body), "GATEWAY_AGGREGATE_MAX_MEMORY_BYTES:") {
		PatchComposeEnvKey(t, composePath, "GATEWAY_AGGREGATE_MAX_MEMORY_BYTES", fmt.Sprintf(`"%d"`, maxMemoryBytes))
		PatchComposeEnvKey(t, composePath, "GATEWAY_AGGREGATE_MAX_RESPONSE_BYTES", fmt.Sprintf(`"%d"`, maxResponseBytes))
		return
	}
	PatchComposeInsertEnvAfter(t, composePath, "GATEWAY_MAX_TOKENS_CAP", lines...)
}

// RequireAggregateSpilledInGatewayLogs asserts proxy_request_completed logged a spill.
func RequireAggregateSpilledInGatewayLogs(t *testing.T, s *Stack) {
	t.Helper()
	out, err := s.ComposeLogsTail(400, "devshardctl")
	require.NoError(t, err)
	require.Contains(t, out, "aggregate_spilled=true",
		"expected aggregate spill in gateway logs")
}

// RequireAggregateSpoolDirEmpty asserts the bind-mounted aggregate spool has no
// leftover named files (unlinked-at-create + Close).
func RequireAggregateSpoolDirEmpty(t *testing.T, s *Stack) {
	t.Helper()
	dir := filepath.Join(s.WorkDir, "data", "devshardctl", "aggregate-spool")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		require.NoError(t, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	require.Empty(t, names, "aggregate spool should be empty after request close; got %v", names)
}

// SSEChunksHaveTopLevelUsage reports whether any chunk carries a non-null usage object.
func SSEChunksHaveTopLevelUsage(chunks []map[string]any) bool {
	for _, chunk := range chunks {
		u, ok := chunk["usage"]
		if !ok || u == nil {
			continue
		}
		if m, ok := u.(map[string]any); ok && len(m) > 0 {
			return true
		}
	}
	return false
}

// CountSSEUsageOnlyChunks counts chunks that have usage and no choices (or empty choices).
func CountSSEUsageOnlyChunks(chunks []map[string]any) int {
	n := 0
	for _, chunk := range chunks {
		u, ok := chunk["usage"]
		if !ok || u == nil {
			continue
		}
		m, ok := u.(map[string]any)
		if !ok || len(m) == 0 {
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			n++
		}
	}
	return n
}

// BodyMentionsForbiddenLogprobKeys reports client-leaked logprob field names.
func BodyMentionsForbiddenLogprobKeys(body []byte) bool {
	s := string(body)
	for _, key := range []string{`"logprobs"`, `"top_logprobs"`, `"token_ids"`, `"prompt_logprobs"`} {
		if strings.Contains(s, key) {
			// Allow "logprobs":null which OpenAI sometimes emits.
			if key == `"logprobs"` && (strings.Contains(s, `"logprobs":null`) || strings.Contains(s, `"logprobs": null`)) {
				// Still fail if a non-null logprobs object appears.
				if strings.Contains(s, `"logprobs":{`) || strings.Contains(s, `"logprobs": {`) {
					return true
				}
				continue
			}
			if key == `"logprobs"` {
				continue
			}
			return true
		}
	}
	return false
}

// LogprobContentEntryCount returns len(choices[0].logprobs.content) from a JSON body
// or SSE chunk map. Returns -1 when absent.
func LogprobContentEntryCount(payload map[string]any) int {
	choices, _ := payload["choices"].([]any)
	if len(choices) == 0 {
		return -1
	}
	choice, _ := choices[0].(map[string]any)
	if choice == nil {
		return -1
	}
	lp, ok := choice["logprobs"].(map[string]any)
	if !ok || lp == nil {
		return -1
	}
	content, _ := lp["content"].([]any)
	return len(content)
}

// MaxTopLogprobsWidth returns the widest top_logprobs array under logprobs.content.
func MaxTopLogprobsWidth(payload map[string]any) int {
	choices, _ := payload["choices"].([]any)
	if len(choices) == 0 {
		return 0
	}
	choice, _ := choices[0].(map[string]any)
	lp, _ := choice["logprobs"].(map[string]any)
	if lp == nil {
		return 0
	}
	content, _ := lp["content"].([]any)
	max := 0
	for _, e := range content {
		entry, _ := e.(map[string]any)
		tops, _ := entry["top_logprobs"].([]any)
		if len(tops) > max {
			max = len(tops)
		}
	}
	return max
}
