package loadtest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	gatewayCPUMetric      = "process_cpu_seconds_total"
	gatewayAttemptsMetric = "devshard_gateway_attempts_started_total"
	hostChatMetric        = "devshard_request_terminal_total"
)

// ladderReport is the per-stage table of a run whose traffic has several
// stages; it is empty for any other run.
type ladderReport struct {
	steps        []ladderStep
	errorBound   float64
	droppedBound float64
	notes        []string
}

// ladderStep is one traffic stage. Requests belong to the stage their start
// falls in; latency is over completed requests only.
type ladderStep struct {
	RPS                float64
	From, To           time.Duration
	Offered            int
	Sent               int
	Completed          int
	Dropped            int
	Behind             int
	P50, P95           time.Duration
	Classes            []tokenClassLatency
	GatewayCores       float64
	HasCPU             bool
	AttemptsPerRequest float64
	HasAttempts        bool
	HostChats          []hostChatRate
	Note               string
}

// tokenClassLatency is the p95 of a step's completed requests that asked for
// one max_tokens value; 0 is a request without max_tokens.
type tokenClassLatency struct {
	MaxTokens int64
	Completed int
	P95       time.Duration
}

// hostChatRate is the increase of a devshardd's request terminals, one per
// Chat it ended, refusals included, per second of the stage.
type hostChatRate struct {
	Host      string
	PerSecond float64
}

// loadLadder reads run.yaml, requests.jsonl and metrics.jsonl of a run. A
// missing or unreadable artifact leaves its columns empty and adds a note.
func loadLadder(outputDir string, summary Summary) ladderReport {
	var scenario Scenario
	if err := loadYAML(filepath.Join(outputDir, "run.yaml"), &scenario); err != nil {
		return ladderReport{}
	}
	traffic := scenario.Workload.Traffic
	if traffic.ResolvedType() != "stages" || len(traffic.Stages) < 2 {
		return ladderReport{}
	}
	requests := scenario.Assertions.Requests
	ladder := ladderReport{errorBound: requests.ErrorRate, droppedBound: requests.ErrorRate}
	if requests.DroppedRate != nil {
		ladder.droppedBound = *requests.DroppedRate
	}
	var offset time.Duration
	for _, stage := range traffic.Stages {
		duration, _ := time.ParseDuration(stage.Duration)
		ladder.steps = append(ladder.steps, ladderStep{RPS: traffic.stageRPS(stage), From: offset, To: offset + duration})
		offset += duration
	}
	started := summary.StartedAt
	if results, err := readLadderRequests(filepath.Join(outputDir, "requests.jsonl")); err != nil {
		ladder.notes = append(ladder.notes, "requests.jsonl: not readable ("+err.Error()+")")
	} else {
		ladder.countRequests(results, started, requests.TerminalOutcome)
	}
	if samples, err := readLadderMetrics(filepath.Join(outputDir, "metrics.jsonl")); err != nil {
		ladder.notes = append(ladder.notes, "metrics.jsonl: not readable, so no gateway CPU, attempts or host Chat rate ("+err.Error()+")")
	} else {
		ladder.notes = append(ladder.notes, ladder.addMetrics(samples, started)...)
	}
	if index, reason := degradationStart(ladder.steps, ladder.errorBound, ladder.droppedBound); index >= 0 {
		ladder.steps[index].Note = "degradation starts here: " + reason
	}
	return ladder
}

func readLadderRequests(path string) ([]RequestResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var results []RequestResult
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for scanner.Scan() {
		var result RequestResult
		if err := json.Unmarshal(scanner.Bytes(), &result); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, scanner.Err()
}

func readLadderMetrics(path string) ([]MetricsSample, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var samples []MetricsSample
	decoder := json.NewDecoder(file)
	for decoder.More() {
		var sample MetricsSample
		if err := decoder.Decode(&sample); err != nil {
			return nil, err
		}
		samples = append(samples, sample)
	}
	return samples, nil
}

func (l *ladderReport) stepAt(offset time.Duration) int {
	for i, step := range l.steps {
		if offset >= step.From && offset < step.To {
			return i
		}
	}
	return -1
}

func (l *ladderReport) countRequests(results []RequestResult, started time.Time, completedOutcome string) {
	latencies := make([][]time.Duration, len(l.steps))
	classes := make([]map[int64][]time.Duration, len(l.steps))
	for _, result := range results {
		i := l.stepAt(result.StartedAt.Sub(started))
		if i < 0 {
			continue
		}
		step := &l.steps[i]
		step.Offered++
		switch result.Outcome {
		case "dropped_by_generator":
			step.Dropped++
			if result.Error == generatorBehind {
				step.Behind++
			}
			continue
		case completedOutcome:
			step.Completed++
			latencies[i] = append(latencies[i], result.Duration)
			if classes[i] == nil {
				classes[i] = map[int64][]time.Duration{}
			}
			classes[i][result.MaxTokens] = append(classes[i][result.MaxTokens], result.Duration)
		}
		step.Sent++
	}
	for i := range l.steps {
		sort.Slice(latencies[i], func(a, b int) bool { return latencies[i][a] < latencies[i][b] })
		l.steps[i].P50, l.steps[i].P95 = percentile(latencies[i], 0.50), percentile(latencies[i], 0.95)
		for maxTokens, values := range classes[i] {
			sort.Slice(values, func(a, b int) bool { return values[a] < values[b] })
			l.steps[i].Classes = append(l.steps[i].Classes, tokenClassLatency{MaxTokens: maxTokens, Completed: len(values), P95: percentile(values, 0.95)})
		}
		sort.Slice(l.steps[i].Classes, func(a, b int) bool { return l.steps[i].Classes[a].MaxTokens < l.steps[i].Classes[b].MaxTokens })
	}
}

// minClassCompletions: a p95 over fewer completions than 1/(1-0.95) sits on
// the class's slowest request, so a class smaller than this is not compared.
const minClassCompletions = 20

// latencyDegraded compares p95 within each max_tokens class both steps have
// minClassCompletions of, so a shift in the token mix is not read as load. With
// no such class it compares the steps' overall p95.
func latencyDegraded(base, step ladderStep) (string, bool) {
	compared := false
	for _, class := range step.Classes {
		i := slices.IndexFunc(base.Classes, func(c tokenClassLatency) bool { return c.MaxTokens == class.MaxTokens })
		if i < 0 || base.Classes[i].Completed < minClassCompletions || class.Completed < minClassCompletions || base.Classes[i].P95 <= 0 {
			continue
		}
		compared = true
		if ref := base.Classes[i].P95; class.P95 > 2*ref {
			if class.MaxTokens == 0 {
				return fmt.Sprintf("p95 %s > 2x step 1's %s", class.P95, ref), true
			}
			return fmt.Sprintf("max_tokens %d p95 %s > 2x step 1's %s", class.MaxTokens, class.P95, ref), true
		}
	}
	if !compared && base.P95 > 0 && step.P95 > 2*base.P95 {
		return fmt.Sprintf("p95 %s > 2x step 1's %s", step.P95, base.P95), true
	}
	return "", false
}

// counterDelta is a counter's increase over the samples inside a stage and
// the time they span; a drop (a process restart) counts the new value as new work.
func counterDelta(points []metricPoint, from, to time.Time) (increase, seconds float64, ok bool) {
	var inside []metricPoint
	for _, point := range points {
		if !point.at.Before(from) && !point.at.After(to) {
			inside = append(inside, point)
		}
	}
	if len(inside) < 2 {
		return 0, 0, false
	}
	for i := 1; i < len(inside); i++ {
		increase += counterIncrease(inside[i-1].value, inside[i].value)
	}
	seconds = inside[len(inside)-1].at.Sub(inside[0].at).Seconds()
	return increase, seconds, seconds > 0
}

type metricPoint struct {
	at    time.Time
	value float64
}

// familySum adds every series of one metric family in a sample's Domain map.
func familySum(domain map[string]float64, family string) (float64, bool) {
	sum, found := 0.0, false
	for key, value := range domain {
		if key == family || strings.HasPrefix(key, family+"{") {
			sum += value
			found = true
		}
	}
	return sum, found
}

func (l *ladderReport) addMetrics(samples []MetricsSample, started time.Time) []string {
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].At.Before(samples[j].At) })
	var cpu, attempts []metricPoint
	hosts := map[string][]metricPoint{}
	for _, sample := range samples {
		switch {
		case sample.Target == "gateway":
			if value, ok := sample.Values[gatewayCPUMetric]; ok {
				cpu = append(cpu, metricPoint{sample.At, value})
			}
			if value, ok := familySum(sample.Domain, gatewayAttemptsMetric); ok {
				attempts = append(attempts, metricPoint{sample.At, value})
			}
		case strings.HasSuffix(sample.Target, "/devshardd"):
			if value, ok := familySum(sample.Domain, hostChatMetric); ok {
				host := strings.TrimSuffix(sample.Target, "/devshardd")
				hosts[host] = append(hosts[host], metricPoint{sample.At, value})
			}
		}
	}
	names := make([]string, 0, len(hosts))
	for host := range hosts {
		names = append(names, host)
	}
	sort.Strings(names)
	for i := range l.steps {
		step := &l.steps[i]
		from, to := started.Add(step.From), started.Add(step.To)
		if increase, seconds, ok := counterDelta(cpu, from, to); ok {
			step.GatewayCores, step.HasCPU = increase/seconds, true
		}
		if increase, _, ok := counterDelta(attempts, from, to); ok && step.Sent > 0 {
			step.AttemptsPerRequest, step.HasAttempts = increase/float64(step.Sent), true
		}
		for _, host := range names {
			if increase, seconds, ok := counterDelta(hosts[host], from, to); ok {
				step.HostChats = append(step.HostChats, hostChatRate{Host: host, PerSecond: increase / seconds})
			}
		}
	}
	var notes []string
	if len(cpu) == 0 {
		notes = append(notes, "gateway CPU: metrics.jsonl has no "+gatewayCPUMetric+" for the gateway")
	}
	if len(attempts) == 0 {
		notes = append(notes, "attempts per request: metrics.jsonl has no "+gatewayAttemptsMetric+" for the gateway")
	}
	if len(hosts) == 0 {
		notes = append(notes, "host Chat rate: metrics.jsonl has no "+hostChatMetric+" for any devshardd")
	}
	return notes
}

// degradationStart is the first step whose p95 within a max_tokens class is
// above twice step 1's, or whose error or never-sent share is above its bound;
// -1 when none is.
func degradationStart(steps []ladderStep, errorBound, droppedBound float64) (int, string) {
	for i, step := range steps {
		if i > 0 {
			if reason, ok := latencyDegraded(steps[0], step); ok {
				return i, reason
			}
		}
		if step.Sent > 0 {
			if share := float64(step.Sent-step.Completed) / float64(step.Sent); share > errorBound {
				return i, fmt.Sprintf("errors %.2f%% > %.2f%%", share*100, errorBound*100)
			}
		}
		if step.Offered > 0 {
			if share := float64(step.Dropped) / float64(step.Offered); share > droppedBound {
				return i, fmt.Sprintf("never sent %.2f%% > %.2f%%", share*100, droppedBound*100)
			}
		}
	}
	return -1, ""
}

func (r runReport) renderLadder(out *strings.Builder) {
	if len(r.ladder.steps) == 0 {
		return
	}
	fmt.Fprintf(out, "\n### Load steps\n\nOne row per traffic stage, requests counted by start time. Latency is over completed requests. Gateway CPU is the increase of `%s` per second in metrics.jsonl; attempts are `%s` per sent request; host Chats are each devshardd's `%s` per second, refusals included. Offered is what the generator released against rps x the stage's length; never sent counts max_in_flight drops and the planned arrivals it fell behind on. Degradation starts at the first step whose p95 within one max_tokens class (%d or more completions in both steps; the overall p95 when no class has that many) is above 2x step 1's, or with errors above %.2f%% or never sent above %.2f%%.\n\n", gatewayCPUMetric, gatewayAttemptsMetric, hostChatMetric, minClassCompletions, r.ladder.errorBound*100, r.ladder.droppedBound*100)
	out.WriteString("| step | rps | window | offered / planned | sent | errors | never sent | p50 | p95 | gateway CPU (cores) | attempts / request | host Chats/s | note |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for i, step := range r.ladder.steps {
		cores, attempts := reportMissing, reportMissing
		if step.HasCPU {
			cores = fmt.Sprintf("%.2f", step.GatewayCores)
		}
		if step.HasAttempts {
			attempts = fmt.Sprintf("%.2f", step.AttemptsPerRequest)
		}
		hosts := make([]string, 0, len(step.HostChats))
		for _, host := range step.HostChats {
			hosts = append(hosts, fmt.Sprintf("%s %.1f", host.Host, host.PerSecond))
		}
		chats := strings.Join(hosts, ", ")
		if chats == "" {
			chats = reportMissing
		}
		planned := int(math.Round(step.RPS * (step.To - step.From).Seconds()))
		writeReportRow(out, fmt.Sprint(i+1), fmt.Sprint(step.RPS), fmt.Sprintf("%s–%s", step.From, step.To), shareOf(step.Offered-step.Behind, planned), fmt.Sprint(step.Sent), shareOf(step.Sent-step.Completed, step.Sent), shareOf(step.Dropped, step.Offered), step.P50.String(), step.P95.String(), cores, attempts, chats, step.Note)
	}
	for _, note := range r.ladder.notes {
		fmt.Fprintf(out, "\n%s\n", note)
	}
}
