package loadtest

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
	"sync"
	"sync/atomic"
	"time"
)

type GeneratorConfig struct {
	GatewayURL string
	APIKey     string
	Scenario   Scenario
	OutputDir  string
	Client     *http.Client
}

type RequestResult struct {
	RequestID string        `json:"request_id"`
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration"`
	Status    int           `json:"status"`
	Outcome   string        `json:"outcome"`
	Error     string        `json:"error,omitempty"`
}

type Summary struct {
	Scenario                string          `json:"scenario"`
	Seed                    int64           `json:"seed"`
	StartedAt               time.Time       `json:"started_at"`
	Duration                time.Duration   `json:"duration"`
	Requests                int             `json:"requests"`
	Completed               int             `json:"completed"`
	Failed                  int             `json:"failed"`
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
		cfg.Client = defaultHTTPClient(cfg.Scenario.Workload.Concurrency)
	}
	if cfg.OutputDir == "" {
		return Summary{}, fmt.Errorf("output directory is required")
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return Summary{}, fmt.Errorf("create output directory: %w", err)
	}

	started := time.Now().UTC()
	deadline := started.Add(cfg.Scenario.Duration())
	var sequence atomic.Uint64
	results := make([]RequestResult, 0)
	var resultsMu sync.Mutex
	var workers sync.WaitGroup

	for i := 0; i < cfg.Scenario.Workload.Concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for time.Now().Before(deadline) {
				select {
				case <-ctx.Done():
					return
				default:
				}
				index := sequence.Add(1)
				result := executeRequest(ctx, cfg, index)
				resultsMu.Lock()
				results = append(results, result)
				resultsMu.Unlock()
			}
		}()
	}
	workers.Wait()

	summary := summarize(cfg.Scenario, started, time.Since(started), results)
	if err := writeResults(cfg.OutputDir, summary); err != nil {
		return Summary{}, err
	}
	return summary, nil
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

func executeRequest(ctx context.Context, cfg GeneratorConfig, index uint64) RequestResult {
	requestID := fmt.Sprintf("%s-%d-%06d", cfg.Scenario.Scenario, cfg.Scenario.Seed, index)
	started := time.Now().UTC()
	result := RequestResult{RequestID: requestID, StartedAt: started}
	request := cfg.Scenario.Workload.Request
	messages := append([]Message(nil), request.Messages...)
	messages[len(messages)-1].Content += " [load-request:" + requestID + "]"
	body, err := json.Marshal(struct {
		Model     string    `json:"model"`
		Messages  []Message `json:"messages"`
		MaxTokens int       `json:"max_tokens,omitempty"`
	}{Model: request.Model, Messages: messages, MaxTokens: request.MaxTokens})
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
	result.Duration = time.Since(started)
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
	var payload struct {
		Choices []json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil || len(payload.Choices) == 0 {
		result.Outcome = "error"
		if err != nil {
			result.Error = err.Error()
		} else {
			result.Error = "response has no choices"
		}
		return result
	}
	result.Outcome = cfg.Scenario.Assertions.Requests.TerminalOutcome
	return result
}

func summarize(scenario Scenario, started time.Time, duration time.Duration, results []RequestResult) Summary {
	summary := Summary{Scenario: scenario.Scenario, Seed: scenario.Seed, StartedAt: started, Duration: duration, Requests: len(results), Results: results}
	latencies := make([]time.Duration, 0, len(results))
	for _, result := range results {
		latencies = append(latencies, result.Duration)
		if result.Outcome == scenario.Assertions.Requests.TerminalOutcome {
			summary.Completed++
		} else {
			summary.Failed++
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
