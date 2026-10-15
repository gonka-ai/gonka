package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const remoteMetricsFixture = `# TYPE process_start_time_seconds gauge
process_start_time_seconds 100
# TYPE process_resident_memory_bytes gauge
process_resident_memory_bytes 1048576
# TYPE process_cpu_seconds_total counter
process_cpu_seconds_total 3
# TYPE devshard_gateway_chat_cache_total counter
devshard_gateway_chat_cache_total{model="target",result="hit"} 12
devshard_gateway_chat_cache_total{model="other",result="hit"} 99
`

func TestRemoteMetricsBeforePeriodicAfter(t *testing.T) {
	var calls atomic.Int32
	periodic := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gateway/metrics" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("incorrect metrics URL or auth")
		}
		if calls.Add(1) == 11 {
			periodic <- struct{}{}
		}
		fmt.Fprint(w, remoteMetricsFixture)
	}))
	defer server.Close()
	out := t.TempDir()
	c, err := startRemoteMetrics(context.Background(), server.Client(), server.URL+"/gateway", "test-key", "target", out, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-periodic:
	case <-time.After(2 * time.Second):
		c.stop()
		t.Fatal("no periodic metrics")
	}
	summary := c.stop()
	if len(summary.Errors) > 0 || summary.Samples != 12 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	body, err := os.ReadFile(filepath.Join(out, "metrics.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	for i, line := range lines {
		var sample remoteMetricsSample
		if err := json.Unmarshal([]byte(line), &sample); err != nil {
			t.Fatal(err)
		}
		if i == 0 && sample.Phase != "before" {
			t.Fatal("missing baseline")
		}
		if i == len(lines)-1 && sample.Phase != "after" {
			t.Fatal("missing final snapshot")
		}
		if i < 11 && (sample.Progress == nil || *sample.Progress != i*10) {
			t.Fatalf("wrong progress at sample %d: %+v", i, sample)
		}
		if i == 11 && sample.Progress != nil {
			t.Fatal("final response snapshot must be separate from 100%")
		}
		if sample.Values["cache_hit"] != 12 {
			t.Fatalf("wrong model cache counter: %+v", sample.Values)
		}
		if _, err := os.Stat(filepath.Join(out, sample.File)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRemoteMetricsAnalysisUsesIntervalDeltas(t *testing.T) {
	started := time.Now()
	s := remoteMetricsSummary{
		StartedAt: started, FinishedAt: started.Add(10 * time.Second),
		Before: map[string]float64{"process_start_time_seconds": 100, "process_cpu_seconds_total": 2, "cache_hit": 10, "first_content_sum": 4, "first_content_count": 2},
		After:  map[string]float64{"process_start_time_seconds": 100, "process_cpu_seconds_total": 4, "cache_hit": 15, "first_content_sum": 10, "first_content_count": 4},
	}
	var out bytes.Buffer
	printRemoteMetrics(&out, s)
	for _, want := range []string{"2.000 CPU-seconds", "0.200 CPU cores", "Cache hits (model): +5", "3.000s (2 observations)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Findings") || strings.Contains(out.String(), "includes cached responses") {
		t.Fatal("report must contain metrics without automated findings")
	}
	s.After["process_start_time_seconds"] = 200
	if _, ok := s.delta("cache_hit"); ok {
		t.Fatal("must not calculate deltas across restarts")
	}
	s.After["process_start_time_seconds"] = 100
	s.After["cache_hit"] = 1
	if _, ok := s.delta("cache_hit"); ok {
		t.Fatal("must not calculate negative counter deltas")
	}
}

func TestRemoteMetricsAggregatesAttemptHistogramsByModel(t *testing.T) {
	body := `# TYPE devshard_gateway_participant_first_content_seconds histogram
devshard_gateway_participant_first_content_seconds_sum{model="target",participant_key="a"} 4
devshard_gateway_participant_first_content_seconds_count{model="target",participant_key="a"} 2
devshard_gateway_participant_first_content_seconds_sum{model="target",participant_key="b"} 6
devshard_gateway_participant_first_content_seconds_count{model="target",participant_key="b"} 3
devshard_gateway_participant_first_content_seconds_sum{model="other",participant_key="a"} 100
devshard_gateway_participant_first_content_seconds_count{model="other",participant_key="a"} 10
`
	values, err := extractRemoteMetrics([]byte(body), "target")
	if err != nil {
		t.Fatal(err)
	}
	if values["first_content_sum"] != 10 || values["first_content_count"] != 5 {
		t.Fatalf("wrong model aggregation: %+v", values)
	}
}

func TestRemoteMetricsUnavailableIsRecorded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	c, err := startRemoteMetrics(context.Background(), server.Client(), server.URL, "test", "target", t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	summary := c.stop()
	if summary.Samples != 2 || len(summary.Errors) != 2 || len(summary.After) != 0 {
		t.Fatalf("missing collection errors: %+v", summary)
	}
}
