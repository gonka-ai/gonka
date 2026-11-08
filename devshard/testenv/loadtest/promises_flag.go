package loadtest

import (
	"bytes"
	"fmt"
	"os"
	"time"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"gopkg.in/yaml.v3"
)

// withPromises turns on, in a scenario file, what the promise checks need: assertions.promises,
// diagnostics.export and diagnostics.logs.tail all. It edits the YAML tree, so every other key keeps
// the value the file gives it.
func withPromises(raw []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the scenario is not a YAML mapping")
	}
	root := doc.Content[0]
	setYAMLScalar(yamlMapping(root, "assertions"), "promises", "!!bool", "true")
	diagnostics := yamlMapping(root, "diagnostics")
	setYAMLScalar(diagnostics, "export", "!!bool", "true")
	setYAMLScalar(yamlMapping(diagnostics, "logs"), "tail", "!!str", "all")
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// yamlMapping returns the mapping under key, adding it, or replacing an empty value, when there is none.
func yamlMapping(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			value := parent.Content[i+1]
			if value.Kind != yaml.MappingNode {
				*value = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
			return value
		}
	}
	value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
	return value
}

func setYAMLScalar(parent *yaml.Node, key, tag, value string) {
	scalar := yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			*parent.Content[i+1] = scalar
			return
		}
	}
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &scalar)
}

// promisesRunFile rewrites the run's copy of the scenario with the promise checks on and loads it, so
// the run, its later checks and a recheck all read the scenario that ran.
func promisesRunFile(path string) (Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, err
	}
	edited, err := withPromises(raw)
	if err != nil {
		return Scenario{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		return Scenario{}, err
	}
	return LoadScenario(path)
}

// LoadScenarioWithPromises loads a scenario as the runner runs it with RunnerConfig.Promises.
func LoadScenarioWithPromises(path string) (Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, err
	}
	edited, err := withPromises(raw)
	if err != nil {
		return Scenario{}, fmt.Errorf("%s: %w", path, err)
	}
	var scenario Scenario
	if err := yaml.Unmarshal(edited, &scenario); err != nil {
		return Scenario{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := scenario.Validate(); err != nil {
		return Scenario{}, fmt.Errorf("scenario %s: %w", path, err)
	}
	return scenario, nil
}

// PromisesWait is the longest a run held to the promises waits after the load before the export:
// until an inference it started at the end of the load is due (promisesHold). A scenario without
// protocol timeouts leaves refusal_timeout and execution_timeout of config.yaml at 0, and the
// mock-chain seed (mockchain/seed FromFile) puts the chain's defaults in their place:
// DefaultDevshardRefusalTimeout (60 s) and DefaultDevshardExecutionTimeout (32 min) of
// x/inference/types. holdForPromises ends earlier once the Gateway holds no inference open within its
// deadline.
func (s Scenario) PromisesWait() time.Duration {
	if !s.Assertions.Promises {
		return 0
	}
	refusal, execution := inferencetypes.DefaultDevshardRefusalTimeout, inferencetypes.DefaultDevshardExecutionTimeout
	if s.Protocol.ExecutionTimeout != "" {
		var err error
		if refusal, err = protocolTimeoutSeconds(s.Protocol.RefusalTimeout); err != nil {
			return 0
		}
		if execution, err = protocolTimeoutSeconds(s.Protocol.ExecutionTimeout); err != nil {
			return 0
		}
	}
	return promisesHold(refusal, execution)
}
