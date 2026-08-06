package observability_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	for _, forbidden := range []string{"trace_id", "span_id", "request_id", "where", "nonce"} {
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
	// T1 exit criteria runs on jaeger-promtail; T2 retargets to tempo.
	require.Regexp(t, regexp.MustCompile(`datasourceUid:\s*(jaeger|tempo)`), text)
	require.True(t,
		strings.Contains(text, "datasourceUid: jaeger") || strings.Contains(text, "datasourceUid: tempo"),
		"derived field must target a trace backend",
	)
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
