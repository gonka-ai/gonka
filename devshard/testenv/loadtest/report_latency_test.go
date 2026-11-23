package loadtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeLatencyRequests(t *testing.T, outputDir string) {
	t.Helper()
	file, err := os.Create(filepath.Join(outputDir, "requests.jsonl"))
	require.NoError(t, err)
	defer file.Close()
	encoder := json.NewEncoder(file)
	write := func(maxTokens int64, outcome string, duration time.Duration) {
		require.NoError(t, encoder.Encode(RequestResult{MaxTokens: maxTokens, Outcome: outcome, Duration: duration}))
	}
	for second := 1; second <= 10; second++ {
		write(32, "completed", time.Duration(second)*time.Second)
	}
	for range 4 {
		write(2048, "completed", time.Minute)
	}
	write(2048, "http_error", 5*time.Second)
	write(2048, "dropped_by_generator", 0)
}

// Test flow:
// 1. Write a run directory whose requests.jsonl holds ten 32-token requests of 1-10 s, four completed 2048-token requests of 1 m, one failed after 5 s and one dropped.
// 2. Render the report through WriteReport.
// 3. Require run-summary.json to keep the overall p50/p95 and add the p95 over completed requests and each class's counts, p50, p95 and completed-only p95, the dropped request in none.
// 4. Require report.md to carry the completed-only p95 row and one row per class.
func TestWriteReportGivesLatencyPerMaxTokensClass(t *testing.T) {
	outputDir := reportFixtureDir(t)
	writeLatencyRequests(t, outputDir)

	report, err := WriteReport(outputDir)

	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(outputDir, runSummaryFileName))
	require.NoError(t, err)
	var summary RunSummary
	require.NoError(t, json.Unmarshal(body, &summary))
	require.Equal(t, RunSummaryLatency{
		P50:          2,
		P95:          9,
		P95Completed: 60,
		Classes: []RunSummaryLatencyClass{
			{MaxTokens: 32, Requests: 10, Completed: 10, P50: 5, P95: 9, P95Completed: 9},
			{MaxTokens: 2048, Requests: 5, Completed: 4, P50: 60, P95: 60, P95Completed: 60},
		},
	}, summary.LatencySeconds)
	require.Contains(t, report, "| latency p95 of completed requests | 1m0s |  |  |")
	require.Contains(t, report, "| max_tokens | requests | completed | p50 | p95 | p95 of completed |")
	require.Contains(t, report, "| 32 | 10 | 10 | 5s | 9s | 9s |")
	require.Contains(t, report, "| 2048 | 5 | 4 | 1m0s | 1m0s | 1m0s |")
}

// Test flow:
// 1. Write a run directory without requests.jsonl.
// 2. Render the report.
// 3. Require the per-class section to say the classes were not recorded and run-summary.json to carry no classes.
func TestWriteReportNamesAMissingRequestsFileForTheClasses(t *testing.T) {
	outputDir := reportFixtureDir(t)

	report, err := WriteReport(outputDir)

	require.NoError(t, err)
	require.Contains(t, report, "### Latency by max_tokens\n\nNot recorded: requests.jsonl is not readable")
	body, err := os.ReadFile(filepath.Join(outputDir, runSummaryFileName))
	require.NoError(t, err)
	var summary RunSummary
	require.NoError(t, json.Unmarshal(body, &summary))
	require.Empty(t, summary.LatencySeconds.Classes)
}
