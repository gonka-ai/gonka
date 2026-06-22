package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func SendCompletion(t *testing.T, client *http.Client, clientURL, content string) {
	t.Helper()
	DebugLogf(t, "sending completion request content=%q", content)
	resp := PostJSON(t, client, clientURL+"/v1/chat/completions", map[string]any{
		"model": "stub-model",
		"messages": []map[string]string{
			{"role": "user", "content": content},
		},
		"max_tokens": 32,
	})
	require.NotEmpty(t, resp["choices"], "completion response should include choices")
}

func SendCompletions(t *testing.T, client *http.Client, clientURL, contentPrefix string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		SendCompletion(t, client, clientURL, fmt.Sprintf("%s %d", contentPrefix, i+1))
	}
}

func DriveUntilValidationObserved(t *testing.T, client *http.Client, clientURL string) {
	t.Helper()
	const maxExtraCompletions = 20
	const validationTarget = 2
	for attempt := 0; attempt <= maxExtraCompletions; attempt++ {
		state := GetJSON(t, client, clientURL+"/v1/state")
		reached, summary := HasInferenceValidationTarget(t, state, validationTarget)
		DebugLogf(t, "inference validation evidence before finalize target=%d reached=%t (%s)",
			validationTarget, reached, summary)
		if reached {
			return
		}
		if attempt == maxExtraCompletions {
			t.Fatalf("no host reached %d/%d validations before finalize after %d extra completion rounds: %s",
				validationTarget, validationTarget, maxExtraCompletions, summary)
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

func FinalizeSession(t *testing.T, client *http.Client, clientURL string) map[string]any {
	t.Helper()
	DebugLogf(t, "finalizing devshard session")
	settlement := PostJSON(t, client, clientURL+"/v1/finalize", map[string]any{})
	settlementJSON, err := json.MarshalIndent(settlement, "", "  ")
	require.NoError(t, err)
	t.Logf("SettlementContract:\n%s", settlementJSON)
	return settlement
}
