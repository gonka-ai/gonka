package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

type remoteMetricsSample struct {
	At       time.Time          `json:"at"`
	Phase    string             `json:"phase"`
	Progress *int               `json:"progress_percent,omitempty"`
	File     string             `json:"file,omitempty"`
	Values   map[string]float64 `json:"values,omitempty"`
	Error    string             `json:"error,omitempty"`
}

type remoteMetricsSummary struct {
	Interval       string             `json:"interval"`
	StartedAt      time.Time          `json:"started_at"`
	FinishedAt     time.Time          `json:"finished_at"`
	Samples        int                `json:"samples"`
	Errors         []string           `json:"errors,omitempty"`
	Before         map[string]float64 `json:"before,omitempty"`
	After          map[string]float64 `json:"after,omitempty"`
	Peaks          map[string]float64 `json:"sampled_peaks,omitempty"`
	ProcessChanged bool               `json:"process_changed"`
}

type remoteMetricsCollector struct {
	client                  *http.Client
	url, key, model, output string
	index                   *os.File
	done                    chan struct{}
	stopSignal              chan struct{}
	started                 time.Time
	duration                time.Duration
	lastProgress            int
	summary                 remoteMetricsSummary
}

func startRemoteMetrics(ctx context.Context, client *http.Client, base, key, targetModel, output string, duration time.Duration) (*remoteMetricsCollector, error) {
	if err := os.Mkdir(filepath.Join(output, "metrics"), 0o755); err != nil {
		return nil, err
	}
	index, err := os.Create(filepath.Join(output, "metrics.jsonl"))
	if err != nil {
		return nil, err
	}
	c := &remoteMetricsCollector{client: client, url: base + "/metrics", key: key, model: targetModel, output: output, index: index, done: make(chan struct{}), stopSignal: make(chan struct{}), duration: duration, summary: remoteMetricsSummary{Interval: "10% of workload (0% through 100%), plus final snapshot", Peaks: map[string]float64{}}}
	zero := 0
	c.collect(ctx, "before", &zero)
	c.started = time.Now()
	go func() {
		defer close(c.done)
		for step := 1; step <= 10; step++ {
			timer := time.NewTimer(max(0, time.Until(c.started.Add(duration*time.Duration(step)/10))))
			select {
			case <-c.stopSignal:
				timer.Stop()
				return
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				progress := step * 10
				c.collect(ctx, "workload", &progress)
				c.lastProgress = progress
			}
		}
	}()
	return c, nil
}

func (c *remoteMetricsCollector) collect(ctx context.Context, phase string, progress *int) {
	sample := remoteMetricsSample{At: time.Now().UTC(), Phase: phase, Progress: progress}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var body []byte
	err := func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
		resp, err := c.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("metrics HTTP %d", resp.StatusCode)
		}
		body, err = io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
		if err != nil {
			return err
		}
		if len(body) > 8*1024*1024 {
			return fmt.Errorf("metrics response exceeds 8 MiB")
		}
		sample.File = filepath.Join("metrics", fmt.Sprintf("%04d-%s.prom", c.summary.Samples+1, phase))
		if err := os.WriteFile(filepath.Join(c.output, sample.File), body, 0o644); err != nil {
			return err
		}
		sample.Values, err = extractRemoteMetrics(body, c.model)
		return err
	}()
	c.summary.Samples++
	if err != nil {
		sample.Error = err.Error()
		c.summary.Errors = append(c.summary.Errors, phase+": "+err.Error())
	} else {
		if c.summary.Before == nil {
			c.summary.Before = sample.Values
			c.summary.StartedAt = sample.At
		}
		if previous, ok := c.summary.After["process_start_time_seconds"]; ok {
			if current, ok := sample.Values["process_start_time_seconds"]; ok && current != previous {
				c.summary.ProcessChanged = true
			}
		}
		c.summary.After = sample.Values
		c.summary.FinishedAt = sample.At
		for name, value := range sample.Values {
			if peak, ok := c.summary.Peaks[name]; !ok || value > peak {
				c.summary.Peaks[name] = value
			}
		}
	}
	if err := json.NewEncoder(c.index).Encode(sample); err != nil {
		c.summary.Errors = append(c.summary.Errors, "write sample index: "+err.Error())
	}
}

func (c *remoteMetricsCollector) stop() remoteMetricsSummary {
	close(c.stopSignal)
	<-c.done
	if c.lastProgress < 100 && time.Since(c.started) >= c.duration {
		hundred := 100
		c.collect(context.Background(), "workload", &hundred)
	}
	// Keep a final snapshot even when the workload was interrupted.
	c.collect(context.Background(), "after", nil)
	if err := c.index.Close(); err != nil {
		c.summary.Errors = append(c.summary.Errors, err.Error())
	}
	body, err := json.MarshalIndent(c.summary, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(c.output, "metrics-summary.json"), append(body, '\n'), 0o644)
	}
	if err != nil {
		c.summary.Errors = append(c.summary.Errors, "write metrics summary: "+err.Error())
	}
	return c.summary
}

func extractRemoteMetrics(body []byte, targetModel string) (map[string]float64, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	values := map[string]float64{}
	for _, name := range []string{"process_start_time_seconds", "process_resident_memory_bytes", "process_cpu_seconds_total", "go_memstats_heap_alloc_bytes", "go_goroutines", "devshard_gateway_inflight_requests"} {
		family := families[name]
		if family == nil || len(family.Metric) != 1 {
			continue
		}
		metric := family.Metric[0]
		if metric.Gauge != nil {
			values[name] = metric.Gauge.GetValue()
		}
		if metric.Counter != nil {
			values[name] = metric.Counter.GetValue()
		}
	}
	if family := families["devshard_gateway_chat_cache_total"]; family != nil {
		for _, metric := range family.Metric {
			var modelName, result string
			for _, label := range metric.Label {
				if label.GetName() == "model" {
					modelName = label.GetValue()
				}
				if label.GetName() == "result" {
					result = label.GetValue()
				}
			}
			if modelName == targetModel && metric.Counter != nil {
				values["cache_"+result] = metric.Counter.GetValue()
			}
		}
	}
	for _, item := range []struct{ name, prefix, group string }{
		{"devshard_gateway_requests_total", "requests_", "outcome"},
		{"devshard_gateway_attempt_failures_total", "attempt_failure_", "reason"},
		{"devshard_gateway_participant_transport_errors_total", "transport_errors", ""},
		{"devshard_gateway_participant_first_content_seconds", "first_content", ""},
		{"devshard_gateway_participant_receipt_seconds", "receipt", ""},
		{"devshard_gateway_participant_total_attempt_seconds", "attempt_duration", ""},
		{"devshard_gateway_participant_max_inter_chunk_seconds", "max_chunk_gap", ""},
	} {
		family := families[item.name]
		if family == nil {
			continue
		}
		for _, metric := range family.Metric {
			if metricLabel(metric, "model") != targetModel {
				continue
			}
			key := item.prefix
			if item.group != "" {
				key += metricLabel(metric, item.group)
			}
			if metric.Counter != nil {
				values[key] += metric.Counter.GetValue()
			}
			if metric.Histogram != nil {
				values[key+"_sum"] += metric.Histogram.GetSampleSum()
				values[key+"_count"] += float64(metric.Histogram.GetSampleCount())
			}
		}
	}
	return values, nil
}

func metricLabel(metric *dto.Metric, name string) string {
	for _, label := range metric.Label {
		if label.GetName() == name {
			return label.GetValue()
		}
	}
	return ""
}

func (s remoteMetricsSummary) delta(name string) (float64, bool) {
	start, haveStart := s.Before["process_start_time_seconds"]
	end, haveEnd := s.After["process_start_time_seconds"]
	before, haveBefore := s.Before[name]
	after, haveAfter := s.After[name]
	if s.ProcessChanged || !haveStart || !haveEnd || start != end || !haveBefore || !haveAfter || after < before {
		return 0, false
	}
	return after - before, true
}

func printRemoteMetrics(w io.Writer, s remoteMetricsSummary) {
	fmt.Fprintf(w, "\nGateway metrics summary\n-----------------------\n  Schedule: %s\n  Snapshots: %d; collection errors: %d\n", s.Interval, s.Samples, len(s.Errors))
	for _, item := range []struct{ name, label string }{{"process_resident_memory_bytes", "Gateway RSS"}, {"go_memstats_heap_alloc_bytes", "Go heap allocated"}} {
		before, b := s.Before[item.name]
		after, a := s.After[item.name]
		peak, p := s.Peaks[item.name]
		if b && a && p {
			fmt.Fprintf(w, "  %s: %.2f -> %.2f MiB; sampled peak %.2f MiB; change %+.2f MiB\n", item.label, before/(1024*1024), after/(1024*1024), peak/(1024*1024), (after-before)/(1024*1024))
		} else {
			fmt.Fprintf(w, "  %s: unavailable\n", item.label)
		}
	}
	if cpu, ok := s.delta("process_cpu_seconds_total"); ok {
		elapsed := s.FinishedAt.Sub(s.StartedAt).Seconds()
		fmt.Fprintf(w, "  Gateway CPU: %.3f CPU-seconds", cpu)
		if elapsed > 0 {
			fmt.Fprintf(w, "; average %.3f CPU cores over observed interval", cpu/elapsed)
		}
		fmt.Fprintln(w)
	} else {
		fmt.Fprintln(w, "  Gateway CPU delta: unavailable")
	}
	for _, item := range []struct{ name, label string }{{"cache_hit", "Cache hits"}, {"cache_stored", "Cache stores"}, {"requests_success", "Server successes"}, {"requests_failed", "Server failures"}, {"transport_errors", "Participant transport errors"}} {
		if value, ok := s.delta(item.name); ok {
			fmt.Fprintf(w, "  %s (model): +%.0f\n", item.label, value)
		} else {
			fmt.Fprintf(w, "  %s delta: unavailable\n", item.label)
		}
	}
	fmt.Fprintln(w, "\n  Server attempt timings (means of new histogram observations):")
	for _, item := range []struct{ prefix, label string }{{"receipt", "Receipt"}, {"first_content", "First content"}, {"attempt_duration", "Total attempt"}, {"max_chunk_gap", "Max chunk gap per attempt"}} {
		sum, haveSum := s.delta(item.prefix + "_sum")
		count, haveCount := s.delta(item.prefix + "_count")
		if haveSum && haveCount && count > 0 {
			fmt.Fprintf(w, "    %-27s %.3fs (%g observations)\n", item.label, sum/count, count)
		} else if haveSum && haveCount {
			fmt.Fprintf(w, "    %s: no new observations\n", item.label)
		} else {
			fmt.Fprintf(w, "    %s: unavailable\n", item.label)
		}
	}
	keys := make([]string, 0)
	for key := range s.After {
		if len(key) > len("attempt_failure_") && key[:len("attempt_failure_")] == "attempt_failure_" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if count, ok := s.delta(key); ok {
			fmt.Fprintf(w, "  Participant attempt failures (%s): +%.0f\n", key[len("attempt_failure_"):], count)
		}
	}
	if len(s.Errors) > 0 {
		for _, err := range s.Errors {
			fmt.Fprintf(w, "  Collection error: %s\n", err)
		}
	}
	if s.ProcessChanged {
		fmt.Fprintln(w, "  Gateway process changed: true")
	}
	fmt.Fprintln(w, "\n  metrics/*.prom | metrics.jsonl | metrics-summary.json")
}
