package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// GatewaySessionSnapshot captures gateway-visible session state for restart citest.
type GatewaySessionSnapshot struct {
	EscrowID       string
	SessionNonce   uint64 // devshardctl session nonce (/v1/status)
	LatestNonce    uint64 // state machine latest nonce (/v1/debug/state)
	Balance        uint64
	Phase          string
	LiveInferences int
}

type gatewayStatusBody struct {
	EscrowID string `json:"escrow_id"`
	Nonce    uint64 `json:"nonce"`
	Phase    string `json:"phase"`
	Balance  uint64 `json:"balance"`
}

type gatewayDebugStateBody struct {
	Nonce          uint64 `json:"nonce"`
	Balance        uint64 `json:"balance"`
	Phase          string `json:"phase"`
	LiveInferences int    `json:"live_inferences"`
}

// GetGatewaySessionSnapshot reads /v1/status and /v1/debug/state from devshardctl.
func GetGatewaySessionSnapshot(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string) GatewaySessionSnapshot {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}

	var status gatewayStatusBody
	require.NoError(t, getGatewayJSON(t, client, gatewayURL+"/v1/status", adminAPIKey, &status))

	var debug gatewayDebugStateBody
	require.NoError(t, getGatewayJSON(t, client, gatewayURL+"/v1/debug/state", adminAPIKey, &debug))

	escrowID := status.EscrowID
	if escrowID == "" {
		escrowID = GetGatewayEscrowID(t, client, gatewayURL)
	}

	return GatewaySessionSnapshot{
		EscrowID:       escrowID,
		SessionNonce:   status.Nonce,
		LatestNonce:    debug.Nonce,
		Balance:        status.Balance,
		Phase:          status.Phase,
		LiveInferences: debug.LiveInferences,
	}
}

func getGatewayJSON(t *testing.T, client *http.Client, url, adminAPIKey string, dest any) error {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if adminAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+adminAPIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: %d %s", url, resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, dest)
}

// GatewayHostStats is the per-slot ledger from GET /v1/state.
type GatewayHostStats struct {
	Missed               uint32 `json:"missed"`
	Invalid              uint32 `json:"invalid"`
	Cost                 uint64 `json:"cost"`
	RequiredValidations  uint32 `json:"required_validations"`
	CompletedValidations uint32 `json:"completed_validations"`
}

type gatewayStateBody struct {
	Session struct {
		Balance     uint64 `json:"balance"`
		LatestNonce uint64 `json:"latest_nonce"`
	} `json:"session"`
	HostStats map[string]GatewayHostStats `json:"host_stats"`
}

// GatewayLedgerSnapshot is session balance, producing nonce, and per-slot
// host stats from one GET /v1/state.
type GatewayLedgerSnapshot struct {
	Balance     uint64
	LatestNonce uint64
	HostStats   map[string]GatewayHostStats
}

// GetGatewayLedgerSnapshot reads /v1/state host_stats, session balance, and
// latest_nonce from a single response.
func GetGatewayLedgerSnapshot(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string) GatewayLedgerSnapshot {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	var body gatewayStateBody
	require.NoError(t, getGatewayJSON(t, client, gatewayURL+"/v1/state", adminAPIKey, &body))
	return GatewayLedgerSnapshot{
		Balance:     body.Session.Balance,
		LatestNonce: body.Session.LatestNonce,
		HostStats:   body.HostStats,
	}
}

// GetGatewayFeePerNonce reads frozen session FeePerNonce from GET /v1/status.
func GetGatewayFeePerNonce(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string) uint64 {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	var status struct {
		Config struct {
			FeePerNonce uint64 `json:"fee_per_nonce"`
		} `json:"config"`
	}
	require.NoError(t, getGatewayJSON(t, client, gatewayURL+"/v1/status", adminAPIKey, &status))
	return status.Config.FeePerNonce
}

// GatewayDebugInference is one record from GET /v1/debug/inferences.
type GatewayDebugInference struct {
	Status       string `json:"status"`
	ExecutorSlot uint32 `json:"executor_slot"`
	ReservedCost uint64 `json:"reserved_cost"`
	ActualCost   uint64 `json:"actual_cost"`
	VotesValid   uint32 `json:"votes_valid"`
	VotesInvalid uint32 `json:"votes_invalid"`
}

type gatewayInferencesBody struct {
	Inferences map[string]GatewayDebugInference `json:"inferences"`
}

// GetGatewayDebugInferences reads GET /v1/debug/inferences.
func GetGatewayDebugInferences(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string) map[string]GatewayDebugInference {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	var body gatewayInferencesBody
	require.NoError(t, getGatewayJSON(t, client, gatewayURL+"/v1/debug/inferences", adminAPIKey, &body))
	if body.Inferences == nil {
		return map[string]GatewayDebugInference{}
	}
	return body.Inferences
}

// PostAdminDeactivateDevshard POSTs /v1/admin/devshards/{id}/deactivate.
// Retires the runtime from memory (same registry drop settle uses).
func PostAdminDeactivateDevshard(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, escrowID string) {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	url := strings.TrimRight(gatewayURL, "/") + "/v1/admin/devshards/" + escrowID + "/deactivate"
	req, err := http.NewRequest(http.MethodPost, url, nil)
	require.NoError(t, err)
	if adminAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+adminAPIKey)
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST deactivate: %s", string(body))
}

// RestartService stops and starts a compose service without removing volumes.
func RestartService(t *testing.T, stack *Stack, service string) {
	t.Helper()
	stack.StopService(t, service)
	stack.StartService(t, service)
}

// RestartServices restarts compose services in order (stop all, then start all).
func RestartServices(t *testing.T, stack *Stack, services ...string) {
	t.Helper()
	for _, svc := range services {
		stack.StopService(t, svc)
	}
	for _, svc := range services {
		stack.StartService(t, svc)
	}
}

// VersiondHostIDs returns compose service ids for configured versiond hosts.
func VersiondHostIDs(cfg *config.File) []string {
	if cfg == nil {
		return nil
	}
	ids := make([]string, len(cfg.Hosts))
	for i, h := range cfg.Hosts {
		ids[i] = h.ID
	}
	return ids
}

// WaitVersiondSessionHealthy polls router + gateway until versiond/devshardd recover after restart.
func WaitVersiondSessionHealthy(t *testing.T, stack *Stack, cfg *config.File, eps Endpoints, escrowID string) {
	t.Helper()
	if escrowID == "" {
		t.Fatal("escrow id is required")
	}
	client := HTTPClient()
	WaitGETOK(t, client, eps.RouterHTTP+"/healthz", 5*time.Minute, "versiond-router healthz", stack)
	// Session routes have no /healthz; mempool resolves the escrow via lazy load / RecoverSessions.
	sessionReady := RouterSessionURL(eps.RouterHTTP, cfg.Versiond.VersionName, escrowID, "/mempool")
	WaitGETOK(t, client, sessionReady, 5*time.Minute, "devshardd session mempool via router", stack)
	WaitRouterCatalogAdmitted(t, stack, 5*time.Minute)
	WaitGETOK(t, client, eps.GatewayHTTP+"/v1/status", 3*time.Minute, "gateway /v1/status", stack)
}

// RequireGatewaySessionAdvanced asserts a successful chat advanced session nonce/state.
func RequireGatewaySessionAdvanced(t *testing.T, before, after GatewaySessionSnapshot) {
	t.Helper()
	require.Equal(t, before.EscrowID, after.EscrowID, "escrow id changed")
	require.Equal(t, before.Phase, after.Phase, "phase changed after chat")
	require.Greater(t, after.SessionNonce, before.SessionNonce,
		"session nonce regressed: before=%d after=%d", before.SessionNonce, after.SessionNonce)
	require.GreaterOrEqual(t, after.LatestNonce, before.LatestNonce,
		"latest nonce regressed: before=%d after=%d", before.LatestNonce, after.LatestNonce)
	require.LessOrEqual(t, after.Balance, before.Balance,
		"balance increased after chat (before=%d after=%d)", before.Balance, after.Balance)
}

// RequireGatewaySessionStable asserts gateway session identity survived a
// versiond restart. Height-sync heartbeats keep running on the gateway while a
// replica drains (drain 5s > DefaultHeartbeatInterval 3s), so the producer
// cursor and durable tip may advance; they must not go backwards. Balance may
// fall if those heartbeat nonces are charged.
func RequireGatewaySessionStable(t *testing.T, before, after GatewaySessionSnapshot) {
	t.Helper()
	require.Equal(t, before.EscrowID, after.EscrowID, "escrow id changed across restart")
	require.Equal(t, before.Phase, after.Phase, "phase changed across restart")
	require.GreaterOrEqual(t, after.SessionNonce, before.SessionNonce,
		"session nonce regressed across restart: before=%d after=%d", before.SessionNonce, after.SessionNonce)
	require.GreaterOrEqual(t, after.LatestNonce, before.LatestNonce,
		"latest nonce regressed across restart: before=%d after=%d", before.LatestNonce, after.LatestNonce)
	require.LessOrEqual(t, after.Balance, before.Balance,
		"balance increased across restart: before=%d after=%d", before.Balance, after.Balance)
	if after.SessionNonce > before.SessionNonce || after.LatestNonce > before.LatestNonce {
		t.Logf("citest: session advanced during restart wait (height-sync heartbeats): nonce %d→%d latest %d→%d",
			before.SessionNonce, after.SessionNonce, before.LatestNonce, after.LatestNonce)
	}
}
