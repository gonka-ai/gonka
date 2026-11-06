package loadtest

import (
	"context"
	"encoding/json"
	"errors"
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

func TestModelBreakdownSeparatesDropsErrorsAndPoolsSuccessfulLatency(t *testing.T) {
	s := mixScenario()
	s.Workload.Duration = "10s"
	results := []RequestResult{}
	for i := 1; i <= 5; i++ {
		results = append(results, RequestResult{Model: "model-a", Status: 200, Outcome: "completed", Duration: time.Duration(i) * time.Millisecond})
	}
	results = append(results,
		RequestResult{Model: "model-b", Status: 200, Outcome: "completed", Duration: 100 * time.Millisecond},
		RequestResult{Model: "model-a", Outcome: "dropped_by_generator", Error: "max_in_flight reached"},
		RequestResult{Model: "model-b", Outcome: "dropped_by_generator", Error: generatorBehind},
		RequestResult{Model: "model-a", Status: 503, Outcome: "error", Duration: time.Hour},
		RequestResult{Model: "model-a", Status: 200, Outcome: "error", ErrorCategory: "timeout", Duration: time.Hour},
		RequestResult{Model: "model-b", Status: 400, Outcome: "error", Duration: time.Hour},
		RequestResult{Model: "model-b", Status: 200, Outcome: "error", ErrorCategory: "invalid_response", Duration: time.Hour},
	)
	b := BuildModelBreakdown(s, results)
	require.Equal(t, []string{"model-a", "model-b"}, []string{b.Models[0].Model, b.Models[1].Model})
	a, c := b.Models[0], b.Models[1]
	require.Equal(t, 8, a.Planned)
	require.Equal(t, 10, b.Total.Errors+b.Total.Completed)
	require.Equal(t, 7, a.Sent)
	require.Equal(t, 5, a.Completed)
	require.Equal(t, 1, a.ConcurrencyDrops)
	require.Equal(t, 2, a.Errors)
	require.Equal(t, map[string]int{"http_503": 1, "timeout": 1}, a.ErrorCategories)
	require.Equal(t, 1, c.BehindDrops)
	require.InDelta(t, 5.0/7, a.SuccessRate, 1e-9)
	require.Equal(t, 3*time.Millisecond, a.P50)
	require.Equal(t, 4*time.Millisecond, a.P95)
	require.Equal(t, 5*time.Millisecond, a.Max)
	require.Equal(t, 3*time.Millisecond, b.Total.P50)
	require.Equal(t, 5*time.Millisecond, b.Total.P95)
	require.Equal(t, 5*time.Millisecond, b.Total.P99)
	require.Equal(t, 100*time.Millisecond, b.Total.Max)
	require.Equal(t, 6, b.Total.LatencySamples)
	require.InDelta(t, 0.6, b.Total.SuccessfulRPS, 1e-9)
	require.Equal(t, b.Total.Errors, len(b.Total.ErrorCategories))
	table := RenderModelBreakdown(b)
	require.Contains(t, table, "| Metric | model-a | model-b | Total |")
	require.Contains(t, table, "| **Errors total** | 2 | 2 | 4 |")
	require.Contains(t, table, "| ↳ HTTP 400 | 0 | 1 | 1 |")
	require.Contains(t, table, "| ↳ HTTP 503 | 1 | 0 | 1 |")
	require.NotContains(t, table, "HTTP 500")
	require.Contains(t, table, "| P50 | 3ms | 100ms | 3ms |")
	raw, err := json.Marshal(b)
	require.NoError(t, err)
	var decoded ModelBreakdown
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, b.Total, decoded.Total)
}

func TestModelBreakdownEmptyDropsOnlyAndZeroErrors(t *testing.T) {
	s := mixScenario()
	b := BuildModelBreakdown(s, nil)
	table := RenderModelBreakdown(b)
	require.Contains(t, table, "| **Errors total** | 0 | 0 | 0 |")
	require.NotContains(t, table, "↳ HTTP")
	require.Contains(t, table, "| P99 | — | — | — |")
	require.Contains(t, table, "NO REQUESTS")
	b = BuildModelBreakdown(s, []RequestResult{{Model: "model-a", Outcome: "dropped_by_generator", Error: "max_in_flight reached"}})
	require.Equal(t, "FAIL", b.Models[0].ClientResult)
	require.Zero(t, b.Total.Errors)
	require.Zero(t, b.Total.Sent)
	require.Contains(t, RenderModelBreakdown(b), "| Success rate of sent requests | — | — | — |")
	// Old records without model IDs are explicit rather than assigned to a random model.
	b = BuildModelBreakdown(s, []RequestResult{{Outcome: "dropped_by_generator", Error: generatorBehind}})
	require.Equal(t, "Unattributed", b.Models[2].Model)
	require.Equal(t, 1, b.Models[2].Dropped)
}

func TestDroppedRequestsRetainTheSameSeededModelAsSentRequests(t *testing.T) {
	s := mixScenario()
	for i := uint64(1); i <= 100; i++ {
		expected := sampleRequest(s, i).Model
		require.Equal(t, expected, droppedRequest(s, i).Model)
		require.Equal(t, expected, behindRequest(s, i, time.Now()).Model)
	}
}

func TestHTTPAndStreamErrorsGetExclusiveCategories(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		stream   bool
		category string
	}{
		{"HTTP 400", 400, `{"error":{"message":"bad request"}}`, false, "http_400"},
		{"HTTP 500", 500, `{"error":{"message":"server error"}}`, false, "http_500"},
		{"HTTP 503", 503, `{"error":{"message":"no live host capacity"}}`, true, "http_503"},
		{"bad JSON", 200, `not json`, false, "invalid_response"},
		{"empty stream", 200, "data: [DONE]\n\n", true, "stream_empty_stream"},
		{"partial stream", 200, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n", true, "stream_partial"},
		{"error envelope", 200, "data: {\"error\":{\"message\":\"overloaded\"}}\n\n", true, "stream_error_envelope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			s := testScenario()
			s.Workload.Request.Stream = tc.stream
			r := executeRequest(context.Background(), GeneratorConfig{GatewayURL: server.URL, Scenario: s, Client: server.Client()}, 1)
			require.Equal(t, "error", r.Outcome)
			require.Equal(t, tc.category, r.ErrorCategory)
			b := BuildModelBreakdown(s, []RequestResult{r})
			require.Equal(t, 1, b.Total.Errors)
			require.Equal(t, map[string]int{tc.category: 1}, b.Total.ErrorCategories)
		})
	}
	require.Equal(t, "timeout", transportErrorCategory(fmt.Errorf("wrapped: %w", context.DeadlineExceeded)))
	require.Equal(t, "cancelled", transportErrorCategory(context.Canceled))
	require.Equal(t, "network", transportErrorCategory(errors.New("connection reset")))
}

func TestGeneratorSavesPerModelDropAttributionWithReplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dataset.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"model":"model-a","messages":[{"role":"user","content":"a"}],"response":{"role":"assistant","content":"a"}}
{"model":"model-b","messages":[{"role":"user","content":"b"}],"response":{"role":"assistant","content":"b"}}`), 0o600))
	dataset, err := replay.LoadFile(path)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	s := mixScenario()
	s.Workload.MaxInFlight = 1
	s.Workload.Duration = "80ms"
	s.Workload.Traffic = TrafficProfile{Type: "constant", RPS: 200}
	summary, err := RunGenerator(context.Background(), GeneratorConfig{GatewayURL: server.URL, Scenario: s, OutputDir: filepath.Join(dir, "output"), Replay: replay.NewSelector(dataset, s.Seed)})
	require.NoError(t, err)
	require.Positive(t, summary.Dropped)
	total := summary.ModelBreakdown.Total
	require.Equal(t, summary.Requests, total.Planned)
	require.Equal(t, summary.Dropped, total.Dropped)
	require.Zero(t, total.Errors)
	for _, r := range summary.Results {
		require.Contains(t, []string{"model-a", "model-b"}, r.Model)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "output", "summary.json"))
	require.NoError(t, err)
	require.True(t, strings.Contains(string(raw), `"model_breakdown"`))
}
