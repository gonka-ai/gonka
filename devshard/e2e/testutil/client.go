package testutil

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type RawResponse struct {
	StatusCode  int
	ContentType string
	Body        string
	JSON        map[string]any
}

func LogRawResponse(t *testing.T, label string, resp RawResponse) {
	t.Helper()
	t.Logf("%s response: status=%d content_type=%q body=%s", label, resp.StatusCode, resp.ContentType, resp.Body)
}

func PostJSON(t *testing.T, client *http.Client, url string, body map[string]any) map[string]any {
	t.Helper()
	resp := PostJSONRaw(t, client, url, body, AdminAPIKey)
	require.Less(t, resp.StatusCode, 300, "POST %s returned %d: %s", url, resp.StatusCode, resp.Body)
	require.NotNil(t, resp.JSON, "response body should be JSON: %s", resp.Body)
	return resp.JSON
}

func PostJSONRaw(t *testing.T, client *http.Client, url string, body map[string]any, bearerToken string) RawResponse {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	return PostRaw(t, client, url, "application/json", data, bearerToken)
}

func PostRaw(t *testing.T, client *http.Client, url, contentType string, body []byte, bearerToken string) RawResponse {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	DebugLogf(t, "POST %s request=%s", url, string(body))

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	DebugLogf(t, "POST %s status=%d response=%s", url, resp.StatusCode, string(respBody))

	var decoded map[string]any
	if len(bytes.TrimSpace(respBody)) > 0 {
		_ = json.Unmarshal(respBody, &decoded)
	}
	return RawResponse{
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        string(respBody),
		JSON:        decoded,
	}
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
