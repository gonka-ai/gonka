package main

import (
	"encoding/json"
	"flag"
	"os/exec"
	"strings"
	"time"

	"devshard/testenv/loadtest"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"testing"
)

func TestPrintMetricsSummaryShowsValuesAndMissingMetrics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.log")
	file, err := os.Create(path)
	require.NoError(t, err)
	prior := os.Stdout
	os.Stdout = file
	t.Cleanup(func() { os.Stdout = prior; _ = file.Close() })
	printMetricsSummary(loadtest.MetricsSummary{Interval: "1s", Processes: []loadtest.ProcessMetricsSummary{{
		Target: "gateway", Samples: 3,
		First:         map[string]float64{"process_resident_memory_bytes": 1 << 20},
		Last:          map[string]float64{"process_resident_memory_bytes": 2 << 20},
		Peaks:         map[string]float64{"process_resident_memory_bytes": 4 << 20},
		WorkloadPeaks: map[string]float64{"process_resident_memory_bytes": 3 << 20},
		Deltas:        map[string]float64{"process_cpu_seconds_total": 2},
		FailedSamples: 1, LastErrors: []string{"missing go_goroutines"},
	}}}, 1000)
	os.Stdout = prior
	require.NoError(t, file.Close())
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(body), "baseline=1.000 MiB final=2.000 MiB workload peak=3.000 MiB all-phase peak=4.000 MiB")
	require.Contains(t, string(body), "RSS high-water mark: unavailable")
	require.Contains(t, string(body), "incomplete/failed=1")
	require.Contains(t, string(body), "CPU per 1000 completed client requests: 2.000 s")
	require.Contains(t, string(body), "missing go_goroutines")
}

func TestTerminalFileLinkKeepsRedirectedOutputPlain(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "console")
	require.NoError(t, err)
	prior := os.Stdout
	os.Stdout = f
	t.Cleanup(func() { os.Stdout = prior; _ = f.Close() })
	link := terminalFileLink("/tmp/load results/timeline.html")
	require.Equal(t, "file:///tmp/load%20results/timeline.html", link)
	require.NotContains(t, link, "\033")
}

// Exercise the actual CLI report pipeline with the new per-model schema and
// messages supplied by a dataset, rather than a synthetic request in run.yaml.
func TestDatasetModelReportsKeepPlainConsoleMarkdownAndTimeline(t *testing.T) {
	fixture := filepath.Join("..", "..", "loadtest", "scenarios", "qwen-two-models-smoke.yaml")
	scenario, err := loadtest.LoadScenario(fixture)
	require.NoError(t, err)
	scenario.Workload.Request.Messages = nil
	dir := t.TempDir()
	body, err := yaml.Marshal(scenario)
	require.NoError(t, err)
	scenarioPath := filepath.Join(dir, "run.yaml")
	require.NoError(t, os.WriteFile(scenarioPath, body, 0o644))
	_, err = loadtest.LoadScenario(scenarioPath)
	require.Error(t, err) // ordinary validation still requires a prompt
	scenario, err = loadtest.LoadDatasetScenario(scenarioPath)
	require.NoError(t, err)

	started := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	models := scenario.RequestModels()
	require.Len(t, models, 2)
	results := []loadtest.RequestResult{
		{StartedAt: started.Add(time.Second), Model: models[0], Status: 200, Outcome: "completed", Duration: 100 * time.Millisecond},
		{StartedAt: started.Add(2 * time.Second), Model: models[1], Status: 200, Outcome: "completed", Duration: 200 * time.Millisecond},
	}
	write := func(name string, value any) {
		b, err := json.Marshal(value)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), b, 0o644))
	}
	summary := loadtest.Summary{Scenario: scenario.Scenario, StartedAt: started, Duration: scenario.Duration() + 20*time.Second, Requests: 2, Completed: 2, ModelBreakdown: loadtest.BuildModelBreakdown(scenario, results)}
	write("summary.json", summary)
	write("assertions.json", map[string]any{"passed": true, "checks": []loadtest.AssertionResult{}})
	f, err := os.Create(filepath.Join(dir, "requests.jsonl"))
	require.NoError(t, err)
	for _, result := range results {
		require.NoError(t, json.NewEncoder(f).Encode(result))
	}
	require.NoError(t, f.Close())

	console, err := writeConsoleReport(dir)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(console, "MODEL SUMMARY"))
	require.Contains(t, console, "Qwen3 4B")
	require.Contains(t, console, "Qwen2.5 7B")
	require.Contains(t, console, "HTML timeline:")
	require.NotContains(t, console, "| Metric")
	require.NotContains(t, console, "## Model summary")
	markdown, err := os.ReadFile(filepath.Join(dir, "report.md"))
	require.NoError(t, err)
	require.Contains(t, string(markdown), "| Metric | "+models[0]+" | "+models[1]+" |")
	html, err := os.ReadFile(filepath.Join(dir, "timeline.html"))
	require.NoError(t, err)
	_, tail, ok := strings.Cut(string(html), `<script id="timeline-data" type="application/json">`)
	require.True(t, ok)
	data, _, ok := strings.Cut(tail, "</script>")
	require.True(t, ok)
	var timeline struct {
		Traffic []struct {
			X float64 `json:"x"`
		} `json:"traffic"`
	}
	require.NoError(t, json.Unmarshal([]byte(data), &timeline))
	require.Len(t, timeline.Traffic, 3) // 30s workload, excludes 20s response tail
	require.Less(t, timeline.Traffic[len(timeline.Traffic)-1].X, scenario.Duration().Minutes())
}

func TestPrestartFailureDoesNotPrintEmptyReports(t *testing.T) {
	if os.Getenv("GONKA_TEST_PRESTART_CHILD") == "1" {
		flag.CommandLine = flag.NewFlagSet("loadtest", flag.ExitOnError)
		os.Args = []string{"loadtest", "-scenario", os.Getenv("GONKA_TEST_SCENARIO"), "-output", os.Getenv("GONKA_TEST_OUTPUT")}
		main()
		os.Exit(0)
	}
	testenv, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "loadtest", "profiles"), 0o755))
	profile, err := os.ReadFile(filepath.Join(testenv, "loadtest", "profiles", "fast.yaml"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "loadtest", "profiles", "fast.yaml"), profile, 0o644))
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(bin, 0o755))
	docker := `#!/bin/sh
if [ "$1" = compose ] && [ "$2" = ls ]; then
    echo '[{"Name":"loadtest-existing"}]'
    exit 0
fi
echo 'unexpected Docker operation' >&2
exit 7
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(docker), 0o755))
	output := filepath.Join(dir, "results")
	cmd := exec.Command(os.Args[0], "-test.run=^TestPrestartFailureDoesNotPrintEmptyReports$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GONKA_TEST_PRESTART_CHILD=1", "GONKA_TEST_SCENARIO="+filepath.Join(testenv, "loadtest", "scenarios", "normal-load.yaml"), "GONKA_TEST_OUTPUT="+output, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	data, err := cmd.CombinedOutput()
	require.Error(t, err)
	text := string(data)
	require.Contains(t, text, "workload did not start")
	require.Contains(t, text, "another load-test stack is running (loadtest-existing)")
	require.NotContains(t, text, "Client traffic")
	require.NotContains(t, text, "Gateway state")
	require.NotContains(t, text, "no HTML timeline")
	require.NotContains(t, text, "summary.json")
	_, err = os.Stat(filepath.Join(output, "report.md"))
	require.True(t, os.IsNotExist(err))
}
