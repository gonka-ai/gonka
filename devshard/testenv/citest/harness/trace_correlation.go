package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// RequireLogsForTrace asserts Loki has at least one JSON log line for traceID
// from each compose_service regex.
func RequireLogsForTrace(t *testing.T, obs ObservabilityEndpoints, traceID string, composeServices []string, timeout time.Duration) {
	t.Helper()
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	require.NotEmpty(t, traceID)
	require.NotEmpty(t, composeServices)
	client := &http.Client{Timeout: 10 * time.Second}

	for _, svc := range composeServices {
		query := fmt.Sprintf(`{compose_service=~%q} | json | trace_id=%q`, svc, traceID)
		t.Logf("citest: waiting for Loki logs trace_id=%s compose_service=~%s", traceID, svc)
		ok := assertEventually(t, timeout, 3*time.Second, func() bool {
			return lokiQueryHasStreams(client, obs.Loki, query)
		})
		require.True(t, ok, "Loki missing logs for trace_id=%s service=~%s within %s (query: %s)",
			traceID, svc, timeout, query)
	}
}

// PostGatewayChatCompletionEx is like PostGatewayChatCompletion but also
// returns response headers (e.g. X-Request-Id).
func PostGatewayChatCompletionEx(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string, req ChatCompletionRequest) (ChatCompletionResponse, http.Header) {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)
	httpReq, err := http.NewRequest(http.MethodPost, gatewayURL+"/v1/chat/completions", bytes.NewReader(data))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	if adminAPIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+adminAPIKey)
	}
	resp, err := client.Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Less(t, resp.StatusCode, 300, "POST chat: %d %s", resp.StatusCode, string(body))

	var out ChatCompletionResponse
	require.NoError(t, json.Unmarshal(body, &out))
	require.NotEmpty(t, out.Choices, "gateway chat returned no choices")
	require.NotEmpty(t, out.Choices[0].Message.Content, "empty assistant content")
	return out, resp.Header.Clone()
}

func jaegerTraceCoveringServices(client *http.Client, baseURL string, wantServices []string) (string, bool) {
	// Search from the first service; then filter traces that include all services.
	u, err := url.Parse(baseURL + "/jaeger/api/traces")
	if err != nil {
		return "", false
	}
	q := u.Query()
	q.Set("service", wantServices[0])
	q.Set("limit", "20")
	q.Set("lookback", "1h")
	u.RawQuery = q.Encode()

	resp, err := client.Get(u.String())
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false
	}

	var parsed struct {
		Data []struct {
			TraceID   string `json:"traceID"`
			Processes map[string]struct {
				ServiceName string `json:"serviceName"`
			} `json:"processes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", false
	}
	for _, tr := range parsed.Data {
		have := make(map[string]struct{})
		for _, p := range tr.Processes {
			have[p.ServiceName] = struct{}{}
		}
		all := true
		for _, svc := range wantServices {
			if _, ok := have[svc]; !ok {
				all = false
				break
			}
		}
		if all && tr.TraceID != "" {
			return tr.TraceID, true
		}
	}
	return "", false
}

func lokiQueryHasStreams(client *http.Client, baseURL, query string) bool {
	end := time.Now()
	start := end.Add(-15 * time.Minute)
	u, err := url.Parse(baseURL + "/loki/api/v1/query_range")
	if err != nil {
		return false
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("limit", "50")
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	u.RawQuery = q.Encode()

	resp, err := client.Get(u.String())
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}
	var parsed struct {
		Data struct {
			Result []struct {
				Values [][]string `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return strings.Contains(string(body), `"values"`)
	}
	for _, stream := range parsed.Data.Result {
		if len(stream.Values) > 0 {
			return true
		}
	}
	return false
}
