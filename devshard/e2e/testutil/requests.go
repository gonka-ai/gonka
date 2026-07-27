package testutil

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func SendCompletion(t *testing.T, client *http.Client, clientURL, content string) map[string]any {
	t.Helper()
	DebugLogf(t, "sending completion request content=%q", content)
	resp := PostJSON(t, client, clientURL+"/v1/chat/completions", ChatCompletionBody(content, false))
	require.NotEmpty(t, resp["choices"], "completion response should include choices")
	return resp
}

func SendCompletionRaw(t *testing.T, client *http.Client, clientURL, content, bearerToken string) RawResponse {
	t.Helper()
	DebugLogf(t, "sending raw completion request content=%q bearer_set=%t", content, bearerToken != "")
	return PostJSONRaw(t, client, clientURL+"/v1/chat/completions", ChatCompletionBody(content, false), bearerToken)
}

type StreamResponse struct {
	ContentType string
	Events      []string
}

func SendStreamingCompletion(t *testing.T, client *http.Client, clientURL, content string) StreamResponse {
	t.Helper()
	DebugLogf(t, "sending streaming completion request content=%q", content)

	data, err := json.Marshal(ChatCompletionBody(content, true))
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, clientURL+"/v1/chat/completions", strings.NewReader(string(data)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+AdminAPIKey)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, events := readSSEEvents(t, resp.Body)
	DebugLogf(t, "streaming completion status=%d content_type=%q body=%s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	require.Less(t, resp.StatusCode, 300, "streaming completion returned %d: %s", resp.StatusCode, body)

	return StreamResponse{
		ContentType: resp.Header.Get("Content-Type"),
		Events:      events,
	}
}

func SendCompletions(t *testing.T, client *http.Client, clientURL, contentPrefix string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		SendCompletion(t, client, clientURL, fmt.Sprintf("%s %d", contentPrefix, i+1))
	}
}

func ChatCompletionBody(content string, stream bool) map[string]any {
	body := map[string]any{
		"model": "stub-model",
		"messages": []map[string]string{
			{"role": "user", "content": content},
		},
		"max_tokens": 32,
	}
	if stream {
		body["stream"] = true
	}
	return body
}

func readSSEEvents(t *testing.T, body io.Reader) (string, []string) {
	t.Helper()
	var raw strings.Builder
	var events []string
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		raw.WriteString(line)
		raw.WriteByte('\n')
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		events = append(events, strings.TrimPrefix(line, "data: "))
	}
	require.NoError(t, scanner.Err())
	return raw.String(), events
}

func DriveUntilValidationObserved(t *testing.T, client *http.Client, clientURL string) {
	t.Helper()
	const maxExtraCompletions = 20
	const validationTarget = 2
	for attempt := 0; attempt <= maxExtraCompletions; attempt++ {
		state := GetJSON(t, client, clientURL+"/v1/debug/inferences")
		reached, summary := HasInferenceValidationTarget(t, state, validationTarget)
		DebugLogf(t, "inference validation evidence before finalize target=%d reached=%t (%s)",
			validationTarget, reached, summary)
		if reached {
			return
		}
		if attempt == maxExtraCompletions {
			t.Fatalf("no host reached at least %d completed validations before finalize after %d extra completion rounds: %s",
				validationTarget, maxExtraCompletions, summary)
		}
		SendCompletion(t, client, clientURL, fmt.Sprintf("validation probe %d", attempt+1))
		time.Sleep(250 * time.Millisecond)
	}
}

func LatestSessionNonce(t *testing.T, client *http.Client, clientURL string) uint64 {
	t.Helper()
	state := GetJSON(t, client, clientURL+"/v1/state")
	session, ok := state["session"].(map[string]any)
	require.True(t, ok, "state session should be an object")
	return NumericField(t, session, "latest_nonce")
}

func GetSignatureStatus(t *testing.T, client *http.Client, clientURL string) map[string]any {
	t.Helper()
	return GetJSON(t, client, clientURL+"/v1/debug/signatures")
}

func GetStatus(t *testing.T, client *http.Client, clientURL string) map[string]any {
	t.Helper()
	return GetJSON(t, client, clientURL+"/v1/status")
}

func CollectSignatures(t *testing.T, client *http.Client, clientURL string, nonce uint64) map[string]any {
	t.Helper()
	return PostJSON(t, client, fmt.Sprintf("%s/v1/debug/signatures/collect?nonce=%d", clientURL, nonce), map[string]any{})
}

func FinalizeSession(t *testing.T, client *http.Client, clientURL string) map[string]any {
	t.Helper()
	DebugLogf(t, "finalizing devshard session")
	settlement := PostJSON(t, client, clientURL+"/v1/finalize", map[string]any{})
	settlementJSON, err := json.MarshalIndent(settlement, "", "  ")
	require.NoError(t, err)
	t.Logf("SettlementContract:\n%s", settlementJSON)
	return settlement
}
