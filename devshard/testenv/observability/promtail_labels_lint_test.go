package observability_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Allowed Loki stream labels from the Promtail `labels:` stage. High-cardinality
// identifiers (trace_id, request_id, nonce, …) must stay in the log line body
// (I6 / T1.6).
var allowedPromtailLabels = map[string]struct{}{
	"level":   {},
	"service": {},
	"stage":   {},
}

func TestPromtailConfig_OnlyLowCardinalityLabels(t *testing.T) {
	path := filepath.Join(findTestenvObservabilityDir(t), "promtail-config.yaml")
	body, err := os.ReadFile(path)
	require.NoError(t, err)

	var cfg struct {
		ScrapeConfigs []struct {
			PipelineStages []map[string]any `yaml:"pipeline_stages"`
		} `yaml:"scrape_configs"`
	}
	require.NoError(t, yaml.Unmarshal(body, &cfg))
	require.NotEmpty(t, cfg.ScrapeConfigs)

	var labelKeys []string
	var jsonExprs map[string]any
	for _, sc := range cfg.ScrapeConfigs {
		for _, stage := range sc.PipelineStages {
			if raw, ok := stage["labels"]; ok {
				m, ok := raw.(map[string]any)
				require.True(t, ok, "labels stage must be a map")
				for k := range m {
					labelKeys = append(labelKeys, k)
				}
			}
			if raw, ok := stage["json"]; ok {
				jm, ok := raw.(map[string]any)
				require.True(t, ok)
				exprs, ok := jm["expressions"].(map[string]any)
				require.True(t, ok, "json.expressions must be a map")
				jsonExprs = exprs
			}
		}
	}

	require.NotEmpty(t, labelKeys, "expected a labels: stage")
	for _, k := range labelKeys {
		_, ok := allowedPromtailLabels[k]
		require.True(t, ok, "promtail must not promote %q to a Loki stream label (cardinality)", k)
	}

	// Correlation fields must be extracted (available to the pipeline / stay on the line)
	// but must not appear under labels.
	for _, key := range []string{"trace_id", "span_id", "request_id", "stage", "where"} {
		_, ok := jsonExprs[key]
		require.True(t, ok, "json.expressions missing %q", key)
	}
	for _, forbidden := range []string{"trace_id", "span_id", "request_id", "where", "nonce", "escrow_id"} {
		for _, k := range labelKeys {
			require.NotEqual(t, forbidden, k)
		}
	}
}

func TestLokiDatasource_DerivedFieldPointsAtTraceBackend(t *testing.T) {
	path := filepath.Join(findTestenvObservabilityDir(t), "grafana", "provisioning", "datasources", "loki.yaml")
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(body)

	require.Contains(t, text, `matcherRegex: '"trace_id":"(\w+)"'`)
	// T2 default profile is tempo-alloy; harness patches to jaeger for jaeger-promtail.
	require.Contains(t, text, "datasourceUid: tempo")
}

func TestTempoDatasource_TracesToLogs(t *testing.T) {
	path := filepath.Join(findTestenvObservabilityDir(t), "grafana", "provisioning", "datasources", "tempo.yaml")
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "uid: tempo")
	require.Contains(t, text, "tracesToLogsV2")
	require.Contains(t, text, "datasourceUid: loki")
	require.Contains(t, text, "trace_id")
}

func TestAlloyConfig_PreservesComposeServiceLabel(t *testing.T) {
	dir := filepath.Join(findTestenvObservabilityDir(t), "alloy")
	for _, name := range []string{"config.alloy", "config.base.alloy"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err, name)
		text := string(body)
		require.Contains(t, text, `compose_service`, name)
		require.Contains(t, text, `otelcol.receiver.otlp`, name)
		require.Contains(t, text, `loki.write`, name)
	}
	alloy := string(mustRead(t, filepath.Join(dir, "config.alloy")))
	require.Contains(t, alloy, `otelcol.exporter.otlp "trace_backend"`)
	require.Contains(t, alloy, `endpoint = "tempo:4317"`)
}

// High-cardinality identifiers must never become Loki stream labels via Alloy
// relabel rules (parent §11 / T3.7 cardinality guard).
func TestAlloyConfig_NoHighCardinalityLabels(t *testing.T) {
	dir := filepath.Join(findTestenvObservabilityDir(t), "alloy")
	forbidden := []string{"nonce", "escrow_id", "trace_id", "span_id", "request_id"}
	for _, name := range []string{"config.alloy", "config.base.alloy"} {
		text := string(mustRead(t, filepath.Join(dir, name)))
		for _, key := range forbidden {
			// Match intentional label assignments, not comments mentioning the words.
			require.NotContains(t, text, `target_label = "`+key+`"`, "%s must not promote %q", name, key)
			require.NotRegexp(t, `(?m)^\s*`+key+`\s*=`, text, "%s must not set stream label %q", name, key)
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	return body
}

func findTestenvObservabilityDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		candidate := filepath.Join(dir, "promtail-config.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("promtail-config.yaml not found walking up from cwd")
		}
		dir = parent
	}
}
