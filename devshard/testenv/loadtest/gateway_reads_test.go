package loadtest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"devshard/types"

	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Encode two gateway debug inference entries from types.InferenceRecord: one anchored at a block time, one the gateway could not anchor.
// 2. Decode the list.
// 3. Check the records are keyed by nonce with the named status, the anchored one started at its block time in seconds and the other at noTimeAnchor.
func TestDecodeInferenceRecordsReadsTheGatewayList(t *testing.T) {
	startedAt := time.Unix(1_700_000_100, 0)
	entries := []map[string]any{
		{"nonce": 3, "status": "started", "record": types.InferenceRecord{Status: types.StatusStarted, ExecutorSlot: 2, Model: "m", ReservedCost: 9, StartedAtHeight: 40, StartedAtTimeMs: startedAt.UnixMilli() + 250}},
		{"nonce": 4, "status": "pending", "record": types.InferenceRecord{Status: types.StatusPending, ExecutorSlot: 1, Model: "m", ReservedCost: 5}},
	}
	raw, err := json.Marshal(entries)
	require.NoError(t, err)

	gateway, err := decodeInferenceRecords(raw)
	require.NoError(t, err)

	want := map[string]DebugInference{
		"3": {Status: "started", ExecutorSlot: 2, Model: "m", ReservedCost: 9, StartedAt: startedAt.Unix()},
		"4": {Status: "pending", ExecutorSlot: 1, Model: "m", ReservedCost: 5, StartedAt: noTimeAnchor},
	}
	require.Equal(t, want, gateway)
}

// Test flow:
// 1. Parse a gateway burn line with a nonce and one without a nonce.
// 2. Check the nonce-carrying line becomes a burn with escrow, host and reason, and the nonce-less one is skipped.
func TestParseGatewayBurnReadsTheNonceCarryingLine(t *testing.T) {
	burn, ok, err := parseGatewayBurn(`gateway-1  | {"time":"2026-10-19T20:00:00Z","level":"WARN","msg":"nonce burned for nobody","escrow":"7","nonce":12,"host":"abcd1234","reason":"participant_ejected_no_send"}`)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, gatewayBurn{EscrowID: "7", Nonce: "12", Host: "abcd1234", Reason: "participant_ejected_no_send", At: burn.At}, burn)
	require.False(t, burn.At.IsZero())

	_, ok, err = parseGatewayBurn(`gateway-1  | {"msg":"nonce burned for nobody","escrow":"7","host":"abcd1234","reason":"request_abandoned_before_dispatch"}`)
	require.NoError(t, err)
	require.False(t, ok)
}

// Test flow:
// 1. Read the gateway's escrow-gone line and an unrelated line.
// 2. Check only the first yields its escrow.
func TestGatewayEscrowGoneReadsTheEscrow(t *testing.T) {
	id, ok := gatewayEscrowGone(`gateway-1  | {"msg":"escrow gone from chain, taken out of service","escrow":"42"}`)
	require.True(t, ok)
	require.Equal(t, "42", id)
	_, ok = gatewayEscrowGone(`gateway-1  | {"msg":"escrow parked at its settlement deadline","escrow":"42"}`)
	require.False(t, ok)
}

// Test flow:
// 1. Serve /v1/state from a fake escrow route.
// 2. Read the escrow's nonce.
// 3. Check the nonce comes from /v1/state.
func TestFetchEscrowNonceReadsTheStateRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/state" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"escrow_id":"7","nonce":21,"pending":3}`))
	}))
	defer server.Close()

	nonce, err := fetchEscrowNonce(context.Background(), server.URL, "")
	require.NoError(t, err)
	require.Equal(t, uint64(21), nonce)
}

// Test flow:
// 1. Apply a rotation scenario with a per-model settlement override to a fake gateway admin endpoint.
// 2. Check rotation_models_json carries settlement_enabled only on the overridden model.
func TestApplyGatewayScenarioSettingsCarriesPerModelSettlement(t *testing.T) {
	var overrides map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&overrides))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	scenario := mixScenario()
	disabled := false
	scenario.Gateway.EscrowRotation = GatewayEscrowRotation{Enabled: true, SettlementEnabled: true, PrePoCBlocks: 10, TempCount: 1, TargetCount: 1, Amount: 5, PrivateKeyEnv: "KEY",
		Models: []GatewayRotationModel{{Model: "model-b", SettlementEnabled: &disabled}}}

	require.NoError(t, applyGatewayScenarioSettings(context.Background(), server.URL, "", scenario, []string{"model-a", "model-b"}))

	var models []map[string]any
	require.NoError(t, json.Unmarshal([]byte(overrides["rotation_models_json"].(string)), &models))
	require.Len(t, models, 2)
	require.NotContains(t, models[0], "settlement_enabled")
	require.Equal(t, false, models[1]["settlement_enabled"])
	require.Equal(t, true, overrides["rotation_settlement_enabled"])
	require.ErrorContains(t, applyGatewayScenarioSettings(context.Background(), server.URL, "", scenario, []string{"model-a"}), "model-b is not a rotated model")
}
