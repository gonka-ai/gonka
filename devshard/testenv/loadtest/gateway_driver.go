package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"devshard/testenv/config"
)

// What the harness reads differently per gateway driver; see README.md, "Driving either gateway".
const (
	gatewayBurnMessage       = "nonce burned for nobody"
	gatewayEscrowGoneMessage = "escrow gone from chain, taken out of service"
)

// driverOnlyInvariant is an invariant only one driver exposes the inputs for.
type driverOnlyInvariant struct {
	driver config.GatewayDriver
	reason string
}

var driverOnlyInvariants = map[string]driverOnlyInvariant{
	"quarantines-match-faults": {
		driver: config.GatewayDriverDevshardctl,
		reason: "the gateway lists participant keys without quarantine state on /v1/admin/devshards/{id}/participants and exports no devshard_gateway_participant_quarantine_transitions_total; its ejections show as devshard_gateway_host_ejected",
	},
}

// resolvedDriver returns the driver a run used; empty means devshardctl.
func resolvedDriver(driver config.GatewayDriver) config.GatewayDriver {
	if driver == "" {
		return config.GatewayDriverDevshardctl
	}
	return driver
}

func gatewayDriverName(driver config.GatewayDriver) string {
	return string(resolvedDriver(driver))
}

// invariantNotApplicable returns the NOT_JUDGED check the report shows in place of an invariant the run's driver cannot feed.
func invariantNotApplicable(invariant string, driver config.GatewayDriver) (AssertionResult, bool) {
	only, found := driverOnlyInvariants[invariant]
	if !found || resolvedDriver(driver) == only.driver {
		return AssertionResult{}, false
	}
	check := invariantCheck(invariant+".applicable", "", true, "gateway="+string(only.driver), "gateway="+gatewayDriverName(driver), "not applicable to "+gatewayDriverName(driver)+": "+only.reason)
	check.Status = statusNotJudged
	return check, true
}

// gatewayInferenceEntry is one record of the gateway's nonce-ordered debug inference list; its record carries a numeric status.
type gatewayInferenceEntry struct {
	Nonce  uint64 `json:"nonce"`
	Status string `json:"status"`
	Record struct {
		ExecutorSlot uint32 `json:"executor_slot"`
		Model        string `json:"model"`
		ActualCost   uint64 `json:"actual_cost"`
		ReservedCost uint64 `json:"reserved_cost"`
		StartedAt    int64  `json:"started_at"`
	} `json:"record"`
}

// decodeInferenceRecords reads devshardctl's nonce-keyed object and the gateway's nonce-carrying list alike.
func decodeInferenceRecords(raw json.RawMessage) (map[string]DebugInference, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return map[string]DebugInference{}, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var entries []gatewayInferenceEntry
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, fmt.Errorf("decode inference list: %w", err)
		}
		inferences := make(map[string]DebugInference, len(entries))
		for _, entry := range entries {
			inferences[strconv.FormatUint(entry.Nonce, 10)] = DebugInference{
				Status:       entry.Status,
				ExecutorSlot: entry.Record.ExecutorSlot,
				Model:        entry.Record.Model,
				ActualCost:   entry.Record.ActualCost,
				ReservedCost: entry.Record.ReservedCost,
				StartedAt:    entry.Record.StartedAt,
			}
		}
		return inferences, nil
	}
	var inferences map[string]DebugInference
	if err := json.Unmarshal(raw, &inferences); err != nil {
		return nil, fmt.Errorf("decode inference map: %w", err)
	}
	return inferences, nil
}

// fetchPendingView returns an escrow runtime's nonce and queued transaction count. The gateway queues no
// transactions behind a runtime, so it reports zero and reads the nonce from /v1/state.
func fetchPendingView(ctx context.Context, runtimeURL, apiKey string, driver config.GatewayDriver) (uint64, int, error) {
	if driver.IsGateway() {
		var state struct {
			Nonce uint64 `json:"nonce"`
		}
		err := getJSON(ctx, runtimeURL+"/v1/state", apiKey, &state)
		return state.Nonce, 0, err
	}
	var pending struct {
		Nonce   uint64            `json:"nonce"`
		Pending []json.RawMessage `json:"pending"`
	}
	err := getJSON(ctx, runtimeURL+"/v1/debug/pending", apiKey, &pending)
	return pending.Nonce, len(pending.Pending), err
}

// gatewayEscrowGone returns the escrow of a gateway gatewayEscrowGoneMessage line.
func gatewayEscrowGone(line string) (string, bool) {
	if !strings.Contains(line, gatewayEscrowGoneMessage) {
		return "", false
	}
	fields := logLineFields(line)
	if fields["msg"] != gatewayEscrowGoneMessage || fields["escrow"] == "" {
		return "", false
	}
	return fields["escrow"], true
}

// applyCmdGatewayScenarioSettings maps the scenario onto the gateway's admin overrides; a setting it cannot carry is logged, never fatal.
func applyCmdGatewayScenarioSettings(ctx context.Context, gatewayURL, apiKey string, scenario Scenario, rotationModels []string) error {
	overrides := map[string]any{}
	if value := scenario.Gateway.Redundancy.SecondaryWaitAfterWinner; value != "" {
		overrides["engine_loser_grace_ms"] = scenario.SecondaryWaitAfterWinner().Milliseconds()
	}
	for _, name := range slices.Sorted(maps.Keys(scenario.Gateway.ParticipantThrottle.settings())) {
		if scenario.Gateway.ParticipantThrottle.settings()[name] != "" {
			log.Printf("loadtest: gateway.participant_throttle.%s skipped: the gateway has no quarantine duration override", name)
		}
	}
	rotation := scenario.Gateway.EscrowRotation
	overrides["rotation_enabled"] = rotation.Enabled
	if rotation.Enabled {
		models, err := rotationModelSettings(rotation, rotationModels)
		if err != nil {
			return err
		}
		encodedModels, err := json.Marshal(models)
		if err != nil {
			return fmt.Errorf("marshal gateway rotation models: %w", err)
		}
		overrides["rotation_settlement_enabled"] = rotation.SettlementEnabled
		overrides["rotation_pre_poc_blocks"] = rotation.PrePoCBlocks
		overrides["rotation_models_json"] = string(encodedModels)
	}
	body, err := json.Marshal(overrides)
	if err != nil {
		return fmt.Errorf("marshal gateway overrides: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, gatewayURL+"/v1/admin/settings", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("create gateway overrides request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		log.Printf("loadtest: gateway overrides not applied: %v", err)
		return nil
	}
	defer func() { _ = response.Body.Close() }()
	reply, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	if response.StatusCode != http.StatusOK {
		log.Printf("loadtest: gateway overrides not applied: %s: %s", response.Status, strings.TrimSpace(string(reply)))
		return nil
	}
	log.Printf("loadtest: gateway overrides applied: %s", body)
	return nil
}
