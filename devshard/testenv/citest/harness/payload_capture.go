package harness

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// EnablePayloadCapture sets DEVSHARD_LOG_PAYLOADS* for the gateway before
// UpWithObservability. Call after RunGencompose.
func EnablePayloadCapture(t *testing.T, stack *Stack, level string) {
	t.Helper()
	require.NotNil(t, stack)
	if level == "" {
		level = "full"
	}
	if stack.payloadEnv == nil {
		stack.payloadEnv = make(map[string]string)
	}
	stack.payloadEnv["DEVSHARD_LOG_PAYLOADS"] = level
	stack.payloadEnv["DEVSHARD_LOG_PAYLOADS_MLNODE"] = "true"
	stack.payloadEnv["DEVSHARD_LOG_PAYLOADS_QUARANTINE"] = "true"
	stack.payloadEnv["DEVSHARD_LOG_PAYLOADS_MAX_BYTES"] = "16384"
}

// WaitPayloadCapturedLog polls Loki for a payload_captured line containing needle.
func WaitPayloadCapturedLog(t *testing.T, obs ObservabilityEndpoints, needle string, timeout time.Duration) map[string]any {
	t.Helper()
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	client := &http.Client{Timeout: 10 * time.Second}
	query := `{compose_service="devshardctl"} | json | stage="payload_captured"`
	t.Logf("citest: waiting for payload_captured containing %q", needle)
	var found map[string]any
	ok := assertEventually(t, timeout, 3*time.Second, func() bool {
		for _, line := range lokiQueryLines(client, obs.Loki, query) {
			if needle != "" && !strings.Contains(line, needle) {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				continue
			}
			if stage, _ := m["stage"].(string); stage != "payload_captured" {
				continue
			}
			found = m
			return true
		}
		return false
	})
	require.True(t, ok, "no payload_captured Loki line with needle %q within %s", needle, timeout)
	return found
}

// WaitPayloadQuarantineLog polls Loki for a payload_quarantine size-only line.
func WaitPayloadQuarantineLog(t *testing.T, obs ObservabilityEndpoints, timeout time.Duration) map[string]any {
	t.Helper()
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	client := &http.Client{Timeout: 10 * time.Second}
	query := `{compose_service="devshardctl"} | json | stage="payload_quarantine"`
	t.Logf("citest: waiting for payload_quarantine")
	var found map[string]any
	ok := assertEventually(t, timeout, 3*time.Second, func() bool {
		for _, line := range lokiQueryLines(client, obs.Loki, query) {
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				continue
			}
			if stage, _ := m["stage"].(string); stage != "payload_quarantine" {
				continue
			}
			found = m
			return true
		}
		return false
	})
	require.True(t, ok, "no payload_quarantine Loki line within %s", timeout)
	return found
}

func lokiQueryLines(client *http.Client, baseURL, query string) []string {
	end := time.Now()
	start := end.Add(-15 * time.Minute)
	u, err := url.Parse(baseURL + "/loki/api/v1/query_range")
	if err != nil {
		return nil
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("limit", "100")
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	u.RawQuery = q.Encode()

	resp, err := client.Get(u.String())
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	var parsed struct {
		Data struct {
			Result []struct {
				Values [][]string `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	var lines []string
	for _, stream := range parsed.Data.Result {
		for _, pair := range stream.Values {
			if len(pair) >= 2 {
				lines = append(lines, pair[1])
			}
		}
	}
	return lines
}
