// Package loadtest defines the runnable testenv load-scenario contract.
package loadtest

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"devshard/transport"

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
	Cases         []Case      `yaml:"cases"`
}

// Case names a scenario case that events and assertions refer to. A case with
// KnownBug set is expected to fail: a failure reports XFAIL, a pass XPASS.
type Case struct {
	ID       string `yaml:"id"`
	Title    string `yaml:"title"`
	KnownBug string `yaml:"known_bug"`
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
	StalledWinnerQuarantine    string `yaml:"stalled_winner_quarantine"`
}

func (t GatewayParticipantThrottle) settings() map[string]string {
	return map[string]string{
		"transport_failure_quarantine": t.TransportFailureQuarantine,
		"http_quarantine":              t.HTTPQuarantine,
		"empty_stream_quarantine":      t.EmptyStreamQuarantine,
		"stalled_winner_quarantine":    t.StalledWinnerQuarantine,
	}
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
	// Models overrides settlement per model; unset keeps the global flag.
	Models []GatewayRotationModel `yaml:"models"`
}

type GatewayRotationModel struct {
	Model             string `yaml:"model"`
	SettlementEnabled *bool  `yaml:"settlement_enabled"`
}

const DefaultSecondaryWaitAfterWinner = 10 * time.Minute

type Topology struct {
	VersiondMode string         `yaml:"versiond_mode"`
	Storage      string         `yaml:"storage"`
	Participants int            `yaml:"participants"`
	Chain        ChainTopology  `yaml:"chain"`
	MockML       MockMLTopology `yaml:"mock_ml"`
	Hosts        []TopologyHost `yaml:"hosts"`
}

// TopologyHost adds environment variables to one versiond container. Keys
// replace the compose defaults of the same name.
type TopologyHost struct {
	ID  string            `yaml:"id"`
	Env map[string]string `yaml:"env"`
	// RPCChatsPerMin sets DEVSHARD_RPC_MSGS_PER_MIN to this many Chats a
	// minute at the current transport.RPCWeightChat, so a budget case keeps biting if the weight changes.
	RPCChatsPerMin int `yaml:"rpc_chats_per_min"`
}

// ResolvedEnv is Env plus the DEVSHARD_RPC_MSGS_PER_MIN that RPCChatsPerMin implies.
func (h TopologyHost) ResolvedEnv() map[string]string {
	if h.RPCChatsPerMin <= 0 {
		return h.Env
	}
	env := make(map[string]string, len(h.Env)+1)
	for key, value := range h.Env {
		env[key] = value
	}
	env[rpcMsgsPerMinEnv] = strconv.Itoa(h.RPCChatsPerMin * transport.RPCWeightChat)
	return env
}

const rpcMsgsPerMinEnv = "DEVSHARD_RPC_MSGS_PER_MIN"

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
	// Host binds the node to one versiond host in mock-dapi; empty keeps it
	// in the shared pool.
	Host string `yaml:"host"`
}

type Workload struct {
	HostFaults     []HostFault    `yaml:"host_faults"`
	Events         []Event        `yaml:"events"`
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
	// PeakRPS is the rate of every stage marked peak (default 32).
	PeakRPS float64 `yaml:"peak_rps"`
}

type TrafficStage struct {
	Duration string  `yaml:"duration"`
	RPS      float64 `yaml:"rps"`
	Peak     bool    `yaml:"peak"`
}

type Request struct {
	Model       string      `yaml:"model"`
	Stream      bool        `yaml:"stream"`
	MaxTokens   int         `yaml:"max_tokens"`
	Temperature *float64    `yaml:"temperature"`
	Logprobs    bool        `yaml:"logprobs"`
	TopLogprobs int         `yaml:"top_logprobs"`
	Messages    []Message   `yaml:"messages,omitempty"`
	Timeout     string      `yaml:"timeout"`
	Mix         *RequestMix `yaml:"mix"`
}

// RequestMix draws per-request parameters from weighted choices. Each
// dimension left empty falls back to the fixed request fields.
type RequestMix struct {
	StreamRatio   *float64              `yaml:"stream_ratio"`
	LogprobsRatio *float64              `yaml:"logprobs_ratio"`
	TopLogprobs   *int                  `yaml:"top_logprobs"`
	MaxTokens     []Weighted[int]       `yaml:"max_tokens"`
	Temperature   []Weighted[float64]   `yaml:"temperature"`
	Prompts       []Weighted[[]Message] `yaml:"prompts"`
	Models        []ModelRequest        `yaml:"models"`
}

// ModelRequest overrides common request fields after selecting a model by weight.
// A nested mix varies parameters only within this model; nested models are forbidden.
type ModelRequest struct {
	Value       string      `yaml:"value"`
	Weight      float64     `yaml:"weight"`
	Messages    []Message   `yaml:"messages,omitempty"`
	MaxTokens   *int        `yaml:"max_tokens"`
	Temperature *float64    `yaml:"temperature"`
	Stream      *bool       `yaml:"stream"`
	Logprobs    *bool       `yaml:"logprobs"`
	TopLogprobs *int        `yaml:"top_logprobs"`
	Mix         *RequestMix `yaml:"mix"`
}

type Weighted[T any] struct {
	Value  T       `yaml:"value"`
	Weight float64 `yaml:"weight"`
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
		// DroppedRate bounds the requests the generator never sent; unset, it is ErrorRate.
		DroppedRate *float64 `yaml:"dropped_rate"`
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
	Accounting AccountingAssertions `yaml:"accounting"`
	Invariants InvariantAssertions  `yaml:"invariants"`
}

// InvariantAssertions enables the end-of-run invariants. Empty fields
// keep the defaults from invariantSettings. Cases ties a run-level invariant,
// such as "no-runtime-for-deactivated-escrow", to a scenario case so its known_bug marker applies.
type InvariantAssertions struct {
	Enabled       bool              `yaml:"enabled"`
	Baseline      InvariantWindow   `yaml:"baseline"`
	TrendEarly    InvariantWindow   `yaml:"trend_early"`
	TrendLate     InvariantWindow   `yaml:"trend_late"`
	Grace         string            `yaml:"grace"`
	MaxPendingAge string            `yaml:"max_pending_age"`
	Cases         map[string]string `yaml:"cases"`
}

// InvariantWindow is an offset range from the workload start.
type InvariantWindow struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
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
	// TTFTP99 and TokenIntervalP99 make ttft and token_interval lognormal
	// medians, seeded from the scenario seed. Empty keeps them fixed.
	TTFTP99          string `yaml:"ttft_p99"`
	TokenIntervalP99 string `yaml:"token_interval_p99"`
}

func LoadScenario(path string) (Scenario, error) {
	return loadScenario(path, false)
}

// LoadDatasetScenario allows messages to come entirely from the replay dataset.
func LoadDatasetScenario(path string) (Scenario, error) {
	return loadScenario(path, true)
}

func loadScenario(path string, dataset bool) (Scenario, error) {
	var scenario Scenario
	if err := loadYAML(path, &scenario); err != nil {
		return Scenario{}, err
	}
	if err := scenario.validate(dataset); err != nil {
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
	for field, p99 := range map[string]string{"ttft_p99": profile.TTFTP99, "token_interval_p99": profile.TokenIntervalP99} {
		if p99 == "" {
			continue
		}
		if _, err := time.ParseDuration(p99); err != nil {
			return Profile{}, fmt.Errorf("profile %s %s: %w", path, field, err)
		}
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
	return s.validate(false)
}

func (s Scenario) validate(dataset bool) error {
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
	if err := validateHostFaults(s.Topology, s.Workload.HostFaults, duration); err != nil {
		return err
	}
	if err := s.validateTopologyBindings(); err != nil {
		return err
	}
	if err := s.validateCases(); err != nil {
		return err
	}
	if err := validateEvents(s, duration); err != nil {
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
	if err := s.Workload.Request.validate(dataset); err != nil {
		return err
	}
	if s.Assertions.Requests.ErrorRate < 0 || s.Assertions.Requests.ErrorRate > 1 {
		return fmt.Errorf("assertions.requests.error_rate must be between 0 and 1")
	}
	if bound := s.Assertions.Requests.DroppedRate; bound != nil && (*bound < 0 || *bound > 1) {
		return fmt.Errorf("assertions.requests.dropped_rate must be between 0 and 1")
	}
	if s.Assertions.Requests.HTTPStatus == 0 || s.Assertions.Requests.TerminalOutcome == "" {
		return fmt.Errorf("request assertions require http_status and terminal_outcome")
	}
	if s.Assertions.Devshard.MaxGhostRate < 0 || s.Assertions.Devshard.MaxGhostRate > 1 {
		return fmt.Errorf("devshard max_ghost_rate must be between 0 and 1")
	}
	if err := s.validateInvariants(duration); err != nil {
		return err
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
	for name, value := range s.Gateway.ParticipantThrottle.settings() {
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
	seenRotationModels := map[string]bool{}
	for i, model := range rotation.Models {
		if !rotation.Enabled {
			return fmt.Errorf("gateway.escrow_rotation.models requires gateway.escrow_rotation.enabled")
		}
		if strings.TrimSpace(model.Model) == "" || seenRotationModels[model.Model] {
			return fmt.Errorf("gateway.escrow_rotation.models[%d]: model must be set and unique", i)
		}
		seenRotationModels[model.Model] = true
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

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (s Scenario) validateTopologyBindings() error {
	seenHosts := map[string]bool{}
	for i, host := range s.Topology.Hosts {
		if s.Topology.Participants == 0 || !validTopologyHost(host.ID, s.Topology.Participants) {
			return fmt.Errorf("topology.hosts[%d]: id must be a versiond host of an explicit topology.participants", i)
		}
		if seenHosts[host.ID] {
			return fmt.Errorf("topology.hosts[%d]: duplicate id %q", i, host.ID)
		}
		seenHosts[host.ID] = true
		for key := range host.Env {
			if !envKeyPattern.MatchString(key) {
				return fmt.Errorf("topology.hosts[%d].env: invalid variable name %q", i, key)
			}
		}
		if _, set := host.Env[rpcMsgsPerMinEnv]; host.RPCChatsPerMin < 0 || (set && host.RPCChatsPerMin > 0) {
			return fmt.Errorf("topology.hosts[%d].rpc_chats_per_min must be positive and replaces env %s", i, rpcMsgsPerMinEnv)
		}
	}
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

func (s Scenario) validateCases() error {
	seen := map[string]bool{}
	for i, c := range s.Cases {
		if strings.TrimSpace(c.ID) == "" || seen[c.ID] {
			return fmt.Errorf("cases[%d]: id must be set and unique", i)
		}
		seen[c.ID] = true
	}
	return nil
}

func (r Request) validate(dataset bool) error {
	if r.Timeout != "" {
		timeout, err := time.ParseDuration(r.Timeout)
		if err != nil || timeout <= 0 {
			return fmt.Errorf("workload.request.timeout must be a positive duration")
		}
	}
	if r.MaxTokens < 0 || !validTemperature(r.Temperature) || r.TopLogprobs < 0 || r.TopLogprobs > 20 {
		return fmt.Errorf("workload.request: max_tokens must be non-negative, temperature between 0 and 2, and top_logprobs between 0 and 20")
	}
	if err := r.Mix.validate("workload.request.mix", true); err != nil {
		return err
	}
	if r.Mix == nil || len(r.Mix.Models) == 0 {
		if strings.TrimSpace(r.Model) == "" {
			return fmt.Errorf("request model and messages are required")
		}
		if !dataset && !hasMessages(r.Messages, r.Mix) {
			return fmt.Errorf("request model and messages are required")
		}
	} else {
		for i, model := range r.Mix.Models {
			if !dataset && !hasMessages(model.Messages, model.Mix) && !hasMessages(r.Messages, r.Mix) {
				return fmt.Errorf("workload.request.mix.models[%d]: messages are required", i)
			}
		}
	}
	if r.Messages != nil && !validMessages(r.Messages) {
		return fmt.Errorf("workload.request.messages must contain non-empty roles and content")
	}
	return nil
}

func hasMessages(messages []Message, mix *RequestMix) bool {
	return len(messages) > 0 || (mix != nil && len(mix.Prompts) > 0)
}

func validMessages(messages []Message) bool {
	if len(messages) == 0 {
		return false
	}
	for _, message := range messages {
		if strings.TrimSpace(message.Role) == "" || message.Content == "" {
			return false
		}
	}
	return true
}

func validTemperature(v *float64) bool { return v == nil || (!math.IsNaN(*v) && *v >= 0 && *v <= 2) }
func validRatio(v *float64) bool       { return v == nil || (!math.IsNaN(*v) && *v >= 0 && *v <= 1) }
func validTopLogprobs(v *int) bool     { return v == nil || (*v >= 0 && *v <= 20) }

func (mix *RequestMix) validate(path string, allowModels bool) error {
	if mix == nil {
		return nil
	}
	if !validRatio(mix.StreamRatio) || !validRatio(mix.LogprobsRatio) {
		return fmt.Errorf("%s stream_ratio and logprobs_ratio must be between 0 and 1", path)
	}
	if !validTopLogprobs(mix.TopLogprobs) {
		return fmt.Errorf("%s.top_logprobs must be between 0 and 20", path)
	}
	if err := validateWeights(path+".max_tokens", mix.MaxTokens, func(v int) bool { return v > 0 }); err != nil {
		return err
	}
	if err := validateWeights(path+".temperature", mix.Temperature, func(v float64) bool { return validTemperature(&v) }); err != nil {
		return err
	}
	if err := validateWeights(path+".prompts", mix.Prompts, validMessages); err != nil {
		return err
	}
	if !allowModels && len(mix.Models) > 0 {
		return fmt.Errorf("%s.models: nested models are not supported", path)
	}
	seen := map[string]bool{}
	total := 0.0
	for i, model := range mix.Models {
		field := fmt.Sprintf("%s.models[%d]", path, i)
		if strings.TrimSpace(model.Value) == "" || seen[model.Value] || !validWeight(model.Weight) {
			return fmt.Errorf("%s: requires a unique model and a positive finite weight", field)
		}
		seen[model.Value] = true
		total += model.Weight
		if (model.Messages != nil && !validMessages(model.Messages)) || (model.MaxTokens != nil && *model.MaxTokens <= 0) || !validTemperature(model.Temperature) || !validTopLogprobs(model.TopLogprobs) {
			return fmt.Errorf("%s: invalid messages, max_tokens, temperature or top_logprobs", field)
		}
		if err := model.Mix.validate(field+".mix", false); err != nil {
			return err
		}
	}
	if math.IsInf(total, 0) {
		return fmt.Errorf("%s.models: total weight must be finite", path)
	}
	return nil
}

func validWeight(weight float64) bool {
	return weight > 0 && !math.IsInf(weight, 0) && !math.IsNaN(weight)
}

func validateWeights[T any](path string, choices []Weighted[T], valid func(T) bool) error {
	total := 0.0
	for i, choice := range choices {
		if !validWeight(choice.Weight) || !valid(choice.Value) {
			return fmt.Errorf("%s[%d]: requires a valid value and a positive finite weight", path, i)
		}
		total += choice.Weight
	}
	if math.IsInf(total, 0) {
		return fmt.Errorf("%s: total weight must be finite", path)
	}
	return nil
}

// RequestModels lists the models the generator can send, in mix order.
func (s Scenario) RequestModels() []string {
	if mix := s.Workload.Request.Mix; mix != nil && len(mix.Models) > 0 {
		models := make([]string, 0, len(mix.Models))
		for _, choice := range mix.Models {
			models = append(models, choice.Value)
		}
		return models
	}
	return []string{s.Workload.Request.Model}
}

func (s Scenario) RequestTimeout() time.Duration {
	if s.Workload.Request.Timeout == "" {
		return 30 * time.Second
	}
	d, _ := time.ParseDuration(s.Workload.Request.Timeout)
	return d
}

// invariantNamePattern accepts an invariant ("no-runtime-for-deactivated-escrow") or one of its checks ("escrow-accounting-reconciles.final_nonce_le_max").
var invariantNamePattern = regexp.MustCompile(`^(` + strings.Join(invariantNames(), "|") + `)(\.[a-z0-9_]+)?$`)

func invariantNames() []string {
	names := make([]string, 0, len(invariantEvaluators))
	for _, eval := range invariantEvaluators {
		names = append(names, eval.name)
	}
	return names
}

func (s Scenario) validateInvariants(workload time.Duration) error {
	inv := s.Assertions.Invariants
	for name, value := range map[string]string{"grace": inv.Grace, "max_pending_age": inv.MaxPendingAge} {
		if value == "" {
			continue
		}
		if d, err := time.ParseDuration(value); err != nil || d <= 0 {
			return fmt.Errorf("assertions.invariants.%s must be a positive duration", name)
		}
	}
	for name, window := range map[string]InvariantWindow{"baseline": inv.Baseline, "trend_early": inv.TrendEarly, "trend_late": inv.TrendLate} {
		if window.From == "" && window.To == "" {
			continue
		}
		from, e1 := time.ParseDuration(window.From)
		to, e2 := time.ParseDuration(window.To)
		if e1 != nil || e2 != nil || from < 0 || to <= from || to > workload {
			return fmt.Errorf("assertions.invariants.%s requires from < to inside the workload", name)
		}
	}
	declared := map[string]bool{}
	for _, c := range s.Cases {
		declared[c.ID] = true
	}
	for name, caseID := range inv.Cases {
		if !invariantNamePattern.MatchString(name) || !declared[caseID] {
			return fmt.Errorf("assertions.invariants.cases: %s must name an invariant (or one of its checks) and a declared case", name)
		}
	}
	return nil
}
