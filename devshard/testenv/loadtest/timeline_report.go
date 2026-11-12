package loadtest

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const TimelineReportFileName = "timeline.html"

//go:embed timeline.html.tmpl
var timelineHTML string

type timelineTraffic struct {
	X         float64 `json:"x"`
	OK        float64 `json:"ok"`
	Scheduled float64 `json:"scheduled"`
	Drop      float64 `json:"drop"`
	Error     float64 `json:"error"`
}

type timelineMemoryPoint struct {
	X    float64  `json:"x"`
	RSS  *float64 `json:"rss"`
	Heap *float64 `json:"heap"`
}

type timelineSeries struct {
	Key    string                `json:"key"`
	Label  string                `json:"label"`
	Points []timelineMemoryPoint `json:"points"`
}

type timelineEvent struct {
	From           float64  `json:"from"`
	To             float64  `json:"to"`
	Title          string   `json:"title"`
	Label          string   `json:"label"`
	Nodes          []string `json:"nodes,omitempty"`
	PreservedNodes []string `json:"preservedNodes,omitempty"`
}

type timelineData struct {
	Scenario   string            `json:"scenario"`
	Run        string            `json:"run"`
	Started    string            `json:"started"`
	End        float64           `json:"end"`
	TrafficBin float64           `json:"trafficBin"`
	MemoryBin  float64           `json:"memoryBin"`
	Traffic    []timelineTraffic `json:"traffic"`
	Series     []timelineSeries  `json:"series"`
	Events     []timelineEvent   `json:"events"`
	Notes      []string          `json:"notes"`
}

// WriteTimelineReport builds an offline HTML report from saved run artifacts.
// It never queries the stack and embeds its data, styles and chart code.
func WriteTimelineReport(outputDir string) (string, error) {
	data, err := loadTimelineData(outputDir)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(data) // escapes </script>, U+2028 and U+2029
	if err != nil {
		return "", fmt.Errorf("encode timeline: %w", err)
	}
	t, err := template.New("timeline").Parse(timelineHTML)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	// Only JSON produced above is trusted as script data; labels remain data.
	err = t.Execute(&out, struct {
		Title string
		Data  template.JS
	}{data.Scenario, template.JS(encoded)})
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(outputDir, TimelineReportFileName))
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func loadTimelineData(dir string) (timelineData, error) {
	var summary Summary
	if err := readReportArtifact(dir, "summary.json", true, &summary); err != nil {
		return timelineData{}, err
	}
	if summary.StartedAt.IsZero() {
		return timelineData{}, fmt.Errorf("summary.json has no workload start time")
	}
	duration := summary.Duration
	// Saved dataset scenarios may omit messages supplied by replay rows.
	// Reporting needs only workload timing, not request validation.
	var timing struct {
		Workload struct {
			Duration string `yaml:"duration"`
		} `yaml:"workload"`
	}
	if err := loadYAML(filepath.Join(dir, "run.yaml"), &timing); err == nil {
		if configured, err := time.ParseDuration(timing.Workload.Duration); err == nil && configured > 0 {
			duration = configured
			if summary.Duration > 0 {
				duration = min(duration, summary.Duration)
			}
		}
	}
	if duration <= 0 {
		return timelineData{}, fmt.Errorf("timeline requires a positive run duration")
	}
	// Bound the embedded series size even for multi-hour runs.
	bin := time.Duration(math.Max(10, math.Ceil(duration.Seconds()/1200))) * time.Second
	memoryBin := time.Duration(math.Max(5, math.Ceil(summary.Duration.Seconds()/1200))) * time.Second
	out := timelineData{Scenario: summary.Scenario, Run: filepath.Base(filepath.Clean(dir)), Started: summary.StartedAt.UTC().Format(time.RFC3339), End: duration.Minutes(), TrafficBin: bin.Seconds(), MemoryBin: memoryBin.Seconds(), Traffic: []timelineTraffic{}, Series: []timelineSeries{}, Events: []timelineEvent{}, Notes: []string{}}
	type trafficCount struct{ scheduled, completed, dropped, failed int }
	counts := map[int]*trafficCount{}
	err := timelineReadJSONL[RequestResult](filepath.Join(dir, "requests.jsonl"), func(r RequestResult) {
		offset := r.StartedAt.Sub(summary.StartedAt)
		if offset < 0 || offset >= duration {
			return
		}
		index := int(offset / bin)
		if counts[index] == nil {
			counts[index] = &trafficCount{}
		}
		b := counts[index]
		b.scheduled++
		switch r.Outcome {
		case "completed":
			b.completed++
		case "dropped_by_generator":
			b.dropped++
		case "error":
			b.failed++
		}
	})
	if err != nil && !os.IsNotExist(err) {
		return out, fmt.Errorf("requests.jsonl: %w", err)
	}
	if os.IsNotExist(err) {
		out.Notes = append(out.Notes, "requests.jsonl is missing; traffic uses the saved summary windows.")
		for _, w := range summary.LatencyWindows {
			if w.Duration <= 0 {
				continue
			}
			sent := w.Offered - w.Dropped
			out.Traffic = append(out.Traffic, timelineTraffic{X: w.StartedAt.Sub(summary.StartedAt).Minutes() + w.Duration.Minutes()/2, OK: float64(w.Completed) / w.Duration.Seconds(), Scheduled: float64(w.Offered) / w.Duration.Seconds(), Drop: timelinePercent(w.Dropped, w.Offered), Error: timelinePercent(w.Failed, sent)})
		}
	} else {
		for offset := time.Duration(0); offset < duration; offset += bin {
			width := min(bin, duration-offset)
			b := counts[int(offset/bin)]
			if b == nil {
				b = &trafficCount{}
			}
			out.Traffic = append(out.Traffic, timelineTraffic{X: offset.Minutes() + width.Minutes()/2, OK: float64(b.completed) / width.Seconds(), Scheduled: float64(b.scheduled) / width.Seconds(), Drop: timelinePercent(b.dropped, b.scheduled), Error: timelinePercent(b.failed, b.scheduled-b.dropped)})
		}
	}
	type memoryBucket struct {
		times     []float64
		rss, heap []float64
	}
	memory := map[string]map[int]*memoryBucket{}
	err = timelineReadJSONL[MetricsSample](filepath.Join(dir, "metrics.jsonl"), func(r MetricsSample) {
		if r.Target != "gateway" && !strings.HasSuffix(r.Target, "/devshardd") {
			return
		}
		offset := r.At.Sub(summary.StartedAt)
		// The collector takes a synchronous baseline before workload starts.
		if offset < -time.Minute {
			return
		}
		if offset < 0 {
			offset = 0
		}
		rss, hasRSS := r.Values["process_resident_memory_bytes"]
		heap, hasHeap := r.Values["go_memstats_heap_alloc_bytes"]
		if !hasRSS && !hasHeap {
			return
		}
		if memory[r.Target] == nil {
			memory[r.Target] = map[int]*memoryBucket{}
		}
		index := int(offset / memoryBin)
		if memory[r.Target][index] == nil {
			memory[r.Target][index] = &memoryBucket{}
		}
		b := memory[r.Target][index]
		b.times = append(b.times, offset.Minutes())
		if hasRSS {
			b.rss = append(b.rss, rss/(1<<20))
		}
		if hasHeap {
			b.heap = append(b.heap, heap/(1<<20))
		}
		out.End = max(out.End, offset.Minutes())
	})
	if err != nil && !os.IsNotExist(err) {
		return out, fmt.Errorf("metrics.jsonl: %w", err)
	}
	if len(memory) == 0 {
		out.Notes = append(out.Notes, "No process memory samples are available; memory charts are empty.")
	}
	keys := make([]string, 0, len(memory))
	for key := range memory {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i] == "gateway" {
			return true
		}
		if keys[j] == "gateway" {
			return false
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys {
		s := timelineSeries{Key: key, Label: strings.TrimSuffix(key, "/devshardd"), Points: []timelineMemoryPoint{}}
		indices := make([]int, 0, len(memory[key]))
		for index := range memory[key] {
			indices = append(indices, index)
		}
		sort.Ints(indices)
		for _, index := range indices {
			b := memory[key][index]
			s.Points = append(s.Points, timelineMemoryPoint{X: *timelineMedian(b.times), RSS: timelineMedian(b.rss), Heap: timelineMedian(b.heap)})
		}
		out.Series = append(out.Series, s)
	}
	var events []EventRecord
	if err := readReportArtifact(dir, "events.json", false, &events); err != nil {
		return out, err
	}
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i].PlannedStart, events[j].PlannedStart
		if events[i].StartedAt != nil {
			a = *events[i].StartedAt
		}
		if events[j].StartedAt != nil {
			b = *events[j].StartedAt
		}
		return a.Before(b)
	})
	var hostSamples []DapiHostSample
	if err := timelineReadJSONL[DapiHostSample](filepath.Join(dir, "dapi-host-stats.jsonl"), func(sample DapiHostSample) {
		if sample.Error == "" {
			hostSamples = append(hostSamples, sample)
		}
	}); err != nil && !os.IsNotExist(err) {
		out.Notes = append(out.Notes, "Host-state samples could not be read; PoC node annotations may be unavailable.")
	}
	numbers := map[string]int{}
	for _, event := range events {
		if event.StartedAt == nil {
			continue
		}
		end := summary.StartedAt.Add(time.Duration(out.End * float64(time.Minute)))
		if event.EndedAt != nil {
			end = *event.EndedAt
		}
		title := event.ID
		if title == "" {
			title = event.Action
		}
		label := title
		var nodes, preserved []string
		if event.Action == "poc_window" {
			kind := "PoC"
			if event.Params["kind"] == "cpoc" {
				kind = "CPoC"
			}
			numbers[kind]++
			title = fmt.Sprintf("%s %d", kind, numbers[kind])
			nodes, preserved = timelinePoCNodes(hostSamples, *event.StartedAt, end)
			label = title
			if len(preserved) > 0 {
				label += " · preserved: " + strings.Join(preserved, ", ")
			} else if host, ok := event.Params["preserved"].(string); ok {
				label += " · preserved: " + host
			}
			if len(nodes) > 0 {
				label += " · PoC nodes (observed): " + strings.Join(nodes, ", ")
			}
		}
		from, to := event.StartedAt.Sub(summary.StartedAt).Minutes(), end.Sub(summary.StartedAt).Minutes()
		if to < from {
			to = from
		}
		out.Events = append(out.Events, timelineEvent{From: from, To: to, Title: title, Label: label, Nodes: nodes, PreservedNodes: preserved})
		out.End = max(out.End, to)
	}
	return out, nil
}

// Use a recorded state well inside the applied window; do not infer affected
// identities from service numbering or confuse a preserved HA replica with PoC.
func timelinePoCNodes(samples []DapiHostSample, from, to time.Time) (nodes, preserved []string) {
	midpoint := from.Add(to.Sub(from) / 2)
	var best *DapiHostSample
	for i := range samples {
		sample := &samples[i]
		if !sample.At.After(from.Add(time.Second)) || !sample.At.Before(to.Add(-time.Second)) || len(sample.Hosts) == 0 {
			continue
		}
		if best == nil || sample.At.Sub(midpoint).Abs() < best.At.Sub(midpoint).Abs() {
			best = sample
		}
	}
	if best == nil {
		return nil, nil
	}
	for host, state := range best.Hosts {
		if host == dapiUnmappedHost || len(state.Nodes) == 0 {
			continue
		}
		if state.Challenged {
			nodes = append(nodes, host)
		} else {
			preserved = append(preserved, host)
		}
	}
	sort.Strings(nodes)
	sort.Strings(preserved)
	return nodes, preserved
}

func timelinePercent(n, d int) float64 {
	if d <= 0 {
		return 0
	}
	return 100 * float64(max(0, n)) / float64(d)
}
func timelineMedian(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	sort.Float64s(values)
	i := len(values) / 2
	v := values[i]
	if len(values)%2 == 0 {
		v = (values[i-1] + v) / 2
	}
	return &v
}

func timelineReadJSONL[T any](path string, visit func(T)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	for {
		var row T
		if err = decoder.Decode(&row); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		visit(row)
	}
}
