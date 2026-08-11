package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
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

	client := &http.Client{Timeout: testutil.DefaultRequestTimeout}
	testutil.SendCompletions(t, client, env.clientURL, "sqlite host restart before", 3)
	beforeRestart := testutil.LatestSessionNonce(t, client, env.clientURL)

	env.restartHost(ctx, t, 1)

	testutil.SendCompletions(t, client, env.clientURL, "sqlite host restart after", 3)
	afterRestart := testutil.LatestSessionNonce(t, client, env.clientURL)
	require.GreaterOrEqual(t, afterRestart, beforeRestart, "session nonce should not regress after host restart")

	settlement := testutil.FinalizeSession(t, client, env.clientURL)
	testutil.RequireSettlementContract(t, settlement)
	testutil.RequireSettlementHostStats(t, settlement, len(testutil.HostPrivateKeys))
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

	client := &http.Client{Timeout: testutil.DefaultRequestTimeout}
	testutil.SendCompletions(t, client, env.clientURL, "sqlite all-host restart before", 3)
	beforeRestart := testutil.LatestSessionNonce(t, client, env.clientURL)

	env.restartAllHosts(ctx, t)

	testutil.SendCompletions(t, client, env.clientURL, "sqlite all-host restart after", 3)
	afterRestart := testutil.LatestSessionNonce(t, client, env.clientURL)
	require.GreaterOrEqual(t, afterRestart, beforeRestart, "session nonce should not regress after all-host restart")

	settlement := testutil.FinalizeSession(t, client, env.clientURL)
	testutil.RequireSettlementContract(t, settlement)
	testutil.RequireSettlementHostStats(t, settlement, len(testutil.HostPrivateKeys))
}

// Test flow:
//  1. Start the default three-host environment with Postgres-backed hosts.
//  2. Send several OpenAI-style chat completion requests through devshardctl.
//  3. Capture the session nonce before restart.
//  4. Restart one devshard-host container against its original Postgres database.
//  5. Continue sending requests through devshardctl.
//  6. Assert the session nonce did not regress.
//  7. Finalize the devshard session and verify the settlement contract.
func TestE2E_PostgresHostRestartRecovery(t *testing.T) {
	requireE2EEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	images := requiredImages(t)
	env := startE2EEnv(ctx, t, images, e2eEnvOptions{
		usePostgresStorage: true,
	})

	client := &http.Client{Timeout: testutil.DefaultRequestTimeout}
	testutil.SendCompletions(t, client, env.clientURL, "postgres host restart before", 3)
	beforeRestart := testutil.LatestSessionNonce(t, client, env.clientURL)

	env.restartHost(ctx, t, 1)

	testutil.SendCompletions(t, client, env.clientURL, "postgres host restart after", 3)
	afterRestart := testutil.LatestSessionNonce(t, client, env.clientURL)
	require.GreaterOrEqual(t, afterRestart, beforeRestart, "session nonce should not regress after Postgres-backed host restart")

	settlement := testutil.FinalizeSession(t, client, env.clientURL)
	testutil.RequireSettlementContract(t, settlement)
	testutil.RequireSettlementHostStats(t, settlement, len(testutil.HostPrivateKeys))
}

// Test flow:
//  1. Start the default three-host environment with Postgres-backed hosts.
//  2. Send several OpenAI-style chat completion requests through devshardctl.
//  3. Capture the session nonce before restart.
//  4. Restart all devshard-host containers against their original Postgres databases.
//  5. Continue sending requests through devshardctl.
//  6. Assert the session nonce did not regress.
//  7. Finalize the devshard session and verify the settlement contract.
func TestE2E_PostgresAllHostsRestartRecovery(t *testing.T) {
	requireE2EEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	images := requiredImages(t)
	env := startE2EEnv(ctx, t, images, e2eEnvOptions{
		usePostgresStorage: true,
	})

	client := &http.Client{Timeout: testutil.DefaultRequestTimeout}
	testutil.SendCompletions(t, client, env.clientURL, "postgres all-host restart before", 3)
	beforeRestart := testutil.LatestSessionNonce(t, client, env.clientURL)

	env.restartAllHosts(ctx, t)

	testutil.SendCompletions(t, client, env.clientURL, "postgres all-host restart after", 3)
	afterRestart := testutil.LatestSessionNonce(t, client, env.clientURL)
	require.GreaterOrEqual(t, afterRestart, beforeRestart, "session nonce should not regress after all Postgres-backed hosts restart")

	settlement := testutil.FinalizeSession(t, client, env.clientURL)
	testutil.RequireSettlementContract(t, settlement)
	testutil.RequireSettlementHostStats(t, settlement, len(testutil.HostPrivateKeys))
}
