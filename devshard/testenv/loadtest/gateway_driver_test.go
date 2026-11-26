package loadtest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/testenv/config"
)

// Test flow:
// 1. Decode a gateway debug inference list whose records carry a numeric status.
// 2. Decode a devshardctl nonce-keyed map.
// 3. Check both yield the same records keyed by nonce, with the named status.
func TestDecodeInferenceRecordsReadsBothDrivers(t *testing.T) {
	gateway, err := decodeInferenceRecords(json.RawMessage(`[{"nonce":3,"status":"started","record":{"status":1,"executor_slot":2,"model":"m","reserved_cost":9,"started_at":100}}]`))
	require.NoError(t, err)
	devshardctl, err := decodeInferenceRecords(json.RawMessage(`{"3":{"status":"started","executor_slot":2,"model":"m","reserved_cost":9,"started_at":100}}`))
	require.NoError(t, err)

	want := map[string]DebugInference{"3": {Status: "started", ExecutorSlot: 2, Model: "m", ReservedCost: 9, StartedAt: 100}}
	require.Equal(t, want, gateway)
	require.Equal(t, want, devshardctl)
}

// Test flow:
// 1. Parse a gateway burn line with a nonce, one without a nonce, and a devshardctl ghost_probe_skipped line.
// 2. Check the nonce-carrying lines become burns with escrow, host and reason, and the nonce-less one is skipped.
func TestParseGatewayBurnReadsBothDrivers(t *testing.T) {
	burn, ok, err := parseGatewayBurn(`devshardctl-1  | {"time":"2026-10-19T20:00:00Z","level":"WARN","msg":"nonce burned for nobody","escrow":"7","nonce":12,"host":"abcd1234","reason":"participant_ejected_no_send"}`)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, gatewayBurn{EscrowID: "7", Nonce: "12", Host: "abcd1234", Reason: "participant_ejected_no_send", At: burn.At}, burn)
	require.False(t, burn.At.IsZero())

	_, ok, err = parseGatewayBurn(`devshardctl-1  | {"msg":"nonce burned for nobody","escrow":"7","host":"abcd1234","reason":"request_abandoned_before_dispatch"}`)
	require.NoError(t, err)
	require.False(t, ok)

	burn, ok, err = parseGatewayBurn(`devshardctl-1  | stage=ghost_probe_skipped escrow=7 nonce=13 host=abcd1234 reason=participant_cut_off_no_send`)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "13", burn.Nonce)
}

// Test flow:
// 1. Read the gateway's escrow-gone line and an unrelated line.
// 2. Check only the first yields its escrow.
func TestGatewayEscrowGoneReadsTheEscrow(t *testing.T) {
	id, ok := gatewayEscrowGone(`devshardctl-1  | {"msg":"escrow gone from chain, taken out of service","escrow":"42"}`)
	require.True(t, ok)
	require.Equal(t, "42", id)
	_, ok = gatewayEscrowGone(`devshardctl-1  | {"msg":"escrow parked at its settlement deadline","escrow":"42"}`)
	require.False(t, ok)
}

// Test flow:
// 1. Evaluate invariants on empty inputs for a gateway run and for a devshardctl run.
// 2. Check the gateway run reports quarantines-match-faults as NOT_JUDGED with the reason, and the devshardctl run judges it.
func TestEvaluateInvariantsReportsDriverOnlyInvariantsAsNotApplicable(t *testing.T) {
	gatewayChecks, _ := evaluateInvariants(InvariantInputs{Gateway: config.GatewayDriverGateway, DidNotCompress: -1})
	devshardctlChecks, _ := evaluateInvariants(InvariantInputs{DidNotCompress: -1})

	applicable, found := findCheck(gatewayChecks, "invariants.quarantines-match-faults.applicable")
	require.True(t, found)
	require.Equal(t, statusNotJudged, applicable.Status)
	require.Contains(t, applicable.Details, "not applicable to gateway")
	for _, check := range gatewayChecks {
		require.NotContains(t, check.Name, "quarantines-match-faults.durations")
	}
	_, found = findCheck(devshardctlChecks, "invariants.quarantines-match-faults.applicable")
	require.False(t, found)
}

// Test flow:
// 1. Serve /v1/state and /v1/debug/pending from a fake runtime.
// 2. Read the pending view as the gateway and as devshardctl.
// 3. Check the gateway reads the nonce from /v1/state with no queued transactions, devshardctl from /v1/debug/pending.
func TestFetchPendingViewReadsEachDriversRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/state":
			_, _ = w.Write([]byte(`{"escrow_id":"7","nonce":21,"pending":3}`))
		case "/v1/debug/pending":
			_, _ = w.Write([]byte(`{"nonce":20,"pending":[{},{}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	nonce, queued, err := fetchPendingView(context.Background(), server.URL, "", config.GatewayDriverGateway)
	require.NoError(t, err)
	require.Equal(t, uint64(21), nonce)
	require.Zero(t, queued)
	nonce, queued, err = fetchPendingView(context.Background(), server.URL, "", config.GatewayDriverDevshardctl)
	require.NoError(t, err)
	require.Equal(t, uint64(20), nonce)
	require.Equal(t, 2, queued)
}

// Test flow:
// 1. Apply a rotation scenario with a per-model settlement override to a fake gateway admin endpoint.
// 2. Check rotation_models_json carries settlement_enabled only on the overridden model.
func TestApplyCmdGatewayScenarioSettingsCarriesPerModelSettlement(t *testing.T) {
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

	require.NoError(t, applyCmdGatewayScenarioSettings(context.Background(), server.URL, "", scenario, []string{"model-a", "model-b"}))

	var models []map[string]any
	require.NoError(t, json.Unmarshal([]byte(overrides["rotation_models_json"].(string)), &models))
	require.Len(t, models, 2)
	require.NotContains(t, models[0], "settlement_enabled")
	require.Equal(t, false, models[1]["settlement_enabled"])
	require.Equal(t, true, overrides["rotation_settlement_enabled"])
}

func findCheck(checks []AssertionResult, name string) (AssertionResult, bool) {
	for _, check := range checks {
		if check.Name == name {
			return check, true
		}
	}
	return AssertionResult{}, false
}
