package loadtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// validate keeps legacy settings from silently disappearing on the new engine.
func (g Gateway) validate() error {
	switch g.Implementation {
	case "", "devshardctl":
		if g.Image != "" || g.Protocol != "" || g.EngineLoserGrace != "" {
			return fmt.Errorf("gateway image, protocol and engine_loser_grace require implementation: gateway")
		}
	case "gateway":
		if g.Image == "" || g.Protocol == "" {
			return fmt.Errorf("standalone gateway requires image and protocol")
		}
		if g.Redundancy.SecondaryWaitAfterWinner != "" {
			return fmt.Errorf("standalone gateway uses engine_loser_grace, not legacy redundancy")
		}
		for _, value := range g.ParticipantThrottle.settings() {
			if value != "" {
				return fmt.Errorf("standalone gateway does not support legacy participant_throttle settings")
			}
		}
		if g.EscrowRotation.Enabled {
			return fmt.Errorf("standalone gateway rotation scenarios are not supported yet")
		}
		if g.EngineLoserGrace != "" {
			grace, err := time.ParseDuration(g.EngineLoserGrace)
			if err != nil || grace < 30*time.Second || grace > 24*time.Hour {
				return fmt.Errorf("gateway.engine_loser_grace must be between 30s and 24h (at least the default inter-chunk stall)")
			}
		}
	default:
		return fmt.Errorf("gateway.implementation must be devshardctl or gateway")
	}
	return nil
}

// inferenceEntries accepts the legacy nonce map and the standalone gateway's
// [{nonce,status,record}] shape, preserving the record fields the judges read.
type inferenceEntries map[string]DebugInference

func (entries *inferenceEntries) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return fmt.Errorf("empty inference response")
	}
	if data[0] != '[' {
		var legacy map[string]DebugInference
		if err := json.Unmarshal(data, &legacy); err != nil {
			return err
		}
		*entries = legacy
		return nil
	}
	var rows []struct {
		Nonce  uint64          `json:"nonce"`
		Status string          `json:"status"`
		Record json.RawMessage `json:"record"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return err
	}
	result := make(inferenceEntries, len(rows))
	for _, row := range rows {
		// record.status is the numeric protocol enum; status beside it is its name.
		var record map[string]json.RawMessage
		if err := json.Unmarshal(row.Record, &record); err != nil {
			return err
		}
		delete(record, "status")
		body, err := json.Marshal(record)
		if err != nil {
			return err
		}
		var value DebugInference
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		value.Status = row.Status
		key := strconv.FormatUint(row.Nonce, 10)
		if _, exists := result[key]; exists {
			return fmt.Errorf("duplicate inference nonce %s", key)
		}
		result[key] = value
	}
	*entries = result
	return nil
}

// recordGatewayImage fails before starting a stack if the requested image is
// missing and records its content ID, since a local tag can be moved later.
func recordGatewayImage(ctx context.Context, gateway Gateway, outputDir string) error {
	body, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", `{"id":{{json .Id}},"architecture":{{json .Architecture}},"entrypoint":{{json .Config.Entrypoint}}}`, gateway.Image).Output()
	if err != nil {
		return fmt.Errorf("standalone gateway image %s is not available locally: %w", gateway.Image, err)
	}
	var record map[string]any
	if err := json.Unmarshal(body, &record); err != nil {
		return err
	}
	record["image"] = gateway.Image
	record["protocol"] = gateway.Protocol
	record["implementation"] = "gateway"
	body, err = json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, "gateway-image.json"), append(body, '\n'), 0o644)
}

func writeStandaloneGatewayState(ctx context.Context, gatewayURL, apiKey, outputDir string) error {
	var state map[string]any
	if err := getJSON(ctx, gatewayURL+"/v1/state", apiKey, &state); err != nil {
		return err
	}
	state["legacy_state_sizes_unavailable"] = "standalone gateway does not expose /v1/debug/state-sizes"
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, "gateway-state.json"), append(body, '\n'), 0o644)
}

// Precedence is CLI > build manifest > scenario. Copy the manifest so a run
// never mutates a caller's reusable build definition.
func resolveGatewayImage(s Scenario, build *Build, image string) (Scenario, *Build, error) {
	if s.Gateway.Implementation != "gateway" {
		if image != "" {
			return s, build, fmt.Errorf("-gateway-image requires gateway.implementation: gateway; use -build for the legacy gateway")
		}
		return s, build, nil
	}
	if build != nil {
		copied := *build
		build = &copied
		s.Gateway.Image = build.Images.Gateway
	}
	if image != "" {
		s.Gateway.Image = image
	}
	if build != nil {
		build.Images.Gateway = s.Gateway.Image
	}
	return s, build, s.Gateway.validate()
}

// Keep the source YAML and its comments, but record the effective image so
// saved-run reports and rechecks describe what actually ran.
func saveRunScenario(source, destination string, effective Scenario) error {
	body, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if effective.Gateway.Implementation == "gateway" {
		var document yaml.Node
		if err := yaml.Unmarshal(body, &document); err != nil {
			return err
		}
		root := document.Content[0]
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value != "gateway" {
				continue
			}
			gateway := root.Content[i+1]
			found := false
			for j := 0; j+1 < len(gateway.Content); j += 2 {
				if gateway.Content[j].Value == "image" {
					gateway.Content[j+1].Value = effective.Gateway.Image
					found = true
					break
				}
			}
			if !found {
				gateway.Content = append(gateway.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "image"}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: effective.Gateway.Image})
			}
		}
		body, err = yaml.Marshal(&document)
		if err != nil {
			return err
		}
	}
	return os.WriteFile(destination, body, 0o644)
}
