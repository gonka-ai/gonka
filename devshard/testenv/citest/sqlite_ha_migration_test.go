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
)

// TestSQLiteToPostgresHAMigration walks v4 §1.7 / v5-deploy-test-plan.md Phases 0–4:
// single-host SQLite → multi-host Devshard-Ha 503 → postgres migrate → HA serve.
//
// NON_HA pin uses the running VersionName (a real child). A fictional v1 makes
// HAProxy L7-check /v1/healthz and mark the backend NOSRV. The pin is set at
// boot (same VersionName as TestLegacyVersionPinnedToSingleHost) and cleared in
// phase 2 together with GONKA_HA. The gateway is stopped across phases 2–3 so
// router recreates cannot persist a participant quarantine that survives restart.
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
	require.NotEmpty(t, haVersion)

	harness.Step(t, "phase 0: pin running version %q to %s (real child, not fictional v1)", haVersion, legacyHost)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/healthz", 5*time.Minute, "versiond-router healthz", stack)
	harness.WaitGETOK(t, client, eps.GatewayHTTP+"/v1/status", 5*time.Minute, "gateway status", stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+haVersion+"/healthz", 5*time.Minute, "pinned child /healthz", stack)
	requirePinnedToLegacy(t, client, cfg, eps.RouterHTTP, haVersion, legacyHost, 8, "sqlite-migration-pin")

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

	harness.Step(t, "phase 2: unpin %q, declare HA, start %s; sqlite → Devshard-Ha must 503", haVersion, secondHost)
	// Heartbeats during a router recreate persist a 30m participant quarantine
	// (`participant_limit_loaded_from_db`). Restarting the gateway does not clear it.
	stack.StopService(t, "devshardctl")
	harness.PatchComposeEnvKey(t, stack.ComposePath, "VERSIOND_NON_HA_VERSIONS", `""`)
	harness.PatchRouterHADeployment(t, stack.ComposePath, true)
	// Bring the second host back before recreating the router so catalog
	// admission (min-ready 2) is not waiting on a dead DNS name.
	stack.StartService(t, secondHost)
	stack.RecreateServices(t, "versiond-router")
	eps.RouterHTTP = stack.RouterHTTP(t)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/healthz", 2*time.Minute, "router after expand", stack)

	haHealth := eps.RouterHTTP + "/" + haVersion + "/healthz"
	// Catalog NOSRV is also HTTP 503 but has no routing headers. Wait until the
	// version is admitted and the child rejects Devshard-Ha under sqlite.
	status := harness.WaitRoutedStatus(t, client, haHealth, 2*time.Minute, "HA path under sqlite", http.StatusServiceUnavailable)
	require.Equal(t, http.StatusServiceUnavailable, status)
	requireHAPoolBackend(t, client, haHealth)

	harness.Step(t, "phase 3: DEVSHARD_STORAGE_MODE=postgres → boot migrate")
	harness.PatchVersiondStorageMode(t, stack.ComposePath, "postgres")
	stack.RecreateServices(t, legacyHost, secondHost)
	harness.WaitGETOK(t, client, haHealth, 5*time.Minute, "devshardd health after postgres migrate", stack)

	harness.RequireSQLiteQuarantined(t, storeDir)
	harness.RequirePGBoundMarker(t, storeDir)
	harness.RequirePostgresMatchesInventory(t, stack, cfg, inv)

	harness.Step(t, "phase 4: multi-host HA serves with Devshard-Ha + postgres")
	require.True(t, harness.AssertEventually(t, 2*time.Minute, time.Second, func() bool {
		return harness.TryVersiondReady(stack, legacyHost, haVersion) == nil &&
			harness.TryVersiondReady(stack, secondHost, haVersion) == nil
	}), "both %s and %s must pass /readyz?version=%s before HA fan-out", legacyHost, secondHost, haVersion)
	// Refresh router DNS after versiond recreate while the gateway is still down.
	stack.RequireServicesRunning(t, legacyHost, secondHost, "versiond-router")
	stack.RecreateServices(t, "versiond-router")
	eps.RouterHTTP = stack.RouterHTTP(t)
	haHealth = eps.RouterHTTP + "/" + haVersion + "/healthz"
	haURL := harness.RouterSessionURL(eps.RouterHTTP, haVersion, "sqlite-migration-phase4-ha", "/healthz")
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/healthz", 2*time.Minute, "router after HA refresh", stack)
	harness.WaitGETOK(t, client, haHealth, 2*time.Minute, "devshardd health via refreshed router", stack)

	stack.StartService(t, "devshardctl")
	eps = stack.Endpoints(t, cfg)
	harness.WaitGETOK(t, client, eps.GatewayHTTP+"/v1/status", 3*time.Minute, "gateway after start", stack)
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

	_, upstreamA, _, upstreamB := harness.WaitDistinctStickySessions(t, client, eps.RouterHTTP, haVersion, 2*time.Minute)
	require.NotEqual(t, upstreamA, upstreamB)
	requireHAPoolBackend(t, client, haURL)

	// Migrated escrow remains readable via gateway after HA is healthy.
	after := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	require.Equal(t, snap.EscrowID, after.EscrowID)
	require.GreaterOrEqual(t, after.LatestNonce, snap.LatestNonce)
}

func requirePinnedToLegacy(t *testing.T, client *http.Client, cfg *config.File, routerHTTP, version, legacyHost string, n int, prefix string) {
	t.Helper()
	for i := 0; i < n; i++ {
		url := harness.RouterSessionURL(routerHTTP, version, fmt.Sprintf("%s-%d", prefix, i), "/healthz")
		backend := harness.RequireResponseHeader(t, client, url, migrationBackendHeader)
		require.Equal(t, migrationLegacyBackend, backend)
		upstream := harness.RequireResponseHeader(t, client, url, harness.StickyUpstreamHeader)
		require.Equal(t, legacyHost, harness.HostIDForUpstream(cfg, upstream))
	}
}

func requireHAPoolBackend(t *testing.T, client *http.Client, url string) {
	t.Helper()
	backend := harness.RequireResponseHeader(t, client, url, migrationBackendHeader)
	ok := backend == "versiond_ha_pool" ||
		strings.HasPrefix(backend, "versiond_pool_") ||
		strings.HasPrefix(backend, "versiond_dynamic_")
	require.True(t, ok, "X-Versiond-Backend = %q, want an HA/catalog pool (not versiond_legacy)", backend)
}
