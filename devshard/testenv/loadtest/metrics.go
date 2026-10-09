package loadtest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"devshard/testenv/config"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// MetricsSample keeps absent metrics absent, rather than reporting them as zero.
// Start time and PID distinguish process lifetimes, including restarts.
type MetricsSample struct {
	At        time.Time           `json:"at"`
	Phase     string              `json:"phase"`
	Target    string              `json:"target"`
	Values    map[string]float64  `json:"values,omitempty"`
	Errors    []string            `json:"errors,omitempty"`
	EscrowID  string              `json:"escrow_id,omitempty"`
	Nonce     *uint64             `json:"nonce,omitempty"`
	State     *GatewayStateSizes  `json:"state,omitempty"`
	BuildInfo []map[string]string `json:"build_info,omitempty"`
}

type ProcessMetricsSummary struct {
	Target         string             `json:"target"`
	Samples        int                `json:"samples"`
	FailedSamples  int                `json:"failed_samples"`
	ProcessChanges int                `json:"process_changes"`
	First          map[string]float64 `json:"first,omitempty"`
	Last           map[string]float64 `json:"last,omitempty"`
	Peaks          map[string]float64 `json:"sampled_peaks,omitempty"`
	WorkloadPeaks  map[string]float64 `json:"workload_sampled_peaks,omitempty"`
	Deltas         map[string]float64 `json:"observed_counter_deltas,omitempty"`
	LastErrors     []string           `json:"last_errors,omitempty"`
}

type EscrowMetricsSummary struct {
	EscrowID           string             `json:"escrow_id"`
	Samples            int                `json:"samples"`
	FirstNonce         *uint64            `json:"first_nonce,omitempty"`
	LastNonce          *uint64            `json:"last_nonce,omitempty"`
	First              *GatewayStateSizes `json:"first,omitempty"`
	Last               *GatewayStateSizes `json:"last,omitempty"`
	MaxDiffs           int                `json:"max_diffs"`
	MaxDiffBytes       int64              `json:"max_diff_bytes"`
	MaxSignatureNonces int                `json:"max_signature_nonces"`
}

type MetricsSummary struct {
	Interval         string                  `json:"interval"`
	StartedAt        time.Time               `json:"started_at"`
	FinishedAt       time.Time               `json:"finished_at"`
	CollectionErrors []string                `json:"collection_errors,omitempty"`
	Metadata         map[string]string       `json:"run_metadata"`
	Processes        []ProcessMetricsSummary `json:"processes"`
	Escrows          []EscrowMetricsSummary  `json:"escrows"`
}

type metricsCollector struct {
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	phase     atomic.Value
	file      *os.File
	encoder   *json.Encoder
	summary   MetricsSummary
	processes map[string]*ProcessMetricsSummary
	previous  map[string]map[string]float64
	escrows   map[string]*EscrowMetricsSummary
	collect   func(context.Context) []MetricsSample
	stopOnce  sync.Once
	outputDir string
}

var runtimeMetricNames = []string{
	"process_resident_memory_bytes", "process_cpu_seconds_total", "process_start_time_seconds",
	"go_memstats_heap_alloc_bytes", "go_memstats_heap_inuse_bytes", "go_memstats_heap_objects",
	"go_memstats_alloc_bytes_total", "go_memstats_last_gc_time_seconds", "go_goroutines",
}
var counterMetricNames = []string{"process_cpu_seconds_total", "go_memstats_alloc_bytes_total", "go_gc_duration_seconds_count", "go_gc_duration_seconds_sum"}

func parseRuntimeMetrics(body string) (map[string]float64, []map[string]string, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	values := map[string]float64{}
	for _, name := range append(runtimeMetricNames, "loadtest_process_pid", "loadtest_process_start_ticks", "loadtest_process_rss_hwm_bytes") {
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
		if metric.Untyped != nil {
			values[name] = metric.Untyped.GetValue()
		}
	}
	if family := families["go_gc_duration_seconds"]; family != nil && len(family.Metric) == 1 && family.Metric[0].Summary != nil {
		values["go_gc_duration_seconds_count"] = float64(family.Metric[0].Summary.GetSampleCount())
		values["go_gc_duration_seconds_sum"] = family.Metric[0].Summary.GetSampleSum()
	}
	var builds []map[string]string
	for name, family := range families {
		if !strings.HasSuffix(name, "build_info") {
			continue
		}
		for _, metric := range family.Metric {
			labels := map[string]string{"metric": name}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			builds = append(builds, labels)
		}
	}
	for name, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			delete(values, name)
		}
	}
	if len(values) == 0 {
		return nil, builds, fmt.Errorf("no process/runtime metrics found")
	}
	return values, builds, nil
}

// Read only the executable name and memory counters. Do not expose cmdline/env.
const processProbe = `found=0
for p in /proc/[0-9]*; do
  read -r name 2>/dev/null < "$p/comm" || continue
  [ "$name" = "$1" ] || continue
  found=$((found+1))
  printf 'loadtest_process_pid %s\n' "${p##*/}"
  awk '{printf "loadtest_process_start_ticks %.0f\n", $22}' "$p/stat"
  awk '/^VmHWM:/ {printf "loadtest_process_rss_hwm_bytes %.0f\n", $2*1024}' "$p/status"
done
[ "$found" -eq 1 ]`

func dockerProbe(ctx context.Context, dir, project, compose, service, executable, metricsURL string) (string, error) {
	script := processProbe
	if metricsURL != "" {
		script = `wget -q -T 3 -O - "$2" || exit 1
` + script
	}
	cmd := exec.CommandContext(ctx, "docker", "compose", "-p", project, "-f", compose, "exec", "-T", service, "sh", "-c", script, "probe", executable, metricsURL)
	cmd.Dir = dir
	return probeCommandOutput(cmd, service)
}

// Docker/shell diagnostics must never be parsed as Prometheus samples.
func probeCommandOutput(cmd *exec.Cmd, service string) (string, error) {
	body, err := cmd.Output()
	if err != nil {
		var stderr string
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(exitErr.Stderr))
		}
		return "", fmt.Errorf("%s process probe: %w: %s", service, err, stderr)
	}
	return string(body), nil
}

func getMetricsHTTP(ctx context.Context, url, apiKey string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 4 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return string(body), err
}

func startMetricsCollector(ctx context.Context, opts RunnerConfig, cfg *config.File, project, compose, gatewayURL, apiKey string) (*metricsCollector, error) {
	interval := opts.MetricsInterval
	if interval == 0 {
		interval = time.Second
	}
	if interval < 100*time.Millisecond {
		return nil, fmt.Errorf("metrics interval must be at least 100ms")
	}
	file, err := os.Create(filepath.Join(opts.OutputDir, "metrics.jsonl"))
	if err != nil {
		return nil, err
	}
	c := newMetricsCollector(ctx, file, opts.OutputDir, interval)
	c.summary.Metadata = metricsRunMetadata(opts.TestenvDir, cfg)
	c.collect = func(ctx context.Context) []MetricsSample {
		var mu sync.Mutex
		var samples []MetricsSample
		var wg sync.WaitGroup
		add := func(sample MetricsSample) { mu.Lock(); samples = append(samples, sample); mu.Unlock() }
		scrape := func(target, service, executable, url string) {
			defer wg.Done()
			sample := MetricsSample{At: time.Now().UTC(), Target: target}
			body, err := dockerProbe(ctx, opts.TestenvDir, project, compose, service, executable, url)
			if err != nil {
				sample.Errors = append(sample.Errors, err.Error())
			}
			if service == "devshardctl" {
				httpBody, httpErr := getMetricsHTTP(ctx, gatewayURL+"/metrics", apiKey)
				if httpErr != nil {
					sample.Errors = append(sample.Errors, httpErr.Error())
				} else {
					body = httpBody + "\n" + body
				}
			}
			if body != "" {
				sample.Values, sample.BuildInfo, err = parseRuntimeMetrics(body)
				if err != nil {
					sample.Errors = append(sample.Errors, err.Error())
				}
				for _, name := range append(append([]string{}, runtimeMetricNames...), "go_gc_duration_seconds_count", "go_gc_duration_seconds_sum", "loadtest_process_pid", "loadtest_process_start_ticks", "loadtest_process_rss_hwm_bytes") {
					if _, ok := sample.Values[name]; !ok {
						sample.Errors = append(sample.Errors, "missing "+name)
					}
				}
			}
			add(sample)
		}
		wg.Add(1)
		go scrape("gateway", "devshardctl", "devshardctl", "")
		for _, host := range cfg.Hosts {
			wg.Add(1)
			go scrape(host.ID+"/devshardd", host.ID, "devshardd", fmt.Sprintf("http://127.0.0.1:%d/%s/metrics", config.DefaultHostPort, cfg.Versiond.VersionName))
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids, err := fetchGatewayDevshardIDs(ctx, gatewayURL, apiKey)
			if err != nil {
				add(MetricsSample{At: time.Now().UTC(), Target: "escrows", Errors: []string{err.Error()}})
				return
			}
			for _, id := range ids {
				base := scopedGatewayURL(gatewayURL, id)
				sample := MetricsSample{At: time.Now().UTC(), Target: "escrow", EscrowID: id}
				sizes, err := fetchGatewayStateSizes(ctx, base, apiKey)
				if err != nil {
					sample.Errors = append(sample.Errors, err.Error())
				} else {
					sample.State = &sizes
				}
				statusBody, err := getMetricsHTTP(ctx, base+"/v1/status", apiKey)
				if err != nil {
					sample.Errors = append(sample.Errors, err.Error())
				} else {
					var status struct {
						Nonce *uint64 `json:"nonce"`
					}
					if err := json.Unmarshal([]byte(statusBody), &status); err != nil {
						sample.Errors = append(sample.Errors, err.Error())
					} else {
						sample.Nonce = status.Nonce
					}
				}
				add(sample)
			}
		}()
		wg.Wait()
		sort.Slice(samples, func(i, j int) bool {
			return samples[i].Target+samples[i].EscrowID < samples[j].Target+samples[j].EscrowID
		})
		return samples
	}
	// Baseline is synchronous, before the first load request.
	c.sample()
	go func() {
		defer close(c.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-ticker.C:
				c.sample()
			}
		}
	}()
	return c, nil
}

func newMetricsCollector(ctx context.Context, file *os.File, outputDir string, interval time.Duration) *metricsCollector {
	child, cancel := context.WithCancel(ctx)
	c := &metricsCollector{ctx: child, cancel: cancel, done: make(chan struct{}), file: file, encoder: json.NewEncoder(file), outputDir: outputDir,
		summary: MetricsSummary{Interval: interval.String(), StartedAt: time.Now().UTC()}, processes: map[string]*ProcessMetricsSummary{}, previous: map[string]map[string]float64{}, escrows: map[string]*EscrowMetricsSummary{}}
	c.phase.Store("baseline")
	return c
}

func (c *metricsCollector) sample() {
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	phase := c.phase.Load().(string)
	for _, sample := range c.collect(ctx) {
		sample.Phase = phase
		if err := c.encoder.Encode(sample); err != nil {
			c.summary.CollectionErrors = append(c.summary.CollectionErrors, err.Error())
			c.cancel()
			return
		}
		c.observe(sample)
	}
}

func (c *metricsCollector) observe(sample MetricsSample) {
	if sample.EscrowID != "" {
		s := c.escrows[sample.EscrowID]
		if s == nil {
			s = &EscrowMetricsSummary{EscrowID: sample.EscrowID}
			c.escrows[sample.EscrowID] = s
		}
		if sample.State != nil {
			s.Samples++
			if s.First == nil {
				s.First = sample.State
			}
			s.Last = sample.State
			s.MaxDiffs = max(s.MaxDiffs, sample.State.Diffs)
			s.MaxDiffBytes = max(s.MaxDiffBytes, sample.State.DiffsBytes)
			s.MaxSignatureNonces = max(s.MaxSignatureNonces, sample.State.SignatureNonces)
		}
		if sample.Nonce != nil {
			if s.FirstNonce == nil {
				s.FirstNonce = sample.Nonce
			}
			s.LastNonce = sample.Nonce
		}
		for _, err := range sample.Errors {
			c.summary.CollectionErrors = append(c.summary.CollectionErrors, sample.EscrowID+": "+err)
		}
		return
	}
	s := c.processes[sample.Target]
	if s == nil {
		s = &ProcessMetricsSummary{Target: sample.Target, Peaks: map[string]float64{}, WorkloadPeaks: map[string]float64{}, Deltas: map[string]float64{}}
		c.processes[sample.Target] = s
	}
	if len(sample.Errors) > 0 {
		s.FailedSamples++
		s.LastErrors = sample.Errors
	}
	if len(sample.Values) == 0 {
		return
	}
	s.Samples++
	if s.First == nil {
		s.First = sample.Values
	}
	previous := c.previous[sample.Target]
	changed := processChanged(previous, sample.Values)
	if changed {
		s.ProcessChanges++
	}
	for name, value := range sample.Values {
		s.Peaks[name] = max(s.Peaks[name], value)
		if sample.Phase == "workload" {
			s.WorkloadPeaks[name] = max(s.WorkloadPeaks[name], value)
		}
	}
	if previous != nil && !changed {
		for _, name := range counterMetricNames {
			value, ok := sample.Values[name]
			prior, exists := previous[name]
			if ok && exists && value >= prior {
				s.Deltas[name] += value - prior
			}
		}
	}
	s.Last = sample.Values
	c.previous[sample.Target] = sample.Values
}

func metricsRunMetadata(dir string, cfg *config.File) map[string]string {
	metadata := map[string]string{"runner_go_version": runtime.Version(), "configured_protocol_version": cfg.Versiond.VersionName, "configured_binary_version": cfg.Versiond.BinaryVersion}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	if body, err := cmd.Output(); err == nil {
		metadata["git_commit"] = strings.TrimSpace(string(body))
	}
	cmd = exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = dir
	if body, err := cmd.Output(); err == nil {
		metadata["git_dirty"] = fmt.Sprint(len(body) > 0)
	}
	path := cfg.Versiond.HostBinaryMount
	if path != "" {
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if file, err := os.Open(path); err == nil {
			hash := sha256.New()
			_, err = io.Copy(hash, file)
			_ = file.Close()
			if err == nil {
				metadata["host_binary_sha256"] = fmt.Sprintf("%x", hash.Sum(nil))
			}
		}
	}
	return metadata
}

func processChanged(a, b map[string]float64) bool {
	// Linux boot wall time can shift after VM clock synchronization. /proc
	// start ticks remain stable, and also distinguish a reused PID.
	_, oldTicks := a["loadtest_process_start_ticks"]
	_, newTicks := b["loadtest_process_start_ticks"]
	names := []string{"process_start_time_seconds", "loadtest_process_pid"}
	if oldTicks && newTicks {
		names = []string{"loadtest_process_start_ticks", "loadtest_process_pid"}
	}
	for _, name := range names {
		x, ok := a[name]
		y, exists := b[name]
		if ok && exists && x != y {
			return true
		}
	}
	return false
}

func (c *metricsCollector) stop() MetricsSummary {
	c.stopOnce.Do(func() {
		c.cancel()
		<-c.done
		// The stack remains up; collect a final snapshot after drain, even on failure.
		c.ctx = context.Background()
		c.phase.Store("final")
		c.sample()
		if err := c.file.Close(); err != nil {
			c.summary.CollectionErrors = append(c.summary.CollectionErrors, err.Error())
		}
		c.summary.FinishedAt = time.Now().UTC()
		for _, s := range c.processes {
			c.summary.Processes = append(c.summary.Processes, *s)
		}
		sort.Slice(c.summary.Processes, func(i, j int) bool { return c.summary.Processes[i].Target < c.summary.Processes[j].Target })
		for _, s := range c.escrows {
			c.summary.Escrows = append(c.summary.Escrows, *s)
		}
		sort.Slice(c.summary.Escrows, func(i, j int) bool { return c.summary.Escrows[i].EscrowID < c.summary.Escrows[j].EscrowID })
		body, err := json.MarshalIndent(c.summary, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(c.outputDir, "metrics-summary.json"), append(body, '\n'), 0o644)
		}
		if err != nil {
			c.summary.CollectionErrors = append(c.summary.CollectionErrors, err.Error())
		}
	})
	return c.summary
}
