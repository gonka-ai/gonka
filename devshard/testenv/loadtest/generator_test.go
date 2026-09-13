package loadtest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	scenario.Workload.Concurrency = 1
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
			Mode:        "closed_loop",
			Concurrency: 1,
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
