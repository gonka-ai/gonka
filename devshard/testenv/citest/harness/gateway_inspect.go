package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	devshardRoutePrefix          = "/devshard/"
	devshardStateRoute           = "/v1/state"
	devshardDebugStateRoute      = "/v1/debug/state"
	devshardDebugInferencesRoute = "/v1/debug/inferences"
	devshardDebugPendingRoute    = "/v1/debug/pending"
	gatewayStatusRoute           = "/v1/status"
	InferenceStatusPending       = "pending"
	InferenceStatusStarted       = "started"
	InferenceStatusTimedOut      = "timed_out"
	noAdminAPIKey                = ""
	noEscrowID                   = ""
	authorizationHeader          = "Authorization"
	bearerPrefix                 = "Bearer "
)

// GatewaySessionSnapshot captures gateway-visible session state for restart citest.
type GatewaySessionSnapshot struct {
	EscrowID       string
	SessionNonce   uint64
	LatestNonce    uint64
	Balance        uint64
	Phase          string
	LiveInferences int
	OpenHolds      int
}

// GatewayHostStats is one slot's ledger row from GET /devshard/{id}/v1/debug/state.
type GatewayHostStats struct {
	Missed               uint32 `json:"Missed"`
	Invalid              uint32 `json:"Invalid"`
	Cost                 uint64 `json:"Cost"`
	RequiredValidations  uint32 `json:"RequiredValidations"`
	CompletedValidations uint32 `json:"CompletedValidations"`
	Validated            uint32 `json:"Validated"`
	Finished             uint32 `json:"Finished"`
}

// GatewayLedgerSnapshot is session balance, producing nonce, and per-slot host stats from one debug state read.
type GatewayLedgerSnapshot struct {
	Balance     uint64
	LatestNonce uint64
	HostStats   map[string]GatewayHostStats
}

// GatewayDebugInference is one record from GET /devshard/{id}/v1/debug/inferences.
type GatewayDebugInference struct {
	Status       string
	ExecutorSlot uint32
	ReservedCost uint64
	ActualCost   uint64
	VotesValid   uint32
	VotesInvalid uint32
}

type gatewayEscrowStateBody struct {
	EscrowID       string `json:"escrow_id"`
	Phase          string `json:"phase"`
	Nonce          uint64 `json:"nonce"`
	LatestNonce    uint64 `json:"latest_nonce"`
	Balance        uint64 `json:"balance"`
	LiveInferences int    `json:"live_inferences"`
}

type gatewayEscrowDebugStateBody struct {
	gatewayEscrowStateBody
	Config struct {
		FeePerNonce uint64 `json:"FeePerNonce"`
	} `json:"config"`
	HostStats []struct {
		Slot uint32 `json:"slot"`
		GatewayHostStats
	} `json:"host_stats"`
}

type gatewayInferenceEntry struct {
	Nonce  uint64 `json:"nonce"`
	Status string `json:"status"`
	Record struct {
		ExecutorSlot uint32 `json:"executor_slot"`
		ReservedCost uint64 `json:"reserved_cost"`
		ActualCost   uint64 `json:"actual_cost"`
		VotesValid   uint32 `json:"votes_valid"`
		VotesInvalid uint32 `json:"votes_invalid"`
	} `json:"record"`
}

type gatewayInferencesBody struct {
	EscrowID   string                  `json:"escrow_id"`
	Inferences []gatewayInferenceEntry `json:"inferences"`
}

type gatewayStatusEscrow struct {
	EscrowID string `json:"id"`
	Model    string `json:"model"`
	Nonce    uint64 `json:"nonce"`
	Phase    string `json:"phase"`
	Balance  uint64 `json:"balance"`
}

type gatewayStatusBody struct {
	Devshards []gatewayStatusEscrow `json:"devshards"`
}

// GatewayEscrowURL is the escrow-scoped gateway route for escrowID, e.g. /devshard/{id}/v1/state.
func GatewayEscrowURL(gatewayURL, escrowID, route string) string {
	return strings.TrimRight(gatewayURL, "/") + devshardRoutePrefix + url.PathEscape(escrowID) + route
}

// GetGatewaySessionSnapshot reads /devshard/{id}/v1/state and /devshard/{id}/v1/debug/pending.
func GetGatewaySessionSnapshot(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, escrowID string) GatewaySessionSnapshot {
	t.Helper()
	require.NotEmpty(t, escrowID, "escrow id is required")
	var state gatewayEscrowStateBody
	require.NoError(t, getGatewayJSONWithKey(client, GatewayEscrowURL(gatewayURL, escrowID, devshardStateRoute), adminAPIKey, &state))
	require.Equal(t, escrowID, state.EscrowID, "escrow state answered for another escrow")
	require.NotEmpty(t, state.Phase, "escrow state has no phase")

	var pending gatewayInferencesBody
	require.NoError(t, getGatewayJSONWithKey(client, GatewayEscrowURL(gatewayURL, escrowID, devshardDebugPendingRoute), adminAPIKey, &pending))
	require.Equal(t, escrowID, pending.EscrowID, "pending view answered for another escrow")

	return GatewaySessionSnapshot{
		EscrowID:       escrowID,
		SessionNonce:   state.Nonce,
		LatestNonce:    state.LatestNonce,
		Balance:        state.Balance,
		Phase:          state.Phase,
		LiveInferences: state.LiveInferences,
		OpenHolds:      openHolds(pending.Inferences),
	}
}

// openHolds counts pending and started records: challenged ones are already charged, and their refund must not gate settle.
func openHolds(entries []gatewayInferenceEntry) int {
	holds := 0
	for _, entry := range entries {
		if entry.Status == InferenceStatusPending || entry.Status == InferenceStatusStarted {
			holds++
		}
	}
	return holds
}

func getGatewayDebugState(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, escrowID string) gatewayEscrowDebugStateBody {
	t.Helper()
	require.NotEmpty(t, escrowID, "escrow id is required")
	var body gatewayEscrowDebugStateBody
	require.NoError(t, getGatewayJSONWithKey(client, GatewayEscrowURL(gatewayURL, escrowID, devshardDebugStateRoute), adminAPIKey, &body))
	require.Equal(t, escrowID, body.EscrowID, "escrow debug state answered for another escrow")
	return body
}

// GetGatewayLedgerSnapshot reads host stats, balance and latest nonce from one /devshard/{id}/v1/debug/state.
func GetGatewayLedgerSnapshot(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, escrowID string) GatewayLedgerSnapshot {
	t.Helper()
	body := getGatewayDebugState(t, client, gatewayURL, adminAPIKey, escrowID)
	require.NotEmpty(t, body.HostStats, "escrow debug state has no host stats")
	stats := make(map[string]GatewayHostStats, len(body.HostStats))
	for _, entry := range body.HostStats {
		stats[strconv.FormatUint(uint64(entry.Slot), 10)] = entry.GatewayHostStats
	}
	return GatewayLedgerSnapshot{
		Balance:     body.Balance,
		LatestNonce: body.LatestNonce,
		HostStats:   stats,
	}
}

// GetGatewayFeePerNonce reads the session's frozen FeePerNonce from /devshard/{id}/v1/debug/state.
func GetGatewayFeePerNonce(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, escrowID string) uint64 {
	t.Helper()
	feePerNonce := getGatewayDebugState(t, client, gatewayURL, adminAPIKey, escrowID).Config.FeePerNonce
	require.NotZero(t, feePerNonce, "escrow debug state has no fee per nonce")
	return feePerNonce
}

// GetGatewayDebugInferences reads /devshard/{id}/v1/debug/inferences keyed by nonce.
func GetGatewayDebugInferences(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, escrowID string) map[string]GatewayDebugInference {
	t.Helper()
	require.NotEmpty(t, escrowID, "escrow id is required")
	var body gatewayInferencesBody
	require.NoError(t, getGatewayJSONWithKey(client, GatewayEscrowURL(gatewayURL, escrowID, devshardDebugInferencesRoute), adminAPIKey, &body))
	require.Equal(t, escrowID, body.EscrowID, "inference listing answered for another escrow")
	records := make(map[string]GatewayDebugInference, len(body.Inferences))
	for _, entry := range body.Inferences {
		records[strconv.FormatUint(entry.Nonce, 10)] = GatewayDebugInference{
			Status:       entry.Status,
			ExecutorSlot: entry.Record.ExecutorSlot,
			ReservedCost: entry.Record.ReservedCost,
			ActualCost:   entry.Record.ActualCost,
			VotesValid:   entry.Record.VotesValid,
			VotesInvalid: entry.Record.VotesInvalid,
		}
	}
	return records
}

// GetGatewayStatusEscrows reads the routable escrows from GET /v1/status.
func GetGatewayStatusEscrows(client *http.Client, gatewayURL string) ([]gatewayStatusEscrow, error) {
	if client == nil {
		client = HTTPClient()
	}
	var status gatewayStatusBody
	if err := GetJSON(client, strings.TrimRight(gatewayURL, "/")+gatewayStatusRoute, &status); err != nil {
		return nil, err
	}
	return status.Devshards, nil
}

// GetGatewayEscrowID returns the first routable escrow on GET /v1/status.
func GetGatewayEscrowID(t *testing.T, client *http.Client, gatewayURL string) string {
	t.Helper()
	escrows, err := GetGatewayStatusEscrows(client, gatewayURL)
	require.NoError(t, err)
	require.NotEmpty(t, escrows, "gateway /v1/status lists no routable escrow")
	require.NotEmpty(t, escrows[0].EscrowID, "gateway /v1/status escrow has no id")
	return escrows[0].EscrowID
}

func statusListsEscrow(escrows []gatewayStatusEscrow, escrowID string) bool {
	for _, escrow := range escrows {
		if escrow.EscrowID == escrowID {
			return true
		}
	}
	return false
}

// GetGatewayAdminJSON GETs an admin-gated gateway route with the testenv admin key and decodes the answer.
func GetGatewayAdminJSON(client *http.Client, requestURL string, dest any) error {
	return getGatewayJSONWithKey(client, requestURL, TestenvAdminAPIKey, dest)
}

func getGatewayJSONWithKey(client *http.Client, requestURL, adminAPIKey string, dest any) error {
	if client == nil {
		client = HTTPClient()
	}
	request, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return fmt.Errorf("build GET %s: %w", requestURL, err)
	}
	setAdminBearer(request, adminAPIKey)
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("GET %s: %w", requestURL, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("read GET %s: %w", requestURL, err)
	}
	if response.StatusCode >= http.StatusMultipleChoices {
		return &GatewayStatusError{Method: http.MethodGet, URL: requestURL, StatusCode: response.StatusCode, Body: string(body)}
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return fmt.Errorf("decode GET %s: %w", requestURL, err)
	}
	return nil
}

// GatewayStatusError is a gateway answer outside 2xx, kept typed so a caller can branch on its status.
type GatewayStatusError struct {
	Method     string
	URL        string
	StatusCode int
	Body       string
}

func (e *GatewayStatusError) Error() string {
	return fmt.Sprintf("%s %s: %d %s", e.Method, e.URL, e.StatusCode, e.Body)
}

// setAdminBearer authorises request with key; noAdminAPIKey leaves it unauthenticated.
func setAdminBearer(request *http.Request, key string) {
	if key != noAdminAPIKey {
		request.Header.Set(authorizationHeader, bearerPrefix+key)
	}
}
