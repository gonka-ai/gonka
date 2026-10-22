package loadtest

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// loadTestServiceEnv is set on every service of a load-test stack. It is empty: the stack keeps the
// services' own environment. The grpc-go resolver's SRV _grpclb._tcp.<host> and TXT
// _grpc_config.<host> lookups go out through Docker's embedded DNS to the host resolver; a host whose
// firewall drops DNS from the stack's bridge network makes every new channel wait ~8 s for them, so
// that path must answer (NXDOMAIN) instead of being switched off here.
var loadTestServiceEnv = [][2]string{}

// loadTestServicePrefixEnv is set on the services whose name starts with the prefix. The mock ML nodes
// report usage.completion_tokens as the tokens they emitted, as vLLM does, so a host's Finish can be
// held to what the ML produced rather than to the mock's len(text)/4 estimate.
var loadTestServicePrefixEnv = []struct {
	prefix string
	env    [][2]string
}{
	{"mock-openai", [][2]string{{"MOCK_OPENAI_HONEST_USAGE", "1"}}},
}

// loadTestServiceLogging keeps every container's log for the whole run. The
// Docker daemon default here is json-file 10m x 5, and compose.log is taken
// with `docker compose logs` only at the end, so a busy service loses the
// start of the run.
var loadTestServiceLogging = map[string]any{
	"driver":  "json-file",
	"options": map[string]string{"max-size": "1g", "max-file": "3"},
}

// applyLoadTestComposeOverrides adds the load-test environment and log
// settings to every service of the generated Compose file. Values already
// set in a service's environment are replaced; an explicit logging section is
// kept as it is.
func applyLoadTestComposeOverrides(composePath string) error {
	body, err := os.ReadFile(composePath)
	if err != nil {
		return err
	}
	out, err := loadTestComposeOverrides(body)
	if err != nil {
		return fmt.Errorf("%s: %w", composePath, err)
	}
	return os.WriteFile(composePath, out, 0o644)
}

func loadTestComposeOverrides(body []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("compose file is not a mapping")
	}
	services := mappingValue(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("compose file has no services")
	}
	var logging yaml.Node
	if err := logging.Encode(loadTestServiceLogging); err != nil {
		return nil, err
	}
	for i := 1; i < len(services.Content); i += 2 {
		name, service := services.Content[i-1].Value, services.Content[i]
		if service.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("service %s is not a mapping", name)
		}
		env := mappingValue(service, "environment")
		if env == nil {
			env = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			service.Content = append(service.Content, scalar("environment"), env)
		}
		if env.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("service %s: environment is not a mapping", name)
		}
		pairs := loadTestServiceEnv
		for _, p := range loadTestServicePrefixEnv {
			if strings.HasPrefix(name, p.prefix) {
				pairs = append(append([][2]string{}, pairs...), p.env...)
			}
		}
		for _, kv := range pairs {
			if v := mappingValue(env, kv[0]); v != nil {
				*v = *quoted(kv[1])
				continue
			}
			env.Content = append(env.Content, scalar(kv[0]), quoted(kv[1]))
		}
		if mappingValue(service, "logging") == nil {
			copied := logging
			service.Content = append(service.Content, scalar("logging"), &copied)
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 1; i < len(m.Content); i += 2 {
		if m.Content[i-1].Value == key {
			return m.Content[i]
		}
	}
	return nil
}

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

func quoted(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle}
}
