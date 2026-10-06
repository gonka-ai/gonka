// Package loadtest defines the runnable testenv load-scenario contract.
package loadtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Scenario struct {
	SchemaVersion string      `yaml:"schema_version"`
	Scenario      string      `yaml:"scenario"`
	Seed          int64       `yaml:"seed"`
	Topology      Topology    `yaml:"topology"`
	Workload      Workload    `yaml:"workload"`
	Gateway       Gateway     `yaml:"gateway"`
	Assertions    Assertions  `yaml:"assertions"`
	DrainTimeout  string      `yaml:"drain_timeout"`
	Diagnostics   Diagnostics `yaml:"diagnostics"`
}

type Diagnostics struct {
	CPUProfiles []CPUProfileWindow `yaml:"cpu_profiles"`
}
type CPUProfileWindow struct {
	StartAfter string `yaml:"start_after"`
	Duration   string `yaml:"duration"`
}

type Gateway struct {
	Redundancy     GatewayRedundancy     `yaml:"redundancy"`
	EscrowRotation GatewayEscrowRotation `yaml:"escrow_rotation"`
}

type GatewayRedundancy struct {
	SecondaryWaitAfterWinner string `yaml:"secondary_wait_after_winner"`
}

// GatewayEscrowRotation is scenario-level shorthand. The runner expands it
// into one Gateway rotation model per configured or replayed model.
type GatewayEscrowRotation struct {
	Enabled                bool   `yaml:"enabled"`
	SettlementEnabled      bool   `yaml:"settlement_enabled"`
	PrePoCBlocks           int64  `yaml:"pre_poc_blocks"`
	NonceDeactivationLimit uint64 `yaml:"nonce_deactivation_limit"`
	TempCount              int    `yaml:"temp_count"`
	TargetCount            int    `yaml:"target_count"`
	Amount                 uint64 `yaml:"amount"`
	PrivateKeyEnv          string `yaml:"private_key_env"`
}

const DefaultSecondaryWaitAfterWinner = 10 * time.Minute

type Topology struct {
	VersiondMode string         `yaml:"versiond_mode"`
	Storage      string         `yaml:"storage"`
	Participants int            `yaml:"participants"`
	Chain        ChainTopology  `yaml:"chain"`
	MockML       MockMLTopology `yaml:"mock_ml"`
}

// ChainTopology is the deterministic test-only chain state needed to keep a
// load scenario from terminating because of fixture defaults.
type ChainTopology struct {
	EscrowAmount uint64 `yaml:"escrow_amount"`
	MaxNonce     uint32 `yaml:"max_nonce"`
}

type MockMLTopology struct {
	Allocator string       `yaml:"allocator"`
	Nodes     []MockMLNode `yaml:"nodes"`
}

type MockMLNode struct {
	Name    string `yaml:"name"`
	Profile string `yaml:"profile"`
}

type Workload struct {
	ReportInterval string         `yaml:"report_interval"`
	MaxInFlight    int            `yaml:"max_in_flight"`
	Duration       string         `yaml:"duration"`
	Traffic        TrafficProfile `yaml:"traffic"`
	Request        Request        `yaml:"request"`
}

// TrafficProfile describes the rate of new requests over a workload's duration.
// An omitted type preserves the original closed-loop behavior.
type TrafficProfile struct {
	Type          string         `yaml:"type"`
	RPS           float64        `yaml:"rps"`
	FromRPS       float64        `yaml:"from_rps"`
	ToRPS         float64        `yaml:"to_rps"`
	MinRPS        float64        `yaml:"min_rps"`
	MaxRPS        float64        `yaml:"max_rps"`
	Period        string         `yaml:"period"`
	BaseRPS       float64        `yaml:"base_rps"`
	SpikeRPS      float64        `yaml:"spike_rps"`
	SpikeDuration string         `yaml:"spike_duration"`
	Interval      string         `yaml:"interval"`
	Stages        []TrafficStage `yaml:"stages"`
}

type TrafficStage struct {
	Duration string  `yaml:"duration"`
	RPS      float64 `yaml:"rps"`
}

type Request struct {
	Model     string    `yaml:"model"`
	Stream    bool      `yaml:"stream"`
	MaxTokens int       `yaml:"max_tokens"`
	Messages  []Message `yaml:"messages"`
}

type Message struct {
	Role    string `yaml:"role" json:"role"`
	Content string `yaml:"content" json:"content"`
}

type Assertions struct {
	Requests struct {
		HTTPStatus      int     `yaml:"http_status"`
		TerminalOutcome string  `yaml:"terminal_outcome"`
		ErrorRate       float64 `yaml:"error_rate"`
	} `yaml:"requests"`
	EscrowRotation struct {
		RequireNewEscrow bool `yaml:"require_new_escrow"`
	} `yaml:"escrow_rotation"`
	Devshard struct {
		NoOrphanedWork bool    `yaml:"no_orphaned_work"`
		MaxGhostRate   float64 `yaml:"max_ghost_rate"`
	} `yaml:"devshard"`
	MockML struct {
		RequireEachNodeUsed bool `yaml:"require_each_node_used"`
	} `yaml:"mock_ml"`
}

type Profile struct {
	SchemaVersion string  `yaml:"schema_version"`
	Profile       string  `yaml:"profile"`
	TTFT          string  `yaml:"ttft"`
	TokenInterval string  `yaml:"token_interval"`
	Workers       int     `yaml:"workers"`
	Queue         int     `yaml:"queue"`
	Hang          bool    `yaml:"hang"`
	FailureRate   float64 `yaml:"failure_rate"`
	HTTPStatus    int     `yaml:"http_status"`
	Failures      []any   `yaml:"failures"`
}

func LoadScenario(path string) (Scenario, error) {
	var scenario Scenario
	if err := loadYAML(path, &scenario); err != nil {
		return Scenario{}, err
	}
	if err := scenario.Validate(); err != nil {
		return Scenario{}, fmt.Errorf("scenario %s: %w", path, err)
	}
	return scenario, nil
}

func LoadProfile(dir, name string) (Profile, error) {
	path := filepath.Join(dir, name+".yaml")
	var profile Profile
	if err := loadYAML(path, &profile); err != nil {
		return Profile{}, err
	}
	if profile.SchemaVersion != "v1" || profile.Profile != name {
		return Profile{}, fmt.Errorf("profile %s must declare schema_version v1 and profile %q", path, name)
	}
	if _, err := time.ParseDuration(profile.TTFT); err != nil {
		return Profile{}, fmt.Errorf("profile %s ttft: %w", path, err)
	}
	if _, err := time.ParseDuration(profile.TokenInterval); err != nil {
		return Profile{}, fmt.Errorf("profile %s token_interval: %w", path, err)
	}
	if profile.Workers <= 0 || profile.Queue < 0 {
		return Profile{}, fmt.Errorf("profile %s workers must be positive and queue non-negative", path)
	}
	if profile.FailureRate < 0 || profile.FailureRate > 1 {
		return Profile{}, fmt.Errorf("profile %s failure_rate must be between 0 and 1", path)
	}
	if profile.HTTPStatus != 0 && (profile.HTTPStatus < 400 || profile.HTTPStatus > 599) {
		return Profile{}, fmt.Errorf("profile %s http_status must be between 400 and 599", path)
	}
	return profile, nil
}

func (s Scenario) Validate() error {
	if s.SchemaVersion != "v1" || strings.TrimSpace(s.Scenario) == "" {
		return fmt.Errorf("schema_version must be v1 and scenario must be set")
	}
	if s.Topology.VersiondMode != "multi" {
		return fmt.Errorf("only versiond_mode multi is supported")
	}
	if s.Topology.Storage != "per_participant" {
		return fmt.Errorf("only topology.storage per_participant is supported")
	}
	if s.Topology.Participants != 0 && s.Topology.Participants < 2 {
		return fmt.Errorf("topology.participants must be at least 2 when set")
	}
	if s.Topology.Chain.EscrowAmount == 0 || s.Topology.Chain.MaxNonce == 0 {
		return fmt.Errorf("topology.chain requires positive escrow_amount and max_nonce")
	}
	if s.Topology.MockML.Allocator != "round_robin" {
		return fmt.Errorf("mock DAPI scenarios require mock_ml allocator round_robin")
	}
	if len(s.Topology.MockML.Nodes) < 2 {
		return fmt.Errorf("at least two mock_ml nodes are required")
	}
	seen := make(map[string]struct{}, len(s.Topology.MockML.Nodes))
	for _, node := range s.Topology.MockML.Nodes {
		if node.Name == "" || node.Profile == "" {
			return fmt.Errorf("mock_ml nodes require name and profile")
		}
		if _, exists := seen[node.Name]; exists {
			return fmt.Errorf("duplicate mock_ml node %q", node.Name)
		}
		seen[node.Name] = struct{}{}
	}
	if s.Workload.MaxInFlight <= 0 {
		return fmt.Errorf("workload max_in_flight must be positive")
	}
	duration, err := time.ParseDuration(s.Workload.Duration)
	if err != nil {
		return fmt.Errorf("workload duration: %w", err)
	}
	if err := s.Workload.Traffic.Validate(duration); err != nil {
		return fmt.Errorf("workload traffic: %w", err)
	}
	if s.Workload.ReportInterval != "" {
		interval, err := time.ParseDuration(s.Workload.ReportInterval)
		if err != nil || interval <= 0 {
			return fmt.Errorf("workload.report_interval must be a positive duration")
		}
	}
	var previousEnd time.Duration
	for index, window := range s.Diagnostics.CPUProfiles {
		start, startErr := time.ParseDuration(window.StartAfter)
		length, lengthErr := time.ParseDuration(window.Duration)
		if startErr != nil || lengthErr != nil || start < 0 || length < time.Second || length%time.Second != 0 || start+length > duration {
			return fmt.Errorf("CPU profile window %d must have a non-negative start and whole-second positive duration within workload", index+1)
		}
		if index > 0 && start < previousEnd {
			return fmt.Errorf("CPU profile windows must be ordered and not overlap")
		}
		previousEnd = start + length
	}
	if s.Workload.Request.Stream {
		return fmt.Errorf("streaming requests are not supported by the initial runner")
	}
	if s.Workload.Request.Model == "" || len(s.Workload.Request.Messages) == 0 {
		return fmt.Errorf("request model and messages are required")
	}
	if s.Assertions.Requests.ErrorRate < 0 || s.Assertions.Requests.ErrorRate > 1 {
		return fmt.Errorf("assertions.requests.error_rate must be between 0 and 1")
	}
	if s.Assertions.Requests.HTTPStatus == 0 || s.Assertions.Requests.TerminalOutcome == "" {
		return fmt.Errorf("request assertions require http_status and terminal_outcome")
	}
	if s.Assertions.Devshard.MaxGhostRate < 0 || s.Assertions.Devshard.MaxGhostRate > 1 {
		return fmt.Errorf("devshard max_ghost_rate must be between 0 and 1")
	}
	if s.Gateway.Redundancy.SecondaryWaitAfterWinner != "" {
		duration, err := time.ParseDuration(s.Gateway.Redundancy.SecondaryWaitAfterWinner)
		if err != nil || duration <= 0 {
			if err != nil {
				return fmt.Errorf("gateway.redundancy.secondary_wait_after_winner: %w", err)
			}
			return fmt.Errorf("gateway.redundancy.secondary_wait_after_winner must be positive")
		}
	}
	rotation := s.Gateway.EscrowRotation
	if rotation.Enabled {
		if rotation.PrePoCBlocks <= 0 {
			return fmt.Errorf("gateway.escrow_rotation.pre_poc_blocks must be positive")
		}
		if rotation.TempCount <= 0 || rotation.TargetCount <= 0 {
			return fmt.Errorf("gateway.escrow_rotation temp_count and target_count must be positive")
		}
		if rotation.Amount == 0 {
			return fmt.Errorf("gateway.escrow_rotation.amount must be positive")
		}
		if rotation.NonceDeactivationLimit == 0 {
			return fmt.Errorf("gateway.escrow_rotation.nonce_deactivation_limit must be positive")
		}
		if strings.TrimSpace(rotation.PrivateKeyEnv) == "" {
			return fmt.Errorf("gateway.escrow_rotation.private_key_env must be set")
		}
	}
	if s.Assertions.EscrowRotation.RequireNewEscrow && !rotation.Enabled {
		return fmt.Errorf("assertions.escrow_rotation.require_new_escrow requires gateway.escrow_rotation.enabled")
	}
	if _, err := time.ParseDuration(s.DrainTimeout); err != nil {
		return fmt.Errorf("drain_timeout: %w", err)
	}
	return nil
}

func (s Scenario) Duration() time.Duration {
	d, _ := time.ParseDuration(s.Workload.Duration)
	return d
}

func (s Scenario) ReportInterval() time.Duration {
	if s.Workload.ReportInterval == "" {
		return time.Minute
	}
	d, _ := time.ParseDuration(s.Workload.ReportInterval)
	return d
}

func (s Scenario) SecondaryWaitAfterWinner() time.Duration {
	if s.Gateway.Redundancy.SecondaryWaitAfterWinner == "" {
		return DefaultSecondaryWaitAfterWinner
	}
	d, _ := time.ParseDuration(s.Gateway.Redundancy.SecondaryWaitAfterWinner)
	return d
}

func (s Scenario) DrainDuration() time.Duration {
	d, _ := time.ParseDuration(s.DrainTimeout)
	return d
}

func loadYAML(path string, out any) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(body, out); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
