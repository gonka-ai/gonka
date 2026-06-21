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
//  1. Start the default three-host environment with persistent SQLite volumes.
//  2. Send several OpenAI-style chat completion requests through devshardctl.
//  3. Capture the session nonce before restart.
//  4. Restart one devshard-host container with the same SQLite volume.
//  5. Continue sending requests through devshardctl.
//  6. Assert the session nonce did not regress.
//  7. Finalize the devshard session and verify the settlement contract.
func TestE2E_SQLiteHostRestartRecovery(t *testing.T) {
	requireE2EEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	images := requiredImages(t)
	env := startE2EEnv(ctx, t, images, e2eEnvOptions{
		hostVolumeNames: sqliteHostVolumeNames(t),
	})

	client := &http.Client{Timeout: defaultRequestTimeout}
	for i := 0; i < 3; i++ {
		sendCompletion(t, client, env.clientURL, fmt.Sprintf("sqlite host restart before %d", i+1))
	}
	beforeRestart := latestSessionNonce(t, client, env.clientURL)

	env.restartHost(ctx, t, 1)

	for i := 0; i < 3; i++ {
		sendCompletion(t, client, env.clientURL, fmt.Sprintf("sqlite host restart after %d", i+1))
	}
	afterRestart := latestSessionNonce(t, client, env.clientURL)
	require.GreaterOrEqual(t, afterRestart, beforeRestart, "session nonce should not regress after host restart")

	settlement := finalizeSession(t, client, env.clientURL)
	requireSettlementContract(t, settlement)
	requireSettlementHostStats(t, settlement)
}

// Test flow:
//  1. Start the default three-host environment with persistent SQLite volumes.
//  2. Send several OpenAI-style chat completion requests through devshardctl.
//  3. Capture the session nonce before restart.
//  4. Restart all devshard-host containers with their original SQLite volumes.
//  5. Continue sending requests through devshardctl.
//  6. Assert the session nonce did not regress.
//  7. Finalize the devshard session and verify the settlement contract.
func TestE2E_SQLiteAllHostsRestartRecovery(t *testing.T) {
	requireE2EEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	images := requiredImages(t)
	env := startE2EEnv(ctx, t, images, e2eEnvOptions{
		hostVolumeNames: sqliteHostVolumeNames(t),
	})

	client := &http.Client{Timeout: defaultRequestTimeout}
	for i := 0; i < 3; i++ {
		sendCompletion(t, client, env.clientURL, fmt.Sprintf("sqlite all-host restart before %d", i+1))
	}
	beforeRestart := latestSessionNonce(t, client, env.clientURL)

	env.restartAllHosts(ctx, t)

	for i := 0; i < 3; i++ {
		sendCompletion(t, client, env.clientURL, fmt.Sprintf("sqlite all-host restart after %d", i+1))
	}
	afterRestart := latestSessionNonce(t, client, env.clientURL)
	require.GreaterOrEqual(t, afterRestart, beforeRestart, "session nonce should not regress after all-host restart")

	settlement := finalizeSession(t, client, env.clientURL)
	requireSettlementContract(t, settlement)
	requireSettlementHostStats(t, settlement)
}

func latestSessionNonce(t *testing.T, client *http.Client, clientURL string) uint64 {
	t.Helper()
	state := getJSON(t, client, clientURL+"/v1/state")
	session, ok := state["session"].(map[string]any)
	require.True(t, ok, "state session should be an object")
	return numericField(t, session, "latest_nonce")
}

func finalizeSession(t *testing.T, client *http.Client, clientURL string) map[string]any {
	t.Helper()
	debugLogf(t, "finalizing devshard session")
	settlement := postJSON(t, client, clientURL+"/v1/finalize", map[string]any{})
	settlementJSON, err := json.MarshalIndent(settlement, "", "  ")
	require.NoError(t, err)
	t.Logf("SettlementContract:\n%s", settlementJSON)
	return settlement
}
