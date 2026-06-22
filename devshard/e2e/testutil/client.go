package testutil

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func PostJSON(t *testing.T, client *http.Client, url string, body map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+AdminAPIKey)
	DebugLogf(t, "POST %s request=%s", url, string(data))

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	DebugLogf(t, "POST %s status=%d response=%s", url, resp.StatusCode, string(respBody))
	require.Less(t, resp.StatusCode, 300, "POST %s returned %d: %s", url, resp.StatusCode, string(respBody))

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(respBody, &decoded), "response body: %s", string(respBody))
	return decoded
}

func GetJSON(t *testing.T, client *http.Client, url string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+AdminAPIKey)
	DebugLogf(t, "GET %s", url)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	DebugLogf(t, "GET %s status=%d response=%s", url, resp.StatusCode, string(respBody))
	require.Less(t, resp.StatusCode, 300, "GET %s returned %d: %s", url, resp.StatusCode, string(respBody))

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(respBody, &decoded), "response body: %s", string(respBody))
	return decoded
}
