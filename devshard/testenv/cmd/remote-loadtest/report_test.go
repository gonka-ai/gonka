package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"devshard/testenv/loadtest"
)

func TestPerformanceReportSeparatesDropsAndTransportFailures(t *testing.T) {
	summary := loadtest.Summary{
		Requests: 4, Completed: 1, Failed: 3, Dropped: 1,
		Duration: 2 * time.Second,
		Results: []loadtest.RequestResult{
			{Status: 200, Outcome: "completed", Duration: time.Second},
			{Status: 503, Outcome: "error", Duration: 2 * time.Second},
			{Outcome: "error", Duration: 3 * time.Second},
			{Outcome: "dropped_by_generator"},
		},
	}
	var output bytes.Buffer
	printPerformanceReport(&output, summary, loadtest.Scenario{}, false, "/tmp/report", "")
	for _, want := range []string{"Result: FAIL", "Failed requests:       2", "Generator drops:       1", "0.50 req/s", "HTTP 200: 1", "HTTP 503: 1", "No HTTP response: 1", "2s"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing %q in report:\n%s", want, output.String())
		}
	}
}

func TestPerformanceReportEmpty(t *testing.T) {
	var output bytes.Buffer
	printPerformanceReport(&output, loadtest.Summary{}, loadtest.Scenario{}, false, "/tmp/report", "")
	if !strings.Contains(output.String(), "No attempted request latencies available") || strings.Contains(output.String(), "NaN") {
		t.Fatal(output.String())
	}
}
