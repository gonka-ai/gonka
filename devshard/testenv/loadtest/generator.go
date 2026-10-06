package loadtest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	ErrorRate               float64         `json:"error_rate"`
	P50                     time.Duration   `json:"p50_latency"`
	P95                     time.Duration   `json:"p95_latency"`
	FailureArtifactsOmitted int             `json:"failure_artifacts_omitted,omitempty"`
	Results                 []RequestResult `json:"-"`
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
		cfg.Client = defaultHTTPClient(cfg.Scenario.Workload.MaxInFlight)
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
	profile := cfg.Scenario.Workload.Traffic
	permits := make(chan struct{}, cfg.Scenario.Workload.MaxInFlight)
	var requests sync.WaitGroup
	nextArrival := started

	for {
		if !waitForArrival(ctx, nextArrival) || !time.Now().Before(deadline) {
			break
		}
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

		rate := profile.RPSAt(time.Since(started), cfg.Scenario.Duration())
		nextArrival = time.Now().Add(time.Duration(float64(time.Second) / rate))
	}
	requests.Wait()
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

func defaultHTTPClient(concurrency int) *http.Client {
	connections := concurrency * 2
	if connections < 2 {
		connections = 2
	}
	return &http.Client{
		Timeout: 30 * time.Second,
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
	request := cfg.Scenario.Workload.Request
	stream := request.Stream
	var messages []Message
	model := request.Model
	maxTokens := int64(request.MaxTokens)
	var temperature float64
	if cfg.Replay != nil {
		sample, sampleIndex, ok := cfg.Replay.Select(index)
		if !ok {
			result.Error = "replay selector has no samples"
			result.Outcome = "error"
			return result
		}
		result.SampleIndex = &sampleIndex
		model = sample.Model
		stream = sample.Stream
		temperature = sample.Temperature
		maxTokens = sample.MaxTokens
		messages = make([]Message, len(sample.Messages))
		for i, message := range sample.Messages {
			messages[i] = Message{Role: message.Role, Content: message.Content}
		}
		result.SampleFingerprint = replay.Fingerprint(sample.Model, sample.Messages)
	} else {
		messages = append([]Message(nil), request.Messages...)
		messages[len(messages)-1].Content += " [load-request:" + requestID + "]"
	}
	body, err := json.Marshal(struct {
		Model       string    `json:"model"`
		Stream      bool      `json:"stream,omitempty"`
		Temperature float64   `json:"temperature,omitempty"`
		Messages    []Message `json:"messages"`
		MaxTokens   int64     `json:"max_tokens,omitempty"`
	}{Model: model, Stream: stream, Temperature: temperature, Messages: messages, MaxTokens: maxTokens})
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
	if err := validateGatewayResponse(responseBody, stream); err != nil {
		result.Outcome = "error"
		result.Error = err.Error()
		return result
	}
	result.Outcome = cfg.Scenario.Assertions.Requests.TerminalOutcome
	return result
}

func validateGatewayResponse(body []byte, stream bool) error {
	if !stream {
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

	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	sawChoice := false
	sawDone := false
	for scanner.Scan() {
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
			Choices []json.RawMessage `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decode streaming response: %w", err)
		}
		if len(chunk.Choices) > 0 {
			sawChoice = true
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !sawChoice {
		return fmt.Errorf("streaming response has no choices")
	}
	if !sawDone {
		return fmt.Errorf("streaming response has no [DONE]")
	}
	return nil
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
		}
	}
	if summary.Requests > 0 {
		summary.ErrorRate = float64(summary.Failed) / float64(summary.Requests)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	summary.P50 = percentile(latencies, 0.50)
	summary.P95 = percentile(latencies, 0.95)
	return summary
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
