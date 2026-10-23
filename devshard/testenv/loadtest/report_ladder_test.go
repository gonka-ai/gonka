package loadtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const ladderFixtureScenario = `schema_version: v1
scenario: ladder-fixture
seed: 1515
workload:
  traffic:
    type: stages
    stages:
      - {duration: 1m, rps: 2}
      - {duration: 1m, rps: 4}
      - {duration: 1m, rps: 8}
assertions:
  requests:
    terminal_outcome: completed
    error_rate: 0.1
    dropped_rate: 0.1
`

func writeJSONLines(t *testing.T, path string, values ...any) {
	t.Helper()
	var body strings.Builder
	for _, value := range values {
		line, err := json.Marshal(value)
		require.NoError(t, err)
		body.Write(line)
		body.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(path, []byte(body.String()), 0o644))
}

func ladderRequest(offset, duration time.Duration, outcome string) RequestResult {
	return RequestResult{StartedAt: at(offset), Duration: duration, Outcome: outcome}
}

func ladderSample(offset time.Duration, target string, values, domain map[string]float64) MetricsSample {
	return MetricsSample{At: at(offset), Target: target, Values: values, Domain: domain}
}

func ladderFixtureDir(t *testing.T) string {
	t.Helper()
	outputDir := reportFixtureDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(outputDir, "run.yaml"), []byte(ladderFixtureScenario), 0o644))
	writeReportFixture(t, outputDir, "summary.json", Summary{Scenario: "ladder-fixture", Seed: 1515, StartedAt: fixtureT0, Requests: 62, Completed: 59, Failed: 1, Dropped: 2})
	var requests []any
	for i := 0; i < 20; i++ {
		requests = append(requests, ladderRequest(time.Duration(i)*3*time.Second, time.Second, "completed"))
	}
	for i := 0; i < 22; i++ {
		outcome := "completed"
		switch {
		case i < 2:
			outcome = "dropped_by_generator"
		case i == 2:
			outcome = "error"
		}
		requests = append(requests, ladderRequest(time.Minute+time.Duration(i)*2500*time.Millisecond, 1500*time.Millisecond, outcome))
	}
	for i := 0; i < 20; i++ {
		requests = append(requests, ladderRequest(2*time.Minute+time.Duration(i)*3*time.Second, 3*time.Second, "completed"))
	}
	writeJSONLines(t, filepath.Join(outputDir, "requests.jsonl"), requests...)
	cpu := func(value float64) map[string]float64 { return map[string]float64{"process_cpu_seconds_total": value} }
	attempts := func(value float64) map[string]float64 {
		return map[string]float64{`devshard_gateway_attempts_started_total{model="model-a"}`: value}
	}
	terminals := func(finished, refused float64) map[string]float64 {
		return map[string]float64{`devshard_request_terminal_total{reason="",terminal="finished"}`: finished, `devshard_request_terminal_total{reason="rate_limited",terminal="no_receipt_interrupted"}`: refused}
	}
	writeJSONLines(t, filepath.Join(outputDir, "metrics.jsonl"),
		ladderSample(0, "gateway", cpu(0), attempts(0)),
		ladderSample(0, "versiond-0/devshardd", nil, terminals(0, 0)),
		ladderSample(0, "versiond-1/devshardd", nil, terminals(0, 0)),
		ladderSample(0, "escrow", nil, nil),
		ladderSample(time.Minute, "gateway", cpu(30), attempts(40)),
		ladderSample(time.Minute, "versiond-0/devshardd", nil, terminals(25, 5)),
		ladderSample(time.Minute, "versiond-1/devshardd", nil, terminals(30, 0)),
		ladderSample(2*time.Minute, "gateway", cpu(90), attempts(100)),
		ladderSample(2*time.Minute, "versiond-0/devshardd", nil, terminals(80, 10)),
		ladderSample(2*time.Minute, "versiond-1/devshardd", nil, terminals(60, 0)),
		ladderSample(150*time.Second, "gateway", cpu(5), attempts(10)),
		ladderSample(3*time.Minute, "gateway", cpu(35), attempts(40)),
		ladderSample(3*time.Minute, "versiond-0/devshardd", nil, terminals(200, 10)),
		ladderSample(3*time.Minute, "versiond-1/devshardd", nil, terminals(90, 0)),
	)
	return outputDir
}

// Test flow:
// 1. Write a run directory with a three-stage run.yaml, requests in every stage, and metrics.jsonl with gateway CPU, gateway attempts and host Chat terminals, including a gateway restart in stage 3.
// 2. Build the per-step table from the artifacts.
// 3. Require the counts, shares, latencies, CPU cores, attempts per request and per-host Chat rates of each step, with the restart's counter reset counted as new work.
// 4. Require only stage 3, whose p95 is three times stage 1's, to be marked as where degradation starts.
func TestLoadLadderBuildsOneRowPerStage(t *testing.T) {
	outputDir := ladderFixtureDir(t)

	ladder := loadLadder(outputDir, Summary{StartedAt: fixtureT0})

	require.Empty(t, ladder.notes)
	require.Equal(t, 0.1, ladder.errorBound)
	require.Equal(t, 0.1, ladder.droppedBound)
	require.Len(t, ladder.steps, 3)
	first, second, third := ladder.steps[0], ladder.steps[1], ladder.steps[2]
	require.Equal(t, ladderStep{RPS: 2, From: 0, To: time.Minute, Offered: 20, Sent: 20, Completed: 20, P50: time.Second, P95: time.Second, Classes: []tokenClassLatency{{Completed: 20, P95: time.Second}}, GatewayCores: 0.5, HasCPU: true, AttemptsPerRequest: 2, HasAttempts: true, HostChats: []hostChatRate{{Host: "versiond-0", PerSecond: 0.5}, {Host: "versiond-1", PerSecond: 0.5}}}, first)
	require.Equal(t, 22, second.Offered)
	require.Equal(t, 20, second.Sent)
	require.Equal(t, 19, second.Completed)
	require.Equal(t, 2, second.Dropped)
	require.Equal(t, 1500*time.Millisecond, second.P95)
	require.InDelta(t, 1.0, second.GatewayCores, 1e-9)
	require.InDelta(t, 3.0, second.AttemptsPerRequest, 1e-9)
	require.Equal(t, []hostChatRate{{Host: "versiond-0", PerSecond: 1}, {Host: "versiond-1", PerSecond: 0.5}}, second.HostChats)
	require.Equal(t, 3*time.Second, third.P95)
	require.InDelta(t, 35.0/60, third.GatewayCores, 1e-9)
	require.InDelta(t, 2.0, third.AttemptsPerRequest, 1e-9)
	require.Equal(t, []hostChatRate{{Host: "versiond-0", PerSecond: 2}, {Host: "versiond-1", PerSecond: 0.5}}, third.HostChats)
	require.Empty(t, first.Note)
	require.Empty(t, second.Note)
	require.Equal(t, "degradation starts here: p95 3s > 2x step 1's 1s", third.Note)
}

// Test flow:
// 1. Write the three-stage run directory with its checks.
// 2. Render the report through WriteReport.
// 3. Require the load-step section after client traffic and before the fault windows, with the bounds in its header and one row per stage.
func TestWriteReportRendersTheLoadSteps(t *testing.T) {
	outputDir := ladderFixtureDir(t)

	report, err := WriteReport(outputDir)

	require.NoError(t, err)
	section := strings.Index(report, "### Load steps")
	require.Greater(t, section, strings.Index(report, "## 1. Client traffic"))
	require.Less(t, section, strings.Index(report, "## 2. Fault windows"))
	require.Contains(t, report, "errors above 10.00% or never sent above 10.00%")
	require.Contains(t, report, "| 1 | 2 | 0s–1m0s | 20 of 120 (16.67%) | 20 | 0 of 20 (0.00%) | 0 of 20 (0.00%) | 1s | 1s | 0.50 | 2.00 | versiond-0 0.5, versiond-1 0.5 |  |")
	require.Contains(t, report, "| 2 | 4 | 1m0s–2m0s | 22 of 240 (9.17%) | 20 | 1 of 20 (5.00%) | 2 of 22 (9.09%) | 1.5s | 1.5s | 1.00 | 3.00 | versiond-0 1.0, versiond-1 0.5 |  |")
	require.Contains(t, report, "| 3 | 8 | 2m0s–3m0s | 20 of 480 (4.17%) | 20 | 0 of 20 (0.00%) | 0 of 20 (0.00%) | 3s | 3s | 0.58 | 2.00 | versiond-0 2.0, versiond-1 0.5 | degradation starts here: p95 3s > 2x step 1's 1s |")
}

// Test flow:
// 1. Write a run directory whose run.yaml has one stage, and another with a stage scenario but no requests.jsonl or metrics.jsonl.
// 2. Render both reports.
// 3. Require no load-step section for the single stage, and a section that names each missing artifact for the other.
func TestWriteReportLoadStepsNeedSeveralStagesAndNameMissingData(t *testing.T) {
	single := reportFixtureDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(single, "run.yaml"), []byte(strings.Replace(ladderFixtureScenario, "      - {duration: 1m, rps: 4}\n      - {duration: 1m, rps: 8}\n", "", 1)), 0o644))
	missing := reportFixtureDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(missing, "run.yaml"), []byte(ladderFixtureScenario), 0o644))

	singleReport, err := WriteReport(single)
	require.NoError(t, err)
	missingReport, err := WriteReport(missing)
	require.NoError(t, err)

	require.NotContains(t, singleReport, "### Load steps")
	require.Contains(t, missingReport, "### Load steps")
	require.Contains(t, missingReport, "requests.jsonl: not readable")
	require.Contains(t, missingReport, "metrics.jsonl: not readable, so no gateway CPU, attempts or host Chat rate")
}

// Test flow:
// 1. Build step rows that cross each bound in turn: p95 above twice step 1's, errors above the error bound, never-sent share above the drop bound, and none.
// 2. Find where degradation starts for each.
// 3. Require the first crossing step and its reason, step 1 included for errors, and none when no bound is crossed.
func TestDegradationStartIsTheFirstStepOverABound(t *testing.T) {
	healthy := ladderStep{Offered: 100, Sent: 100, Completed: 100, P95: 10 * time.Second}
	slow := healthy
	slow.P95 = 21 * time.Second
	failing := healthy
	failing.Completed = 97
	dropping := healthy
	dropping.Offered, dropping.Dropped = 110, 10

	cases := []struct {
		name   string
		steps  []ladderStep
		index  int
		reason string
	}{
		{"p95", []ladderStep{healthy, healthy, slow, failing}, 2, "p95 21s > 2x step 1's 10s"},
		{"errors at step 1", []ladderStep{failing, slow}, 0, "errors 3.00% > 2.00%"},
		{"never sent", []ladderStep{healthy, dropping}, 1, "never sent 9.09% > 5.00%"},
		{"none", []ladderStep{healthy, healthy}, -1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			index, reason := degradationStart(tc.steps, 0.02, 0.05)

			require.Equal(t, tc.index, index)
			require.Equal(t, tc.reason, reason)
		})
	}
}

func classRequests(from time.Duration, count int, maxTokens int64, duration time.Duration) []RequestResult {
	requests := make([]RequestResult, 0, count)
	for i := 0; i < count; i++ {
		requests = append(requests, RequestResult{StartedAt: at(from + time.Duration(i)*100*time.Millisecond), Duration: duration, Outcome: "completed", MaxTokens: maxTokens})
	}
	return requests
}

func twoStepLadder(first, second [][]RequestResult) ladderReport {
	ladder := ladderReport{errorBound: 0.02, droppedBound: 0.05, steps: []ladderStep{{RPS: 8, From: 0, To: time.Minute}, {RPS: 16, From: time.Minute, To: 2 * time.Minute}}}
	var results []RequestResult
	for _, group := range append(first, second...) {
		results = append(results, group...)
	}
	ladder.countRequests(results, fixtureT0, "completed")
	return ladder
}

// Test flow:
// 1. Build two steps with the same per-class latency, 16 s at max_tokens 512 and 60 s at 2048, where the 2048 share grows from 5% to 7.5%.
// 2. Require the overall p95 to jump from 16 s to 60 s, more than twice step 1's.
// 3. Require no degradation mark, because no max_tokens class got slower.
func TestDegradationStartIgnoresATokenMixShift(t *testing.T) {
	ladder := twoStepLadder(
		[][]RequestResult{classRequests(0, 380, 512, 16*time.Second), classRequests(0, 20, 2048, 60*time.Second)},
		[][]RequestResult{classRequests(time.Minute, 370, 512, 16*time.Second), classRequests(time.Minute, 30, 2048, 60*time.Second)},
	)

	require.Equal(t, 16*time.Second, ladder.steps[0].P95)
	require.Equal(t, 60*time.Second, ladder.steps[1].P95)
	index, reason := degradationStart(ladder.steps, ladder.errorBound, ladder.droppedBound)
	require.Equal(t, -1, index)
	require.Empty(t, reason)
}

// Test flow:
// 1. Build two steps with the same 5% max_tokens 2048 share, where the 2048 class's latency doubles from 60 s to 121 s and the 512 class stays at 16 s.
// 2. Require the overall p95 to stay at 16 s in both steps.
// 3. Require step 2 to be marked, naming the 2048 class and both p95 values.
// 4. Build the same mix where the 512 class doubles instead and require step 2 to be marked for that class.
func TestDegradationStartFiresWhenP95DoublesWithinAClass(t *testing.T) {
	slowLong := twoStepLadder(
		[][]RequestResult{classRequests(0, 380, 512, 16*time.Second), classRequests(0, 20, 2048, 60*time.Second)},
		[][]RequestResult{classRequests(time.Minute, 380, 512, 16*time.Second), classRequests(time.Minute, 20, 2048, 121*time.Second)},
	)
	slowShort := twoStepLadder(
		[][]RequestResult{classRequests(0, 380, 512, 16*time.Second), classRequests(0, 20, 2048, 60*time.Second)},
		[][]RequestResult{classRequests(time.Minute, 380, 512, 33*time.Second), classRequests(time.Minute, 20, 2048, 60*time.Second)},
	)

	require.Equal(t, 16*time.Second, slowLong.steps[1].P95)
	index, reason := degradationStart(slowLong.steps, slowLong.errorBound, slowLong.droppedBound)
	require.Equal(t, 1, index)
	require.Equal(t, "max_tokens 2048 p95 2m1s > 2x step 1's 1m0s", reason)
	index, reason = degradationStart(slowShort.steps, slowShort.errorBound, slowShort.droppedBound)
	require.Equal(t, 1, index)
	require.Equal(t, "max_tokens 512 p95 33s > 2x step 1's 16s", reason)
}

// Test flow:
// 1. Build two steps where the 2048 class has 19 completions in step 1 and its latency triples in step 2, and the 512 class is flat.
// 2. Require no mark, since a p95 over fewer than 20 completions is not compared.
func TestDegradationStartSkipsClassesTooSmallForAP95(t *testing.T) {
	ladder := twoStepLadder(
		[][]RequestResult{classRequests(0, 381, 512, 16*time.Second), classRequests(0, 19, 2048, 60*time.Second)},
		[][]RequestResult{classRequests(time.Minute, 380, 512, 16*time.Second), classRequests(time.Minute, 20, 2048, 180*time.Second)},
	)

	index, _ := degradationStart(ladder.steps, ladder.errorBound, ladder.droppedBound)
	require.Equal(t, -1, index)
}

// Test flow:
// 1. Build a two-step ladder at 1 rps where step 2 has 30 sent requests and 20 slots the generator fell behind on.
// 2. Require the 20 behind slots to count as never sent against the 50 planned.
// 3. Render the table and require the offered and planned column to show 30 of 60 for step 2.
func TestLadderCountsGeneratorBehindAsNeverSentAndShowsOfferedOverPlanned(t *testing.T) {
	ladder := ladderReport{errorBound: 0.02, droppedBound: 0.05, steps: []ladderStep{{RPS: 1, From: 0, To: time.Minute}, {RPS: 1, From: time.Minute, To: 2 * time.Minute}}}
	results := classRequests(0, 60, 0, time.Second)
	results = append(results, classRequests(time.Minute, 30, 0, time.Second)...)
	for i := 0; i < 20; i++ {
		results = append(results, RequestResult{StartedAt: at(time.Minute + 30*time.Second + time.Duration(i)*time.Second), Outcome: "dropped_by_generator", Error: generatorBehind})
	}
	ladder.countRequests(results, fixtureT0, "completed")

	require.Equal(t, 50, ladder.steps[1].Offered)
	require.Equal(t, 20, ladder.steps[1].Dropped)
	require.Equal(t, 20, ladder.steps[1].Behind)
	index, reason := degradationStart(ladder.steps, ladder.errorBound, ladder.droppedBound)
	require.Equal(t, 1, index)
	require.Equal(t, "never sent 40.00% > 5.00%", reason)
	var out strings.Builder
	runReport{ladder: ladder}.renderLadder(&out)
	require.Contains(t, out.String(), "| 2 | 1 | 1m0s–2m0s | 30 of 60 (50.00%) | 30 |")
}
