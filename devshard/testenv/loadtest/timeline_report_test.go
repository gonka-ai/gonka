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

func TestTimelineUsesStartTimeBinsAndActualEvents(t *testing.T) {
	dir := t.TempDir()
	writeReportFixture(t, dir, "summary.json", Summary{Scenario: "timeline-test", StartedAt: fixtureT0, Duration: 25 * time.Second})
	request := func(at time.Duration, outcome string) RequestResult {
		return RequestResult{StartedAt: fixtureT0.Add(at), Duration: 20 * time.Second, Outcome: outcome}
	}
	writeJSONLines(t, filepath.Join(dir, "requests.jsonl"), request(time.Second, "completed"), request(2*time.Second, "error"), request(3*time.Second, "dropped_by_generator"), request(11*time.Second, "completed"), request(21*time.Second, "dropped_by_generator"))
	writeJSONLines(t, filepath.Join(dir, "metrics.jsonl"),
		MetricsSample{At: fixtureT0.Add(-time.Second), Target: "gateway", Values: map[string]float64{"process_resident_memory_bytes": 64 << 20}},
		MetricsSample{At: fixtureT0.Add(time.Second), Target: "gateway", Values: map[string]float64{"process_resident_memory_bytes": 128 << 20, "go_memstats_heap_alloc_bytes": 32 << 20}},
		MetricsSample{At: fixtureT0.Add(4 * time.Second), Target: "gateway", Values: map[string]float64{"process_resident_memory_bytes": 256 << 20, "go_memstats_heap_alloc_bytes": 64 << 20}},
		MetricsSample{At: fixtureT0.Add(6 * time.Second), Target: "versiond-2/devshardd", Values: map[string]float64{"go_memstats_heap_alloc_bytes": 16 << 20}},
	)
	writeReportFixture(t, dir, "events.json", []EventRecord{{ID: "poc", Action: "poc_window", PlannedStart: fixtureT0.Add(4 * time.Second), StartedAt: atPtr(5 * time.Second), EndedAt: atPtr(15 * time.Second), Params: map[string]any{"preserved": "versiond-2"}}})
	data, err := loadTimelineData(dir)
	require.NoError(t, err)
	require.Len(t, data.Traffic, 3)
	require.InDelta(t, .1, data.Traffic[0].OK, 1e-9) // completion belongs to its start bin
	require.InDelta(t, .3, data.Traffic[0].Scheduled, 1e-9)
	require.InDelta(t, 100.0/3, data.Traffic[0].Drop, 1e-9)
	require.Equal(t, 50.0, data.Traffic[0].Error)   // errors / sent, excludes drops
	require.Equal(t, .2, data.Traffic[2].Scheduled) // five-second partial tail
	require.Equal(t, 100.0, data.Traffic[2].Drop)
	require.Len(t, data.Series, 2)
	require.Equal(t, "gateway", data.Series[0].Key)
	require.Equal(t, 128.0, *data.Series[0].Points[0].RSS)
	require.Equal(t, 48.0, *data.Series[0].Points[0].Heap)
	require.Nil(t, data.Series[1].Points[0].RSS) // absent metrics are not zero
	require.Equal(t, 16.0, *data.Series[1].Points[0].Heap)
	require.InDelta(t, 5.0/60, data.Events[0].From, 1e-9)
	require.InDelta(t, 15.0/60, data.Events[0].To, 1e-9)
	require.Equal(t, "PoC 1 · preserved: versiond-2", data.Events[0].Label)
}

func TestTimelineIsOfflineAndEscapesLabels(t *testing.T) {
	dir := t.TempDir()
	scenario := `</script><script>alert("unexpected")</script>`
	writeReportFixture(t, dir, "summary.json", Summary{Scenario: scenario, StartedAt: fixtureT0, Duration: 10 * time.Second, LatencyWindows: []LatencyWindow{{StartedAt: fixtureT0, Duration: 10 * time.Second, Offered: 3, Completed: 1, Failed: 1, Dropped: 1}}})
	data, err := loadTimelineData(dir)
	require.NoError(t, err)
	require.Equal(t, 50.0, data.Traffic[0].Error)
	require.NotEmpty(t, data.Notes)
	path, err := WriteTimelineReport(dir)
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(path))
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	html := string(body)
	require.Contains(t, html, `<html lang="en">`)
	require.NotContains(t, html, `<script src=`)
	require.NotContains(t, html, `fetch(`)
	require.Equal(t, 2, strings.Count(html, "<script"))
	const marker = `<script id="timeline-data" type="application/json">`
	_, tail, found := strings.Cut(html, marker)
	require.True(t, found)
	encoded, _, found := strings.Cut(tail, "</script>")
	require.True(t, found)
	var embedded timelineData
	require.NoError(t, json.Unmarshal([]byte(encoded), &embedded))
	require.Equal(t, scenario, embedded.Scenario)
	require.NotContains(t, encoded, "</script>")
}

func TestTimelineReportsMalformedInput(t *testing.T) {
	dir := t.TempDir()
	writeReportFixture(t, dir, "summary.json", Summary{StartedAt: fixtureT0, Duration: time.Minute})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "requests.jsonl"), []byte("broken json\n"), 0o644))
	_, err := WriteTimelineReport(dir)
	require.ErrorContains(t, err, "requests.jsonl")
}

func TestTimelinePoCNamesAndObservedHARoles(t *testing.T) {
	dir := t.TempDir()
	writeReportFixture(t, dir, "summary.json", Summary{Scenario: "poc-labels", StartedAt: fixtureT0, Duration: 6 * time.Minute})
	event := func(id, kind, host string, from, to time.Duration) EventRecord {
		return EventRecord{ID: id, Action: "poc_window", StartedAt: atPtr(from), EndedAt: atPtr(to), Params: map[string]any{"kind": kind, "preserved": host}}
	}
	// Out-of-order records and unrelated events must not change PoC numbering.
	writeReportFixture(t, dir, "events.json", []EventRecord{
		event("second", "poc", "versiond-2", 3*time.Minute, 4*time.Minute),
		{ID: "maintenance", Action: "marker", StartedAt: atPtr(10 * time.Second), EndedAt: atPtr(20 * time.Second)},
		event("first", "poc", "versiond-0", time.Minute, 2*time.Minute),
		event("confirmation", "cpoc", "versiond-2", 5*time.Minute, 6*time.Minute),
		{ID: "not-fired", Action: "poc_window"},
	})
	sample := func(offset time.Duration, p0PoC, p1PoC bool) DapiHostSample {
		return DapiHostSample{At: fixtureT0.Add(offset), Hosts: map[string]DapiHostRow{
			"versiond-0": {Nodes: []string{"ml0"}, Challenged: p0PoC},
			"versiond-1": {Nodes: []string{"ml1"}, Challenged: p0PoC},
			"versiond-2": {Nodes: []string{"ml2"}, Challenged: p1PoC},
			"unmapped":   {Nodes: []string{"unknown"}, Challenged: true},
		}}
	}
	writeJSONLines(t, filepath.Join(dir, "dapi-host-stats.jsonl"), sample(30*time.Second, false, false), sample(90*time.Second, false, true), sample(210*time.Second, true, false))
	data, err := loadTimelineData(dir)
	require.NoError(t, err)
	require.Len(t, data.Events, 4)
	require.Equal(t, "maintenance", data.Events[0].Title)
	require.Equal(t, "PoC 1", data.Events[1].Title)
	require.Equal(t, []string{"versiond-2"}, data.Events[1].Nodes)
	require.Equal(t, []string{"versiond-0", "versiond-1"}, data.Events[1].PreservedNodes)
	require.Equal(t, "PoC 2", data.Events[2].Title)
	require.Equal(t, []string{"versiond-0", "versiond-1"}, data.Events[2].Nodes)
	require.Equal(t, []string{"versiond-2"}, data.Events[2].PreservedNodes)
	require.Equal(t, "CPoC 1", data.Events[3].Title)
	require.Empty(t, data.Events[3].Nodes) // no host-state evidence in this window
	require.Equal(t, "CPoC 1 · preserved: versiond-2", data.Events[3].Label)
}
