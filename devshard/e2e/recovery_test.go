package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
)

// Test flow:
//  1. Start the default three-host environment with persistent SQLite volumes.
//  2. Send several OpenAI-style chat completion requests through the gateway.
//  3. Capture the session nonce before restart.
//  4. Restart one devshard-host container with the same SQLite volume.
//  5. Continue sending requests through the gateway.
//  6. Assert the session nonce did not regress.
//  7. Finalize the escrow and assert it left the active phase at a finalize nonce with signature quorum.
func TestE2E_SQLiteHostRestartRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)

	env, client := startGatewayEnv(t, e2eEnvOptions{
		hostVolumeNames: sqliteHostVolumeNames(t),
	})

	testutil.SendCompletions(t, client, env.clientURL, "sqlite host restart before", 3)
	beforeRestart := gatewaySessionNonce(t, client, env.clientURL)

	env.restartHost(ctx, t, 1)

	testutil.SendCompletions(t, client, env.clientURL, "sqlite host restart after", 3)
	afterRestart := gatewaySessionNonce(t, client, env.clientURL)
	require.GreaterOrEqual(t, afterRestart, beforeRestart, "session nonce should not regress after host restart")

	testutil.FinalizeSession(t, client, env.escrowURL())
	testutil.RequireFinalized(t, client, env.escrowURL())
}

// Test flow:
//  1. Start the default three-host environment with persistent SQLite volumes.
//  2. Send several OpenAI-style chat completion requests through the gateway.
//  3. Capture the session nonce before restart.
//  4. Restart all devshard-host containers with their original SQLite volumes.
//  5. Continue sending requests through the gateway.
//  6. Assert the session nonce did not regress.
//  7. Finalize the escrow and assert it left the active phase at a finalize nonce with signature quorum.
func TestE2E_SQLiteAllHostsRestartRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)

	env, client := startGatewayEnv(t, e2eEnvOptions{
		hostVolumeNames: sqliteHostVolumeNames(t),
	})

	testutil.SendCompletions(t, client, env.clientURL, "sqlite all-host restart before", 3)
	beforeRestart := gatewaySessionNonce(t, client, env.clientURL)

	env.restartAllHosts(ctx, t)

	testutil.SendCompletions(t, client, env.clientURL, "sqlite all-host restart after", 3)
	afterRestart := gatewaySessionNonce(t, client, env.clientURL)
	require.GreaterOrEqual(t, afterRestart, beforeRestart, "session nonce should not regress after all-host restart")

	testutil.FinalizeSession(t, client, env.escrowURL())
	testutil.RequireFinalized(t, client, env.escrowURL())
}

// Test flow:
//  1. Start the default three-host environment with Postgres-backed hosts.
//  2. Send several OpenAI-style chat completion requests through the gateway.
//  3. Capture the session nonce before restart.
//  4. Restart one devshard-host container against its original Postgres database.
//  5. Continue sending requests through the gateway.
//  6. Assert the session nonce did not regress.
//  7. Finalize the escrow and assert it left the active phase at a finalize nonce with signature quorum.
func TestE2E_PostgresHostRestartRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)

	env, client := startGatewayEnv(t, e2eEnvOptions{
		usePostgresStorage: true,
	})

	testutil.SendCompletions(t, client, env.clientURL, "postgres host restart before", 3)
	beforeRestart := gatewaySessionNonce(t, client, env.clientURL)

	env.restartHost(ctx, t, 1)

	testutil.SendCompletions(t, client, env.clientURL, "postgres host restart after", 3)
	afterRestart := gatewaySessionNonce(t, client, env.clientURL)
	require.GreaterOrEqual(t, afterRestart, beforeRestart, "session nonce should not regress after Postgres-backed host restart")

	testutil.FinalizeSession(t, client, env.escrowURL())
	testutil.RequireFinalized(t, client, env.escrowURL())
}

// Test flow:
//  1. Start the default three-host environment with Postgres-backed hosts.
//  2. Send several OpenAI-style chat completion requests through the gateway.
//  3. Capture the session nonce before restart.
//  4. Restart all devshard-host containers against their original Postgres databases.
//  5. Continue sending requests through the gateway.
//  6. Assert the session nonce did not regress.
//  7. Finalize the escrow and assert it left the active phase at a finalize nonce with signature quorum.
func TestE2E_PostgresAllHostsRestartRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)

	env, client := startGatewayEnv(t, e2eEnvOptions{
		usePostgresStorage: true,
	})

	testutil.SendCompletions(t, client, env.clientURL, "postgres all-host restart before", 3)
	beforeRestart := gatewaySessionNonce(t, client, env.clientURL)

	env.restartAllHosts(ctx, t)

	testutil.SendCompletions(t, client, env.clientURL, "postgres all-host restart after", 3)
	afterRestart := gatewaySessionNonce(t, client, env.clientURL)
	require.GreaterOrEqual(t, afterRestart, beforeRestart, "session nonce should not regress after all Postgres-backed hosts restart")

	testutil.FinalizeSession(t, client, env.escrowURL())
	testutil.RequireFinalized(t, client, env.escrowURL())
}
