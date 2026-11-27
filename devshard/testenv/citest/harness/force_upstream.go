package harness

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// PatchGatewayForceUpstreamStreaming sets force_upstream_streaming via POST /v1/admin/settings.
func PatchGatewayForceUpstreamStreaming(t *testing.T, client *http.Client, gatewayURL string, enabled bool) {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	body := map[string]any{"force_upstream_streaming": enabled}
	data, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, gatewayURL+"/v1/admin/settings", bytes.NewReader(data))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	setAdminBearer(req, TestenvAdminAPIKey)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST /v1/admin/settings: %s", string(raw))

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, enabled, got["force_upstream_streaming"], "force_upstream_streaming not applied: %s", string(raw))
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

// AnyChoiceCarriesLogprobs reports whether a choice holds a logprobs object; the fold path writes null when none arrived.
func AnyChoiceCarriesLogprobs(payload map[string]any) bool {
	choices, _ := payload["choices"].([]any)
	for _, raw := range choices {
		choice, _ := raw.(map[string]any)
		if logprobs, carried := choice["logprobs"].(map[string]any); carried && logprobs != nil {
			return true
		}
	}
	return false
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
