// Package loadtest defines the runnable testenv load-scenario contract.
package loadtest

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"devshard/testenv/mockopenai"

	"gopkg.in/yaml.v3"
)

type Scenario struct {
	SchemaVersion string      `yaml:"schema_version"`
	Scenario      string      `yaml:"scenario"`
	Seed          int64       `yaml:"seed"`
	Topology      Topology    `yaml:"topology"`
	Protocol      Protocol    `yaml:"protocol"`
	Workload      Workload    `yaml:"workload"`
	Gateway       Gateway     `yaml:"gateway"`
	Assertions    Assertions  `yaml:"assertions"`
	DrainTimeout  string      `yaml:"drain_timeout"`
	Diagnostics   Diagnostics `yaml:"diagnostics"`
}

// Protocol contains optional chain timeout overrides for short deterministic
// lifecycle tests. Values are written to the mock-chain seed in seconds.
type Protocol struct {
	RefusalTimeout   string `yaml:"refusal_timeout"`
	ExecutionTimeout string `yaml:"execution_timeout"`
}

type Diagnostics struct {
	HeapProfiles             []ProfileSnapshot  `yaml:"heap_profiles"`
	GoroutineProfiles        []ProfileSnapshot  `yaml:"goroutine_profiles"`
	Logs                     LogDiagnostics     `yaml:"logs"`
	CPUProfiles              []CPUProfileWindow `yaml:"cpu_profiles"`
	DisablePeriodicSnapshots bool               `yaml:"disable_periodic_snapshots"`
	ContentionProfiles       bool               `yaml:"contention_profiles"`
	// Export copies the hosts' databases and every mock ML node's request log into the output
	// directory before the stack stops, for checks that compare what the ML produced with what the
	// hosts recorded (export.go).
	Export bool `yaml:"export"`
}

type ProfileSnapshot struct {
	StartAfter string   `yaml:"start_after"`
	Targets    []string `yaml:"targets"`
}

type LogDiagnostics struct {
	// Tail is a positive line count per container, or "all" for complete logs.
	Tail string `yaml:"tail"`
}

func (d Diagnostics) LogTail() string {
	if d.Logs.Tail == "" {
		return "5000"
	}
	return d.Logs.Tail
}

type CPUProfileWindow struct {
	StartAfter string `yaml:"start_after"`
	Duration   string `yaml:"duration"`
}

type Gateway struct {
	Redundancy          GatewayRedundancy          `yaml:"redundancy"`
	EscrowRotation      GatewayEscrowRotation      `yaml:"escrow_rotation"`
	ParticipantThrottle GatewayParticipantThrottle `yaml:"participant_throttle"`
}

type GatewayParticipantThrottle struct {
	TransportFailureQuarantine string `yaml:"transport_failure_quarantine"`
	HTTPQuarantine             string `yaml:"http_quarantine"`
	EmptyStreamQuarantine      string `yaml:"empty_stream_quarantine"`
}

type GatewayRedundancy struct {
	SecondaryWaitAfterWinner string `yaml:"secondary_wait_after_winner"`
}

// GatewayEscrowRotation is scenario-level shorthand. The runner expands it
// into one Gateway rotation model per configured or replayed model.
type GatewayEscrowRotation struct {
	Enabled           bool   `yaml:"enabled"`
	SettlementEnabled bool   `yaml:"settlement_enabled"`
	PrePoCBlocks      int64  `yaml:"pre_poc_blocks"`
	TempCount         int    `yaml:"temp_count"`
	TargetCount       int    `yaml:"target_count"`
	Amount            uint64 `yaml:"amount"`
	PrivateKeyEnv     string `yaml:"private_key_env"`
}

const DefaultSecondaryWaitAfterWinner = 10 * time.Minute

type Topology struct {
	VersiondMode string         `yaml:"versiond_mode"`
	Storage      string         `yaml:"storage"`
	Participants int            `yaml:"participants"`
	Chain        ChainTopology  `yaml:"chain"`
	MockML       MockMLTopology `yaml:"mock_ml"`
}

// HostFault freezes a whole versiond container, including its devshardd child.
type HostFault struct {
	Host       string `yaml:"host" json:"host"`
	Mode       string `yaml:"mode" json:"mode"`
	StartAfter string `yaml:"start_after" json:"start_after"`
	Duration   string `yaml:"duration" json:"duration"`
}

// ChainTopology is the deterministic test-only chain state needed to keep a
// load scenario from terminating because of fixture defaults.
type ChainTopology struct {
	EscrowAmount uint64 `yaml:"escrow_amount"`
	MaxNonce     uint32 `yaml:"max_nonce"`
	// ValidationThreshold is the replay similarity bar the chain hands the hosts for every model.
	// Unset keeps the testenv default, 0.50, which no logprob mismatch can fail: one position adds
	// less than 0.5 to the distance, so similarity with matching tokens stays above 0.5.
	ValidationThreshold *ChainDecimal `yaml:"validation_threshold"`
	// Escrows is how many escrows of the request model the gateway holds during the load, as an operator
	// that funds several does; it spreads the requests over them. The first is the configured one, the
	// rest are created through the gateway admin API before the load. 0 and 1 keep the one.
	Escrows int `yaml:"escrows"`
}

// ChainDecimal is a chain decimal, value × 10^exponent; mainnet's 0.958 is value 958, exponent -3.
type ChainDecimal struct {
	Value    int64 `yaml:"value"`
	Exponent int32 `yaml:"exponent"`
}

type MockMLTopology struct {
	Allocator string       `yaml:"allocator"`
	Nodes     []MockMLNode `yaml:"nodes"`
	// Rules are fault rules every node of the scenario runs after its profile's own, so a rule fires
	// whichever node the pool hands the request it selects.
	Rules []mockopenai.FaultRule `yaml:"rules"`
}

type MockMLNode struct {
	Name    string `yaml:"name"`
	Profile string `yaml:"profile"`
	// Host binds the node to one versiond host in mock-dapi; empty keeps it
	// in the shared pool.
	Host string `yaml:"host"`
	// MaxConcurrent caps the AcquireMLNode locks mock-dapi hands out on the
	// node at once, as the dapi broker caps a node; 0 is unlimited.
	MaxConcurrent int `yaml:"max_concurrent"`
}

type Workload struct {
	HostFaults     []HostFault    `yaml:"host_faults"`
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
		// RequireFullNode makes a promises run not valid when no ML node was ever full (none queued a
		// request for a worker or refused one at capacity): the run did not load its nodes the way it
		// was meant to.
		RequireFullNode bool `yaml:"require_full_node"`
	} `yaml:"mock_ml"`
	Accounting AccountingAssertions `yaml:"accounting"`
	// Promises holds the exported run to the system properties of citest/harness (CheckLoadtestRun)
	// and writes report.json and report.md; a violation fails the run.
	Promises bool `yaml:"promises"`
}

type AccountingAssertions struct {
	TargetHost         string `yaml:"target_host"`
	MinProtocolMisses  uint64 `yaml:"min_protocol_misses"`
	MinTimeoutsApplied uint64 `yaml:"min_timeouts_applied"`
	RequireCrossChecks bool   `yaml:"require_cross_checks"`
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
	// Chaos makes the node a dishonest executor: each execution gets one answer shape drawn by these
	// weights (mockopenai chaos actions, e.g. wrong_answer, odd_usage). A validator's replay is
	// always answered honestly, so a shape other than ok is one the protocol should catch.
	Chaos *ProfileChaos `yaml:"chaos"`
	// Oracle answers a validator's replay from the enforced tokens it carries, and reports usage as the
	// tokens emitted. ChunkTokens is how many tokens one streamed chunk carries. FirstContentMs and
	// ExecMs schedule a streamed execution from the request start; ReplayMs delays replays only.
	Oracle         bool `yaml:"oracle"`
	ChunkTokens    int  `yaml:"chunk_tokens"`
	FirstContentMs int  `yaml:"first_content_ms"`
	ExecMs         int  `yaml:"exec_ms"`
	ReplayMs       int  `yaml:"replay_ms"`
	// Rules inject a fault into the requests they select by phase, seed or prompt text; the generator
	// tags every prompt with "[load-request:<id>]".
	Rules []mockopenai.FaultRule `yaml:"rules"`
}

// ProfileChaos is the chaos part of an ML node profile.
type ProfileChaos struct {
	Seed    int64          `yaml:"seed"`
	Weights map[string]int `yaml:"weights"`
}

// rulesJSON is the mock's MOCK_OPENAI_RULES_JSON for a node of the profile: the profile's rules, then
// the ones the scenario gives every node; empty when there are none.
func (p Profile) rulesJSON(scenarioRules ...mockopenai.FaultRule) (string, error) {
	rules := append(slices.Clone(p.Rules), scenarioRules...)
	if len(rules) == 0 {
		return "", nil
	}
	if err := mockopenai.ValidateRules(rules); err != nil {
		return "", err
	}
	raw, err := json.Marshal(rules)
	return string(raw), err
}

// chaosJSON is the mock's MOCK_OPENAI_CHAOS_JSON for the profile, empty when it has no chaos.
func (p Profile) chaosJSON() (string, error) {
	if p.Chaos == nil {
		return "", nil
	}
	c := mockopenai.ChaosConfig{Enabled: true, Seed: p.Chaos.Seed, Weights: p.Chaos.Weights}
	if len(c.Weights) == 0 {
		return "", fmt.Errorf("chaos needs weights")
	}
	if err := c.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(c)
	return string(raw), err
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
	if _, err := profile.chaosJSON(); err != nil {
		return Profile{}, fmt.Errorf("profile %s chaos: %w", path, err)
	}
	if _, err := profile.rulesJSON(); err != nil {
		return Profile{}, fmt.Errorf("profile %s rules: %w", path, err)
	}
	if profile.ChunkTokens < 0 || profile.FirstContentMs < 0 || profile.ExecMs < 0 || profile.ReplayMs < 0 {
		return Profile{}, fmt.Errorf("profile %s chunk_tokens, first_content_ms, exec_ms and replay_ms must be non-negative", path)
	}
	if profile.ExecMs > 0 && profile.ExecMs < profile.FirstContentMs {
		return Profile{}, fmt.Errorf("profile %s exec_ms (the last chunk) must not come before first_content_ms", path)
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

func protocolTimeoutSeconds(value string) (int64, error) {
	duration, err := time.ParseDuration(value)
	if err != nil || duration < time.Second || duration%time.Second != 0 {
		return 0, fmt.Errorf("protocol timeout %q must be a whole number of seconds >= 1s", value)
	}
	return int64(duration / time.Second), nil
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
	// A similarity is at most 1, so a bar of 1 or more fails every honest replay.
	if t := s.Topology.Chain.ValidationThreshold; t != nil && (t.Value <= 0 || t.Exponent >= 0 || float64(t.Value) >= math.Pow10(int(-t.Exponent))) {
		return fmt.Errorf("topology.chain.validation_threshold must be above 0 and below 1")
	}
	if s.Topology.Chain.Escrows < 0 || s.Topology.Chain.Escrows > maxScenarioEscrows {
		return fmt.Errorf("topology.chain.escrows must be between 0 and %d", maxScenarioEscrows)
	}
	if err := mockopenai.ValidateRules(s.Topology.MockML.Rules); err != nil {
		return fmt.Errorf("topology.mock_ml.rules: %w", err)
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
		if node.MaxConcurrent < 0 {
			return fmt.Errorf("mock_ml node %q max_concurrent must be non-negative", node.Name)
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
	if err := validateHostFaults(s.Topology, s.Workload.HostFaults, duration); err != nil {
		return err
	}
	if err := s.validateTopologyBindings(); err != nil {
		return err
	}
	protocolTimeouts := map[string]string{
		"refusal_timeout":   s.Protocol.RefusalTimeout,
		"execution_timeout": s.Protocol.ExecutionTimeout,
	}
	for name, value := range protocolTimeouts {
		if value == "" {
			continue
		}
		timeout, parseErr := time.ParseDuration(value)
		if parseErr != nil || timeout < time.Second || timeout%time.Second != 0 {
			return fmt.Errorf("protocol.%s must be a whole number of seconds >= 1s", name)
		}
	}
	if (s.Protocol.RefusalTimeout == "") != (s.Protocol.ExecutionTimeout == "") {
		return fmt.Errorf("protocol.refusal_timeout and protocol.execution_timeout must be configured together")
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
	if tail := s.Diagnostics.LogTail(); tail != "all" {
		lines, err := strconv.Atoi(tail)
		if err != nil || lines <= 0 {
			return fmt.Errorf("diagnostics.logs.tail must be a positive integer or all")
		}
	}
	for kind, snapshots := range map[string][]ProfileSnapshot{
		"heap_profiles":      s.Diagnostics.HeapProfiles,
		"goroutine_profiles": s.Diagnostics.GoroutineProfiles,
	} {
		var previous time.Duration
		for i, snapshot := range snapshots {
			after, err := time.ParseDuration(snapshot.StartAfter)
			if err != nil || after < 0 || after >= duration || (i > 0 && after <= previous) {
				return fmt.Errorf("diagnostics.%s snapshots must have ordered, distinct non-negative start_after within workload", kind)
			}
			previous = after
			seen := map[string]bool{}
			for _, target := range snapshot.Targets {
				if seen[target] || (target != "gateway" && !validSnapshotHost(target, s.Topology.Participants)) {
					return fmt.Errorf("diagnostics.%s invalid or duplicate target %q", kind, target)
				}
				seen[target] = true
			}
		}
	}
	var previousEnd time.Duration
	if s.Diagnostics.ContentionProfiles && len(s.Diagnostics.CPUProfiles) == 0 {
		return fmt.Errorf("diagnostics.contention_profiles requires at least one cpu_profiles window")
	}
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
	if s.Assertions.Accounting.TargetHost != "" {
		validHost := false
		for index := 0; index <= s.Topology.Participants; index++ {
			if s.Assertions.Accounting.TargetHost == fmt.Sprintf("versiond-%d", index) {
				validHost = true
				break
			}
		}
		if !validHost {
			return fmt.Errorf("assertions.accounting.target_host must be a configured versiond host")
		}
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
	for name, value := range map[string]string{
		"transport_failure_quarantine": s.Gateway.ParticipantThrottle.TransportFailureQuarantine,
		"http_quarantine":              s.Gateway.ParticipantThrottle.HTTPQuarantine,
		"empty_stream_quarantine":      s.Gateway.ParticipantThrottle.EmptyStreamQuarantine,
	} {
		if value == "" {
			continue
		}
		duration, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("gateway.participant_throttle.%s: %w", name, err)
		}
		if duration < time.Millisecond {
			return fmt.Errorf("gateway.participant_throttle.%s must be at least 1ms", name)
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
	if s.Assertions.Promises && !s.Diagnostics.Export {
		return fmt.Errorf("assertions.promises requires diagnostics.export")
	}
	// The checks find the executor of every inference in compose.log; a cut log hides the early ones.
	if s.Assertions.Promises && s.Diagnostics.LogTail() != "all" {
		return fmt.Errorf("assertions.promises requires diagnostics.logs.tail: all")
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

func (s Scenario) validateTopologyBindings() error {
	for i, node := range s.Topology.MockML.Nodes {
		if node.Host == "" {
			continue
		}
		if s.Topology.Participants == 0 || !validTopologyHost(node.Host, s.Topology.Participants) {
			return fmt.Errorf("topology.mock_ml.nodes[%d].host must be a versiond host of an explicit topology.participants", i)
		}
	}
	return nil
}
