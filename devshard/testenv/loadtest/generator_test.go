package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devshard/testenv/replay"

	"github.com/stretchr/testify/require"
)

func TestRunGenerator_WritesRequestArtifacts(t *testing.T) {
	var requestIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		requestIDs = append(requestIDs, r.Header.Get("X-Request-Id"))
		var body struct {
			Messages []Message `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "user", body.Messages[0].Role)
		require.Contains(t, body.Messages[0].Content, "[load-request:")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]string{{"text": "ok"}}})
	}))
	defer server.Close()

	outputDir := t.TempDir()
	scenario := testScenario()
	scenario.Workload.MaxInFlight = 1
	scenario.Workload.Duration = "20ms"
	summary, err := RunGenerator(context.Background(), GeneratorConfig{
		GatewayURL: server.URL,
		Scenario:   scenario,
		OutputDir:  outputDir,
	})
	require.NoError(t, err)
	require.NotZero(t, summary.Requests)
	require.Equal(t, summary.Requests, summary.Completed)
	require.FileExists(t, filepath.Join(outputDir, "requests.jsonl"))
	require.FileExists(t, filepath.Join(outputDir, "summary.json"))
	require.NotEmpty(t, requestIDs)
	require.True(t, strings.HasPrefix(requestIDs[0], "normal-load-42-"))

	body, err := os.ReadFile(filepath.Join(outputDir, "requests.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, strings.TrimSpace(string(body)))
}

func TestRunGenerator_RateProfileRecordsDroppedRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]string{{"text": "ok"}}})
	}))
	defer server.Close()

	scenario := testScenario()
	scenario.Workload.Duration = "60ms"
	scenario.Workload.MaxInFlight = 1
	scenario.Workload.Traffic = TrafficProfile{Type: "constant", RPS: 100}
	summary, err := RunGenerator(context.Background(), GeneratorConfig{
		GatewayURL: server.URL,
		Scenario:   scenario,
		OutputDir:  t.TempDir(),
	})
	require.NoError(t, err)
	require.Equal(t, 1, summary.Completed)
	require.Greater(t, summary.Dropped, 0)
	require.Equal(t, summary.Offered, summary.Requests)
}

func TestRunGenerator_ReplaysRandomSamplesWithoutMutatingPrompts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "samples.jsonl")
	body := `{"model":"replay-model","stream":false,"temperature":0.7,"max_tokens":64,"messages":[{"role":"user","content":"captured one"}],"response":{"role":"assistant","content":"answer one"}}
{"model":"replay-model","stream":false,"temperature":0.7,"max_tokens":64,"messages":[{"role":"user","content":"captured two"}],"response":{"role":"assistant","content":"answer two"}}
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	dataset, err := replay.LoadFile(path)
	require.NoError(t, err)

	var prompts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model    string    `json:"model"`
			Messages []Message `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, "replay-model", request.Model)
		require.Len(t, request.Messages, 1)
		require.NotContains(t, request.Messages[0].Content, "[load-request:")
		prompts = append(prompts, request.Messages[0].Content)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]string{{"text": "ok"}}})
	}))
	defer server.Close()

	scenario := testScenario()
	scenario.Workload.Duration = "30ms"
	summary, err := RunGenerator(context.Background(), GeneratorConfig{
		GatewayURL: server.URL,
		Scenario:   scenario,
		OutputDir:  t.TempDir(),
		Replay:     replay.NewSelector(dataset, scenario.Seed),
	})
	require.NoError(t, err)
	require.NotEmpty(t, prompts)
	for _, result := range summary.Results {
		require.NotNil(t, result.SampleIndex)
		require.NotEmpty(t, result.SampleFingerprint)
	}
}

func TestExecuteRequestMeasuresStreamingBodyUntilDone(t *testing.T) {
	const bodyDelay = 50 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)
		_, err := fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		require.NoError(t, err)
		flusher.Flush()

		time.Sleep(bodyDelay)
		_, err = fmt.Fprint(w, "data: [DONE]\n\n")
		require.NoError(t, err)
		flusher.Flush()
	}))
	defer server.Close()

	scenario := testScenario()
	scenario.Workload.Request.Stream = true
	result := executeRequest(context.Background(), GeneratorConfig{
		GatewayURL: server.URL,
		Scenario:   scenario,
		Client:     &http.Client{},
	}, 1)

	require.Equal(t, "completed", result.Outcome)
	require.GreaterOrEqual(t, result.Duration, bodyDelay)
}

func TestWorkloadProgressBar(t *testing.T) {
	require.Equal(t, "[--------------------]", workloadProgressBar(0))
	require.Equal(t, "[##------------------]", workloadProgressBar(10))
	require.Equal(t, "[##########----------]", workloadProgressBar(50))
	require.Equal(t, "[####################]", workloadProgressBar(100))
	require.Equal(t, "[####################]", workloadProgressBar(150))
}

func testScenario() Scenario {
	scenario := Scenario{
		SchemaVersion: "v1",
		Scenario:      "normal-load",
		Seed:          42,
		Topology: Topology{
			VersiondMode: "multi",
			Storage:      "per_participant",
			Chain:        ChainTopology{EscrowAmount: 1_000_000_000, MaxNonce: 100_000},
			MockML: MockMLTopology{Allocator: "round_robin", Nodes: []MockMLNode{
				{Name: "mock-openai-0", Profile: "fast"},
				{Name: "mock-openai-1", Profile: "fast"},
			}},
		},
		Workload: Workload{
			MaxInFlight: 1,
			Duration:    "1s",
			Request: Request{
				Model:    "test-model",
				Messages: []Message{{Role: "user", Content: "hello"}},
			},
		},
		Assertions:   Assertions{},
		DrainTimeout: "1s",
	}
	scenario.Assertions.Requests.HTTPStatus = http.StatusOK
	scenario.Assertions.Requests.TerminalOutcome = "completed"
	return scenario
}
