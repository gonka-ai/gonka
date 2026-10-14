package loadtest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"devshard/testenv/replay"
)

type GeneratorConfig struct {
	GatewayURL string
	APIKey     string
	Scenario   Scenario
	OutputDir  string
	Client     *http.Client
	Replay     *replay.Selector
}

type RequestResult struct {
	RequestID         string        `json:"request_id"`
	SampleIndex       *int          `json:"sample_index,omitempty"`
	SampleFingerprint string        `json:"sample_fingerprint,omitempty"`
	StartedAt         time.Time     `json:"started_at"`
	Duration          time.Duration `json:"duration"`
	Status            int           `json:"status"`
	Outcome           string        `json:"outcome"`
	Error             string        `json:"error,omitempty"`
	Model             string        `json:"model,omitempty"`
	Stream            bool          `json:"stream,omitempty"`
	MaxTokens         int64         `json:"max_tokens,omitempty"`
	// StreamOutcome classifies a streamed response, one of the stream* values.
	StreamOutcome string `json:"stream_outcome,omitempty"`
}

type Summary struct {
	Scenario                string          `json:"scenario"`
	Seed                    int64           `json:"seed"`
	StartedAt               time.Time       `json:"started_at"`
	Duration                time.Duration   `json:"duration"`
	Offered                 int             `json:"offered_requests"`
	Requests                int             `json:"requests"`
	Completed               int             `json:"completed"`
	Failed                  int             `json:"failed"`
	Dropped                 int             `json:"dropped_by_generator"`
	Behind                  int             `json:"generator_behind,omitempty"`
	ErrorRate               float64         `json:"error_rate"`
	DroppedRate             float64         `json:"dropped_rate"`
	P50                     time.Duration   `json:"p50_latency"`
	P95                     time.Duration   `json:"p95_latency"`
	FailureArtifactsOmitted int             `json:"failure_artifacts_omitted,omitempty"`
	Results                 []RequestResult `json:"-"`
	LatencyWindows          []LatencyWindow `json:"latency_windows"`
	LatencyWindowInterval   string          `json:"latency_window_interval"`
	StreamOutcomes          map[string]int  `json:"stream_outcomes,omitempty"`
}

// Windows group requests by start time; drops are included in offered RPS,
// but excluded from latency. Durations are nanoseconds, as in Summary.
type LatencyWindow struct {
	StartedAt    time.Time     `json:"started_at"`
	Duration     time.Duration `json:"duration"`
	Offered      int           `json:"offered"`
	Completed    int           `json:"completed"`
	Failed       int           `json:"failed"`
	Dropped      int           `json:"dropped"`
	CompletedRPS float64       `json:"completed_rps"`
	P50          time.Duration `json:"p50_latency"`
	P95          time.Duration `json:"p95_latency"`
	P99          time.Duration `json:"p99_latency"`
}

const maxFailureArtifacts = 100

func RunGenerator(ctx context.Context, cfg GeneratorConfig) (Summary, error) {
	if cfg.GatewayURL == "" {
		return Summary{}, fmt.Errorf("gateway URL is required")
	}
	if err := cfg.Scenario.Validate(); err != nil {
		return Summary{}, err
	}
	if cfg.Client == nil {
		cfg.Client = defaultHTTPClient(cfg.Scenario.Workload.MaxInFlight, cfg.Scenario.RequestTimeout())
	}
	if cfg.OutputDir == "" {
		return Summary{}, fmt.Errorf("output directory is required")
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return Summary{}, fmt.Errorf("create output directory: %w", err)
	}

	started := time.Now().UTC()
	workloadDuration := cfg.Scenario.Duration()
	deadline := started.Add(workloadDuration)
	var sequence atomic.Uint64
	results := make([]RequestResult, 0)
	var resultsMu sync.Mutex
	record := func(result RequestResult) {
		resultsMu.Lock()
		results = append(results, result)
		resultsMu.Unlock()
	}
	stopProgress := startWorkloadProgress(ctx, started, workloadDuration)
	if cfg.Scenario.Workload.Traffic.IsRateBased() {
		runRateProfile(ctx, cfg, started, deadline, &sequence, record)
	} else {
		runClosedLoop(ctx, cfg, deadline, &sequence, record)
	}
	stopProgress()

	summary := summarize(cfg.Scenario, started, time.Since(started), results)
	if err := writeResults(cfg.OutputDir, summary); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

const workloadProgressSteps = 10

// startWorkloadProgress logs time-based workload progress independently from
// request throughput. This keeps long or overloaded runs observable even when
// the generator is dropping requests or waiting for slow responses.
func startWorkloadProgress(ctx context.Context, started time.Time, duration time.Duration) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)
		for step := 1; step <= workloadProgressSteps; step++ {
			target := started.Add(duration * time.Duration(step) / workloadProgressSteps)
			timer := time.NewTimer(time.Until(target))
			select {
			case <-ctx.Done():
				stopTimer(timer)
				return
			case <-done:
				stopTimer(timer)
				return
			case <-timer.C:
				elapsed := time.Since(started)
				if elapsed > duration {
					elapsed = duration
				}
				percent := step * 100 / workloadProgressSteps
				log.Printf("loadtest: stage=workload_progress progress=%d%% elapsed=%s duration=%s chart=%s", percent, elapsed.Round(time.Millisecond), duration, workloadProgressBar(percent))
			}
		}
	}()

	return func() {
		close(done)
		<-stopped
	}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func workloadProgressBar(percent int) string {
	const width = 20
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := percent * width / 100
	return "[" + strings.Repeat("#", filled) + strings.Repeat("-", width-filled) + "]"
}

func runClosedLoop(ctx context.Context, cfg GeneratorConfig, deadline time.Time, sequence *atomic.Uint64, record func(RequestResult)) {
	var workers sync.WaitGroup
	for i := 0; i < cfg.Scenario.Workload.MaxInFlight; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for time.Now().Before(deadline) {
				select {
				case <-ctx.Done():
					return
				default:
				}
				record(executeRequest(ctx, cfg, sequence.Add(1)))
			}
		}()
	}
	workers.Wait()
}

func runRateProfile(ctx context.Context, cfg GeneratorConfig, started, deadline time.Time, sequence *atomic.Uint64, record func(RequestResult)) {
	permits := make(chan struct{}, cfg.Scenario.Workload.MaxInFlight)
	var requests sync.WaitGroup
	send := func(time.Time) {
		index := sequence.Add(1)
		select {
		case permits <- struct{}{}:
			requests.Add(1)
			go func() {
				defer requests.Done()
				defer func() { <-permits }()
				record(executeRequest(ctx, cfg, index))
			}()
		default:
			record(droppedRequest(cfg.Scenario, index))
		}
	}
	miss := func(slot time.Time) { record(behindRequest(cfg.Scenario, sequence.Add(1), slot)) }
	paceArrivals(ctx, cfg.Scenario.Workload.Traffic, cfg.Scenario.Duration(), started, deadline, rateClock{now: time.Now, wait: waitForArrival}, send, miss)
	requests.Wait()
}

// rateClock is the time source of the rate loop; tests drive a fake one.
type rateClock struct {
	now  func() time.Time
	wait func(ctx context.Context, at time.Time) bool
}

// maxCatchUp bounds how far behind a late wake-up still sends its slots, so a
// long stall releases at most one second's worth of arrivals at once.
const maxCatchUp = time.Second

// paceArrivals schedules each slot from the previous slot, not from the wake-up,
// so late ticks catch up. Every slot before the deadline goes to send or, when
// it is more than maxCatchUp behind or left at the deadline, to miss.
func paceArrivals(ctx context.Context, profile TrafficProfile, workload time.Duration, started, deadline time.Time, clock rateClock, send, miss func(slot time.Time)) {
	slot := started
	following := func(slot time.Time) time.Time {
		return slot.Add(time.Duration(float64(time.Second) / profile.RPSAt(slot.Sub(started), workload)))
	}
	for slot.Before(deadline) {
		if !clock.wait(ctx, slot) {
			return
		}
		now := clock.now()
		for slot.Before(deadline) && (now.Sub(slot) > maxCatchUp || !now.Before(deadline)) {
			miss(slot)
			slot = following(slot)
		}
		if !slot.Before(deadline) {
			return
		}
		send(slot)
		slot = following(slot)
	}
}

func waitForArrival(ctx context.Context, at time.Time) bool {
	delay := time.Until(at)
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func droppedRequest(scenario Scenario, index uint64) RequestResult {
	return RequestResult{
		RequestID: fmt.Sprintf("%s-%d-%06d", scenario.Scenario, scenario.Seed, index),
		StartedAt: time.Now().UTC(),
		Outcome:   "dropped_by_generator",
		Error:     "max_in_flight reached",
	}
}

// generatorBehind is the Error of a planned arrival the generator never offered
// because it fell behind its schedule; it counts as dropped_by_generator.
const generatorBehind = "generator_behind"

func behindRequest(scenario Scenario, index uint64, slot time.Time) RequestResult {
	return RequestResult{
		RequestID: fmt.Sprintf("%s-%d-%06d", scenario.Scenario, scenario.Seed, index),
		StartedAt: slot.UTC(),
		Outcome:   "dropped_by_generator",
		Error:     generatorBehind,
	}
}

func defaultHTTPClient(concurrency int, timeout time.Duration) *http.Client {
	connections := concurrency * 2
	if connections < 2 {
		connections = 2
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConns:        connections,
			MaxIdleConnsPerHost: connections,
			MaxConnsPerHost:     connections,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

func executeRequest(ctx context.Context, cfg GeneratorConfig, index uint64) (result RequestResult) {
	requestID := fmt.Sprintf("%s-%d-%06d", cfg.Scenario.Scenario, cfg.Scenario.Seed, index)
	started := time.Now().UTC()
	result = RequestResult{RequestID: requestID, StartedAt: started}
	defer func() {
		result.Duration = time.Since(started)
	}()
	spec := sampleRequest(cfg.Scenario, index)
	var messages []Message
	if cfg.Replay != nil {
		sample, sampleIndex, ok := cfg.Replay.Select(index)
		if !ok {
			result.Error = "replay selector has no samples"
			result.Outcome = "error"
			return result
		}
		result.SampleIndex = &sampleIndex
		spec = requestSpec{Model: sample.Model, Stream: sample.Stream, MaxTokens: sample.MaxTokens}
		if sample.Temperature != 0 {
			temperature := sample.Temperature
			spec.Temperature = &temperature
		}
		messages = make([]Message, len(sample.Messages))
		for i, message := range sample.Messages {
			messages[i] = Message{Role: message.Role, Content: message.Content}
		}
		result.SampleFingerprint = replay.Fingerprint(sample.Model, sample.Messages)
	} else {
		messages = append([]Message(nil), spec.Messages...)
		messages[len(messages)-1].Content += " [load-request:" + requestID + "]"
	}
	result.Model, result.Stream, result.MaxTokens = spec.Model, spec.Stream, spec.MaxTokens
	body, err := json.Marshal(struct {
		Model       string    `json:"model"`
		Stream      bool      `json:"stream,omitempty"`
		Temperature *float64  `json:"temperature,omitempty"`
		Logprobs    bool      `json:"logprobs,omitempty"`
		TopLogprobs int       `json:"top_logprobs,omitempty"`
		Messages    []Message `json:"messages"`
		MaxTokens   int64     `json:"max_tokens,omitempty"`
	}{Model: spec.Model, Stream: spec.Stream, Temperature: spec.Temperature, Logprobs: spec.Logprobs, TopLogprobs: spec.TopLogprobs, Messages: messages, MaxTokens: spec.MaxTokens})
	if err != nil {
		result.Error = err.Error()
		result.Outcome = "error"
		return result
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.GatewayURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		result.Error = err.Error()
		result.Outcome = "error"
		return result
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Request-Id", requestID)
	if cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := cfg.Client.Do(httpReq)
	if err != nil {
		result.Error = err.Error()
		result.Outcome = "error"
		return result
	}
	defer resp.Body.Close()
	result.Status = resp.StatusCode
	if spec.Stream && resp.StatusCode == cfg.Scenario.Assertions.Requests.HTTPStatus {
		stream := readSSE(resp.Body)
		result.StreamOutcome = stream.Outcome
		if stream.Outcome != streamCompleted {
			result.Outcome = "error"
			result.Error = stream.Outcome
			if stream.Err != nil {
				result.Error += ": " + stream.Err.Error()
			}
			return result
		}
		result.Outcome = cfg.Scenario.Assertions.Requests.TerminalOutcome
		return result
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		result.Error = err.Error()
		result.Outcome = "error"
		return result
	}
	if resp.StatusCode != cfg.Scenario.Assertions.Requests.HTTPStatus {
		result.Outcome = "error"
		result.Error = string(responseBody)
		return result
	}
	if err := validateGatewayResponse(responseBody); err != nil {
		result.Outcome = "error"
		result.Error = err.Error()
		return result
	}
	result.Outcome = cfg.Scenario.Assertions.Requests.TerminalOutcome
	return result
}

func validateGatewayResponse(body []byte) error {
	var payload struct {
		Choices []json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	if len(payload.Choices) == 0 {
		return fmt.Errorf("response has no choices")
	}
	return nil
}

// Stream outcome classes. Only streamCompleted counts as a successful request.
const (
	streamCompleted     = "completed"
	streamEmpty         = "empty_stream"
	streamPartial       = "partial"
	streamErrorEnvelope = "error_envelope"
)

type sseResult struct {
	Outcome      string
	Chunks       int
	ContentChars int
	Err          error
}

// readSSE consumes a chat-completion stream. completed needs content and
// [DONE]; empty_stream is a clean end with no content; partial is a stream
// cut or garbled before [DONE]; error_envelope is an in-stream error event.
func readSSE(body io.Reader) sseResult {
	var result sseResult
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	sawDone := false
	for !sawDone && scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			sawDone = true
			continue
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Text  string `json:"text"`
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			result.Outcome, result.Err = streamPartial, fmt.Errorf("decode chunk %d: %w", result.Chunks+1, err)
			return result
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			result.Outcome, result.Err = streamErrorEnvelope, fmt.Errorf("%s", chunk.Error)
			return result
		}
		result.Chunks++
		for _, choice := range chunk.Choices {
			result.ContentChars += len(choice.Delta.Content) + len(choice.Text)
		}
	}
	switch {
	case scanner.Err() != nil:
		result.Outcome, result.Err = streamPartial, scanner.Err()
	case !sawDone:
		result.Outcome, result.Err = streamPartial, fmt.Errorf("stream ended before [DONE] after %d chunks", result.Chunks)
	case result.ContentChars == 0:
		result.Outcome, result.Err = streamEmpty, fmt.Errorf("stream finished with no content after %d chunks", result.Chunks)
	default:
		result.Outcome = streamCompleted
	}
	return result
}

// requestSpec is the sampled body of one generated request.
type requestSpec struct {
	Model       string
	Stream      bool
	MaxTokens   int64
	Temperature *float64
	Logprobs    bool
	TopLogprobs int
	Messages    []Message
}

// sampleRequest draws the request for one sequence index. The draw depends
// only on the seed and the index, so a rerun sends the same mix.
func sampleRequest(scenario Scenario, index uint64) requestSpec {
	request := scenario.Workload.Request
	spec := requestSpec{Model: request.Model, Stream: request.Stream, MaxTokens: int64(request.MaxTokens), Messages: request.Messages}
	mix := request.Mix
	if mix == nil {
		return spec
	}
	rng := rand.New(rand.NewPCG(uint64(scenario.Seed), index))
	streamDraw, logprobsDraw := rng.Float64(), rng.Float64()
	if mix.StreamRatio > 0 {
		spec.Stream = streamDraw < mix.StreamRatio
	}
	if logprobsDraw < mix.LogprobsRatio {
		spec.Logprobs = true
		spec.TopLogprobs = mix.TopLogprobs
	}
	if choice, ok := pickWeighted(rng, mix.MaxTokens); ok {
		spec.MaxTokens = int64(choice)
	}
	if choice, ok := pickWeighted(rng, mix.Temperature); ok {
		spec.Temperature = &choice
	}
	if choice, ok := pickWeighted(rng, mix.Prompts); ok {
		spec.Messages = choice
	}
	if choice, ok := pickWeighted(rng, mix.Models); ok {
		spec.Model = choice
	}
	return spec
}

// pickWeighted always consumes one draw so each dimension keeps its place in
// the random sequence whether or not earlier dimensions are configured.
func pickWeighted[T any](rng *rand.Rand, choices []Weighted[T]) (T, bool) {
	draw := rng.Float64()
	var zero T
	total := 0.0
	for _, choice := range choices {
		total += choice.Weight
	}
	if total <= 0 {
		return zero, false
	}
	target := draw * total
	for _, choice := range choices {
		if target < choice.Weight {
			return choice.Value, true
		}
		target -= choice.Weight
	}
	return choices[len(choices)-1].Value, true
}

func summarize(scenario Scenario, started time.Time, duration time.Duration, results []RequestResult) Summary {
	summary := Summary{Scenario: scenario.Scenario, Seed: scenario.Seed, StartedAt: started, Duration: duration, Offered: len(results), Requests: len(results), Results: results}
	latencies := make([]time.Duration, 0, len(results))
	for _, result := range results {
		if result.Outcome != "dropped_by_generator" && result.Duration > 0 {
			latencies = append(latencies, result.Duration)
		}
		if result.Outcome == scenario.Assertions.Requests.TerminalOutcome {
			summary.Completed++
		} else {
			summary.Failed++
		}
		if result.Outcome == "dropped_by_generator" {
			summary.Dropped++
			if result.Error == generatorBehind {
				summary.Behind++
			}
		}
		if result.StreamOutcome != "" {
			if summary.StreamOutcomes == nil {
				summary.StreamOutcomes = map[string]int{}
			}
			summary.StreamOutcomes[result.StreamOutcome]++
		}
	}
	if attempted := summary.Requests - summary.Dropped; attempted > 0 {
		summary.ErrorRate = float64(attempted-summary.Completed) / float64(attempted)
	}
	if summary.Requests > 0 {
		summary.DroppedRate = float64(summary.Dropped) / float64(summary.Requests)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	summary.P50 = percentile(latencies, 0.50)
	summary.P95 = percentile(latencies, 0.95)
	summary.LatencyWindows = latencyWindows(scenario, started, results)
	summary.LatencyWindowInterval = scenario.ReportInterval().String()
	return summary
}

func latencyWindows(scenario Scenario, started time.Time, results []RequestResult) []LatencyWindow {
	interval := scenario.ReportInterval()
	groups := map[int][]RequestResult{}
	for _, result := range results {
		index := max(0, int(result.StartedAt.Sub(started)/interval))
		groups[index] = append(groups[index], result)
	}
	indices := make([]int, 0, len(groups))
	for index := range groups {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	var windows []LatencyWindow
	for _, index := range indices {
		offset := time.Duration(index) * interval
		window := LatencyWindow{StartedAt: started.Add(offset), Duration: max(time.Duration(0), min(interval, scenario.Duration()-offset))}
		var latencies []time.Duration
		for _, result := range groups[index] {
			window.Offered++
			if result.Outcome == "dropped_by_generator" {
				window.Dropped++
			} else {
				if result.Outcome == scenario.Assertions.Requests.TerminalOutcome {
					window.Completed++
				} else {
					window.Failed++
				}
				if result.Duration > 0 {
					latencies = append(latencies, result.Duration)
				}
			}
		}
		if window.Duration > 0 {
			window.CompletedRPS = float64(window.Completed) / window.Duration.Seconds()
		}
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		window.P50 = percentile(latencies, 0.5)
		window.P95 = percentile(latencies, 0.95)
		window.P99 = percentile(latencies, 0.99)
		windows = append(windows, window)
	}
	return windows
}

func percentile(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * p)
	return values[index]
}

func writeResults(outputDir string, summary Summary) error {
	failureCount := 0
	for _, result := range summary.Results {
		if result.Outcome != "completed" {
			failureCount++
		}
	}
	if failureCount > maxFailureArtifacts {
		summary.FailureArtifactsOmitted = failureCount - maxFailureArtifacts
	}

	requestsPath := filepath.Join(outputDir, "requests.jsonl")
	requests, err := os.Create(requestsPath)
	if err != nil {
		return fmt.Errorf("create requests artifact: %w", err)
	}
	encoder := json.NewEncoder(requests)
	for _, result := range summary.Results {
		if err := encoder.Encode(result); err != nil {
			_ = requests.Close()
			return fmt.Errorf("write requests artifact: %w", err)
		}
	}
	if err := requests.Close(); err != nil {
		return fmt.Errorf("close requests artifact: %w", err)
	}
	summaryPath := filepath.Join(outputDir, "summary.json")
	body, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal summary: %w", err)
	}
	if err := os.WriteFile(summaryPath, append(body, '\n'), 0o644); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	failuresDir := filepath.Join(outputDir, "failures")
	failureArtifacts := 0
	for _, result := range summary.Results {
		if result.Outcome == "completed" {
			continue
		}
		if failureArtifacts == maxFailureArtifacts {
			continue
		}
		if err := os.MkdirAll(failuresDir, 0o755); err != nil {
			return fmt.Errorf("create failure artifact directory: %w", err)
		}
		body, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal failure artifact: %w", err)
		}
		if err := os.WriteFile(filepath.Join(failuresDir, result.RequestID+".json"), append(body, '\n'), 0o644); err != nil {
			return fmt.Errorf("write failure artifact: %w", err)
		}
		failureArtifacts++
	}
	if summary.FailureArtifactsOmitted > 0 {
		body := fmt.Sprintf("Individual failure bundles are capped at %d. %d additional outcomes are recorded in requests.jsonl.\n", maxFailureArtifacts, summary.FailureArtifactsOmitted)
		if err := os.WriteFile(filepath.Join(failuresDir, "README.txt"), []byte(body), 0o644); err != nil {
			return fmt.Errorf("write failure artifact note: %w", err)
		}
	}
	return nil
}
