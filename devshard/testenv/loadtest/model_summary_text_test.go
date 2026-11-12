package loadtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestModelTextSummaryAlignsSectionsAndKeepsMarkdownSeparate(t *testing.T) {
	s := mixScenario()
	s.Workload.Request.Mix.Models = []ModelRequest{
		{Value: "Qwen/Qwen3-4B-Instruct-2507", Weight: 1},
		{Value: "Qwen/Qwen2.5-7B-Instruct", Weight: 1},
	}
	b := BuildModelBreakdown(s, []RequestResult{
		{Model: s.RequestModels()[0], Status: 200, Outcome: "completed", Duration: 105944 * time.Microsecond},
		{Model: s.RequestModels()[1], Status: 200, Outcome: "completed", Duration: 1334101 * time.Microsecond},
	})
	text := RenderModelBreakdownText(b)
	require.Contains(t, text, "Qwen3 4B = Qwen/Qwen3-4B-Instruct-2507")
	require.Contains(t, text, "Qwen2.5 7B = Qwen/Qwen2.5-7B-Instruct")
	require.Contains(t, text, "105.94ms")
	require.Contains(t, text, "1.33s")
	require.NotContains(t, text, "| Metric")
	require.NotContains(t, text, "**")
	require.NotContains(t, text, "HTTP 500")
	require.NotContains(t, text, "\t")
	firstValueColumn := -1
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "Metric ") {
			firstValueColumn = strings.Index(line, "Qwen3 4B")
		}
	}
	require.Positive(t, firstValueColumn)
	for _, prefix := range []string{"Client result ", "Planned ", "Errors total ", "  Samples ", "  P50 ", "Successful requests/s "} {
		found := false
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, prefix) {
				found = true
				require.Greater(t, len(line), firstValueColumn)
				require.NotEqual(t, byte(' '), line[firstValueColumn], line)
				require.Equal(t, firstValueColumn, len(prefix)+len(line[len(prefix):])-len(strings.TrimLeft(line[len(prefix):], " ")), line)
			}
		}
		require.True(t, found, prefix)
	}
	dir := reportFixtureDir(t)
	writeReportFixture(t, dir, "summary.json", Summary{Scenario: s.Scenario, ModelBreakdown: b})
	terminal, err := WriteReportForTerminal(dir)
	require.NoError(t, err)
	require.Contains(t, terminal, "MODEL SUMMARY")
	require.NotContains(t, terminal, "## Model summary")
	markdown, err := os.ReadFile(filepath.Join(dir, "report.md"))
	require.NoError(t, err)
	require.Contains(t, string(markdown), "| Metric | Qwen/Qwen3-4B-Instruct-2507 |")
	require.NotContains(t, string(markdown), "MODEL SUMMARY")
}

func TestModelTextSummaryUsesUniqueBoundedLabelsAndNonzeroErrors(t *testing.T) {
	models := []ModelSummary{
		{Model: "vendor-a/shared-name"},
		{Model: "vendor-b/shared-name"},
		{Model: "vendor/very-very-long-model-name-with-many-details"},
		{Model: "Total"},
	}
	labels := ModelColumnLabels(models)
	seen := map[string]bool{"Total": true}
	for _, label := range labels {
		require.False(t, seen[label])
		seen[label] = true
		require.LessOrEqual(t, len(label), 18)
	}
	s := mixScenario()
	b := BuildModelBreakdown(s, []RequestResult{{Model: "model-a", Status: 503, Outcome: "error"}})
	text := RenderModelBreakdownText(b)
	require.Contains(t, text, "HTTP 503")
	require.NotContains(t, text, "HTTP 500")
	require.NotContains(t, text, "Timeout")
	require.Regexp(t, `Errors total +1 +0 +1`, text)
	require.Regexp(t, `P99 +- +- +-`, text)
	require.Equal(t, "", RenderModelBreakdownText(nil))
}
