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
	SchemaVersion string     `yaml:"schema_version"`
	Scenario      string     `yaml:"scenario"`
	Seed          int64      `yaml:"seed"`
	Topology      Topology   `yaml:"topology"`
	Workload      Workload   `yaml:"workload"`
	Thresholds    Thresholds `yaml:"thresholds"`
	Assertions    Assertions `yaml:"assertions"`
	DrainTimeout  string     `yaml:"drain_timeout"`
}

type Topology struct {
	VersiondMode string         `yaml:"versiond_mode"`
	Storage      string         `yaml:"storage"`
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
	Mode        string  `yaml:"mode"`
	Concurrency int     `yaml:"concurrency"`
	Duration    string  `yaml:"duration"`
	Request     Request `yaml:"request"`
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

type Thresholds struct {
	ErrorRate float64 `yaml:"error_rate"`
}

type Assertions struct {
	Requests struct {
		HTTPStatus      int    `yaml:"http_status"`
		TerminalOutcome string `yaml:"terminal_outcome"`
	} `yaml:"requests"`
	Devshard struct {
		NoOrphanedWork bool `yaml:"no_orphaned_work"`
		RequireDrain   bool `yaml:"require_drain"`
	} `yaml:"devshard"`
	MockML struct {
		RequireEachNodeUsed bool `yaml:"require_each_node_used"`
	} `yaml:"mock_ml"`
}

type Profile struct {
	SchemaVersion string `yaml:"schema_version"`
	Profile       string `yaml:"profile"`
	TTFT          string `yaml:"ttft"`
	TokenInterval string `yaml:"token_interval"`
	Workers       int    `yaml:"workers"`
	Queue         int    `yaml:"queue"`
	Failures      []any  `yaml:"failures"`
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
	return profile, nil
}

func (s Scenario) Validate() error {
	if s.SchemaVersion != "v1" || strings.TrimSpace(s.Scenario) == "" {
		return fmt.Errorf("schema_version must be v1 and scenario must be set")
	}
	if s.Topology.VersiondMode != "multi" {
		return fmt.Errorf("only versiond_mode multi is supported")
	}
	if s.Topology.Storage != "per_host" {
		return fmt.Errorf("only topology.storage per_host is supported")
	}
	if s.Topology.Chain.EscrowAmount == 0 || s.Topology.Chain.MaxNonce == 0 {
		return fmt.Errorf("topology.chain requires positive escrow_amount and max_nonce")
	}
	if s.Topology.MockML.Allocator != "round_robin" {
		return fmt.Errorf("only mock_ml allocator round_robin is supported")
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
	if s.Workload.Mode != "closed_loop" || s.Workload.Concurrency <= 0 {
		return fmt.Errorf("only closed_loop workloads with positive concurrency are supported")
	}
	if _, err := time.ParseDuration(s.Workload.Duration); err != nil {
		return fmt.Errorf("workload duration: %w", err)
	}
	if s.Workload.Request.Stream {
		return fmt.Errorf("streaming requests are not supported by the initial runner")
	}
	if s.Workload.Request.Model == "" || len(s.Workload.Request.Messages) == 0 {
		return fmt.Errorf("request model and messages are required")
	}
	if s.Thresholds.ErrorRate < 0 || s.Thresholds.ErrorRate > 1 {
		return fmt.Errorf("error_rate must be between 0 and 1")
	}
	if s.Assertions.Requests.HTTPStatus == 0 || s.Assertions.Requests.TerminalOutcome == "" {
		return fmt.Errorf("request assertions require http_status and terminal_outcome")
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
