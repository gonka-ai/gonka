//go:build testenvci

package citest

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

const (
	migrationBackendHeader = "X-Versiond-Backend"
	migrationLegacyBackend = "versiond_legacy"
	migrationHAPrefix      = "versiond_pool_"
	nonHAVersion           = "v1"
)

// TestSQLiteToPostgresHAMigration walks pr-1366-deploy-test-plan.md §3.3 Phases 0–4:
// single-host SQLite → multi-host Devshard-Ha 503 → postgres migrate → HA serve.
func TestSQLiteToPostgresHAMigration(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootSQLiteHAMigrationStack(t, "citest-sqlite-ha-migration-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "versiond-0", "versiond-1", "versiond-router", "devshardctl", "devshard-postgres")
		}
	})

	legacyHost := cfg.Hosts[0].ID
	secondHost := cfg.Hosts[1].ID
	haVersion := cfg.Versiond.VersionName
	require.NotEqual(t, nonHAVersion, haVersion)

	harness.Step(t, "phase 0: single-host pool on %s, storage=sqlite, not declared HA", legacyHost)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/healthz", 5*time.Minute, "versiond-router healthz", stack)
	harness.WaitGETOK(t, client, eps.GatewayHTTP+"/v1/status", 5*time.Minute, "gateway status", stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+haVersion+"/healthz", 5*time.Minute, "devshardd health (no Devshard-Ha)", stack)

	for i := 0; i < 8; i++ {
		url := harness.RouterSessionURL(eps.RouterHTTP, nonHAVersion, fmt.Sprintf("sqlite-migration-nonha-%d", i), "/healthz")
		backend := harness.RequireResponseHeader(t, client, url, migrationBackendHeader)
		require.Equal(t, migrationLegacyBackend, backend)
		upstream := harness.RequireResponseHeader(t, client, url, harness.StickyUpstreamHeader)
		require.Equal(t, legacyHost, harness.HostIDForUpstream(cfg, upstream))
	}
	haURL := harness.RouterSessionURL(eps.RouterHTTP, haVersion, "sqlite-migration-phase0-ha", "/healthz")
	requireHAPoolBackend(t, client, haURL)

	harness.Step(t, "phase 1: create HA-version sessions under sqlite on %s", legacyHost)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	model := config.PrimaryModelID(cfg)
	adminKey := harness.TestenvAdminAPIKey
	for i := 0; i < 3; i++ {
		req := harness.ChatCompletionRequest{
			Model: model,
			Messages: []harness.ChatMessage{
				{Role: "user", Content: fmt.Sprintf("citest sqlite migration chat %d", i)},
			},
			MaxTokens: 32,
		}
		resp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, adminKey, req)
		harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)
	}
	snap := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	require.NotEmpty(t, snap.EscrowID)
	require.Greater(t, snap.LatestNonce, uint64(0), "expected session diffs after chat")

	storeDir := harness.VersionStoreDir(stack, legacyHost, haVersion)
	inv := harness.InventorySQLiteVersionStore(t, storeDir)
	require.Contains(t, inv.EscrowIDs, snap.EscrowID)
	harness.Step(t, "phase 1 inventory: %d sqlite escrow(s) under %s", len(inv.EscrowIDs), storeDir)

	harness.Step(t, "phase 2: declare the stack HA and start %s; sqlite → Devshard-Ha must 503", secondHost)
	harness.PatchRouterHADeployment(t, stack.ComposePath, true)
	stack.RecreateServices(t, "versiond-router")
	stack.StartService(t, secondHost)
	// Force-recreate remaps Docker host ports; refresh probes before waiting.
	eps = stack.Endpoints(t, cfg)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/healthz", 2*time.Minute, "router after expand", stack)

	haHealth := eps.RouterHTTP + "/" + haVersion + "/healthz"
	status := harness.WaitGETStatus(t, client, haHealth, 2*time.Minute, "HA path under sqlite", http.StatusServiceUnavailable)
	require.Equal(t, http.StatusServiceUnavailable, status)

	// Confirm router still HA-pools the version and NON_HA stays pinned.
	// (Probe via GET that may be 503; HAProxy still emits routing headers.)
	requireHAPoolBackend(t, client, haHealth)
	nonHAURL := harness.RouterSessionURL(eps.RouterHTTP, nonHAVersion, "sqlite-migration-phase2-nonha", "/healthz")
	require.Equal(t, migrationLegacyBackend, harness.RequireResponseHeader(t, client, nonHAURL, migrationBackendHeader))

	harness.Step(t, "phase 3: DEVSHARD_STORAGE_MODE=postgres → boot migrate")
	harness.PatchVersiondStorageMode(t, stack.ComposePath, "postgres")
	stack.RecreateServices(t, legacyHost, secondHost)
	harness.WaitGETOK(t, client, haHealth, 5*time.Minute, "devshardd health after postgres migrate", stack)

	harness.RequireSQLiteQuarantined(t, storeDir)
	harness.RequirePGBoundMarker(t, storeDir)
	harness.RequirePostgresMatchesInventory(t, stack, cfg, inv)

	harness.Step(t, "phase 4: multi-host HA serves with Devshard-Ha + postgres")
	// Clear gateway limiter / in-flight state left from Phase 2 HA 503s.
	harness.RestartService(t, stack, "devshardctl")
	// Stop/start remaps Docker host ports; refresh before probing.
	eps = stack.Endpoints(t, cfg)
	harness.WaitGETOK(t, client, eps.GatewayHTTP+"/v1/status", 3*time.Minute, "gateway after restart", stack)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	okReq := harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest postgres ha migration chat"},
		},
		MaxTokens: 32,
	}
	okResp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, adminKey, okReq)
	harness.RequireMockOpenAIContent(t, okResp.Choices[0].Message.Content)

	// Refresh router DNS after versiond recreate so both HA upstreams are live.
	stack.RequireServicesRunning(t, legacyHost, secondHost, "versiond-router")
	stack.RecreateServices(t, "versiond-router")
	eps = stack.Endpoints(t, cfg)
	haHealth = eps.RouterHTTP + "/" + haVersion + "/healthz"
	haURL = harness.RouterSessionURL(eps.RouterHTTP, haVersion, "sqlite-migration-phase0-ha", "/healthz")
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/healthz", 2*time.Minute, "router after HA refresh", stack)
	harness.WaitGETOK(t, client, haHealth, 2*time.Minute, "devshardd health via refreshed router", stack)

	_, upstreamA, _, upstreamB := harness.FindDistinctStickySessions(t, client, eps.RouterHTTP, haVersion)
	require.NotEqual(t, upstreamA, upstreamB)
	requireHAPoolBackend(t, client, haURL)

	// Migrated escrow remains readable via gateway after HA is healthy.
	after := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	require.Equal(t, snap.EscrowID, after.EscrowID)
	require.GreaterOrEqual(t, after.LatestNonce, snap.LatestNonce)

	for i := 0; i < 8; i++ {
		url := harness.RouterSessionURL(eps.RouterHTTP, nonHAVersion, fmt.Sprintf("sqlite-migration-final-nonha-%d", i), "/healthz")
		require.Equal(t, migrationLegacyBackend, harness.RequireResponseHeader(t, client, url, migrationBackendHeader))
		upstream := harness.RequireResponseHeader(t, client, url, harness.StickyUpstreamHeader)
		require.Equal(t, legacyHost, harness.HostIDForUpstream(cfg, upstream))
	}
}

func requireHAPoolBackend(t *testing.T, client *http.Client, url string) {
	t.Helper()
	backend := harness.RequireResponseHeader(t, client, url, migrationBackendHeader)
	require.True(t, strings.HasPrefix(backend, migrationHAPrefix),
		"X-Versiond-Backend = %q, want a per-version pool", backend)
}
