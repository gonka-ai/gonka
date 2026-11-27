package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	gatewayBurnMessage             = "nonce burned for nobody"
	gatewayEscrowGoneMessage       = "escrow gone from chain, taken out of service"
	noTimeAnchor             int64 = 0
	gatewayService                 = "gateway"
	gatewayExecutable              = "gateway"
	probeProcessOnly               = ""
	composeAssignedIP              = ""
)

// gatewayInferenceEntry is one record of the gateway's nonce-ordered debug inference list; its record carries a numeric status.
type gatewayInferenceEntry struct {
	Nonce  uint64 `json:"nonce"`
	Status string `json:"status"`
	Record struct {
		ExecutorSlot uint32 `json:"executor_slot"`
		Model        string `json:"model"`
		ActualCost   uint64 `json:"actual_cost"`
		ReservedCost uint64 `json:"reserved_cost"`
		StartedAtMs  int64  `json:"started_at_time_ms"`
	} `json:"record"`
}

// decodeInferenceRecords reads the gateway's nonce-carrying inference list.
func decodeInferenceRecords(raw json.RawMessage) (map[string]DebugInference, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return map[string]DebugInference{}, nil
	}
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
			StartedAt:    startedAtSeconds(entry.Record.StartedAtMs),
		}
	}
	return inferences, nil
}

// startedAtSeconds reads a record's block-time anchor; a record the gateway could not anchor stays at noTimeAnchor.
func startedAtSeconds(startedAtMs int64) int64 {
	if startedAtMs <= noTimeAnchor {
		return noTimeAnchor
	}
	return time.UnixMilli(startedAtMs).Unix()
}

func fetchEscrowNonce(ctx context.Context, escrowURL, apiKey string) (uint64, error) {
	var state struct {
		Nonce uint64 `json:"nonce"`
	}
	err := getJSON(ctx, escrowURL+"/v1/state", apiKey, &state)
	return state.Nonce, err
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

// applyGatewayScenarioSettings maps the scenario onto the gateway's admin overrides; a setting it cannot carry is logged, never fatal.
func applyGatewayScenarioSettings(ctx context.Context, gatewayURL, apiKey string, scenario Scenario, rotationModels []string) error {
	overrides := map[string]any{}
	if value := scenario.Gateway.Redundancy.SecondaryWaitAfterWinner; value != "" {
		overrides["engine_loser_grace_ms"] = scenario.SecondaryWaitAfterWinner().Milliseconds()
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
