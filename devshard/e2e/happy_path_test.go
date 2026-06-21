package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Send three OpenAI-style chat completion requests through devshardctl.
//  2. Assert that every completion response includes choices.
//  3. Drive extra completion rounds until one host reaches 2/2 validations.
//  4. Finalize the devshard session through devshardctl.
//  5. Verify the stable settlement contract:
//     escrow id, version, state root, nonce, signatures, and no duplicate
//     signature slot ids, with completed validations included.
func TestE2E_HappyPath(t *testing.T) {
	requireE2EEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	images := requiredImages(t)
	env := startHappyPathEnv(ctx, t, images)

	client := &http.Client{Timeout: defaultRequestTimeout}
	for i := 0; i < 3; i++ {
		sendCompletion(t, client, env.clientURL, fmt.Sprintf("hello %d", i+1))
	}

	driveUntilValidationObserved(t, client, env.clientURL)

	debugLogf(t, "finalizing devshard session")
	settlement := postJSON(t, client, env.clientURL+"/v1/finalize", map[string]any{})
	settlementJSON, err := json.MarshalIndent(settlement, "", "  ")
	require.NoError(t, err)
	t.Logf("SettlementContract:\n%s", settlementJSON)
	requireSettlementContract(t, settlement)
	requireValidationTargetContract(t, settlement, 2)
}

func sendCompletion(t *testing.T, client *http.Client, clientURL, content string) {
	t.Helper()
	debugLogf(t, "sending completion request content=%q", content)
	resp := postJSON(t, client, clientURL+"/v1/chat/completions", map[string]any{
		"model": "stub-model",
		"messages": []map[string]string{
			{"role": "user", "content": content},
		},
		"max_tokens": 32,
	})
	require.NotEmpty(t, resp["choices"], "completion response should include choices")
}

func driveUntilValidationObserved(t *testing.T, client *http.Client, clientURL string) {
	t.Helper()
	const maxExtraCompletions = 20
	const validationTarget = 2
	for attempt := 0; attempt <= maxExtraCompletions; attempt++ {
		state := getJSON(t, client, clientURL+"/v1/state")
		reached, summary := hasInferenceValidationTarget(t, state, validationTarget)
		debugLogf(t, "inference validation evidence before finalize target=%d reached=%t (%s)",
			validationTarget, reached, summary)
		if reached {
			return
		}
		if attempt == maxExtraCompletions {
			t.Fatalf("no host reached %d/%d validations before finalize after %d extra completion rounds: %s",
				validationTarget, validationTarget, maxExtraCompletions, summary)
		}
		sendCompletion(t, client, clientURL, fmt.Sprintf("validation probe %d", attempt+1))
		time.Sleep(250 * time.Millisecond)
	}
}
