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
	"sync"
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

func TestRemoteDatasetUsesTargetModelAndHTTPStatusOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"model":"captured-model","stream":true,"temperature":0.7,"max_tokens":4096,"messages":[{"role":"user","content":"original prompt"}]}`), 0o600))
	dataset, err := replay.LoadRequestsFile(path)
	require.NoError(t, err)
	_, err = replay.LoadFile(path)
	require.ErrorContains(t, err, "response.role")
	for _, status := range []int{200, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model       string    `json:"model"`
					Messages    []Message `json:"messages"`
					Stream      bool      `json:"stream"`
					MaxTokens   int       `json:"max_tokens"`
					Temperature float64   `json:"temperature"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Equal(t, "target-model", body.Model)
				require.Equal(t, []Message{{Role: "user", Content: "original prompt"}}, body.Messages)
				require.True(t, body.Stream)
				require.Equal(t, 4096, body.MaxTokens)
				require.Equal(t, 0.7, body.Temperature)
				w.WriteHeader(status)
				fmt.Fprint(w, "arbitrary body without JSON or SSE")
			}))
			defer server.Close()
			scenario := testScenario()
			cfg := GeneratorConfig{GatewayURL: server.URL, Client: server.Client(), Scenario: scenario, Replay: replay.NewSelector(dataset, 42), ReplayModel: "target-model", HTTPStatusOnly: true}
			result := executeRequest(context.Background(), cfg, 1)
			require.Equal(t, status, result.Status)
			require.NotNil(t, result.SampleIndex)
			if status == 200 {
				require.Equal(t, "completed", result.Outcome)
				cfg.HTTPStatusOnly = false
				require.Equal(t, "error", executeRequest(context.Background(), cfg, 1).Outcome)
			} else {
				require.Equal(t, "error", result.Outcome)
			}
		})
	}
}

func TestHTTPStatusOnlyRecordsTruncatedBodyWithoutFailingStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		fmt.Fprint(w, "short body")
	}))
	defer server.Close()
	cfg := GeneratorConfig{GatewayURL: server.URL, Client: server.Client(), Scenario: testScenario(), HTTPStatusOnly: true}
	result := executeRequest(context.Background(), cfg, 1)
	require.Equal(t, 200, result.Status)
	require.Equal(t, "completed", result.Outcome)
	require.NotEmpty(t, result.ResponseReadError)
	cfg.HTTPStatusOnly = false
	require.Equal(t, "error", executeRequest(context.Background(), cfg, 1).Outcome)
}

func TestGeneratorHonorsScenarioRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(100 * time.Millisecond):
			fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
		}
	}))
	defer server.Close()
	for _, timeout := range []string{"10ms", "1s"} {
		t.Run(timeout, func(t *testing.T) {
			scenario := testScenario()
			scenario.Workload.Duration = "1ms"
			scenario.Workload.MaxInFlight = 1
			scenario.Workload.RequestTimeout = timeout
			summary, err := RunGenerator(context.Background(), GeneratorConfig{GatewayURL: server.URL, Scenario: scenario, OutputDir: t.TempDir()})
			require.NoError(t, err)
			require.Equal(t, 1, summary.Requests)
			if timeout == "10ms" {
				require.Equal(t, 1, summary.Failed)
			} else {
				require.Equal(t, 1, summary.Completed)
			}
		})
	}
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

// Test flow:
// 1. Summarize two completed requests, one gateway error and one generator drop.
// 2. Require the error rate to count only the requests that reached the gateway.
// 3. Require the drop rate to count the drop against everything offered.
func TestSummarizeKeepsGeneratorDropsOutOfTheErrorRate(t *testing.T) {
	scenario := testScenario()
	results := []RequestResult{
		{Outcome: scenario.Assertions.Requests.TerminalOutcome},
		{Outcome: scenario.Assertions.Requests.TerminalOutcome},
		{Outcome: "error", Status: 502},
		{Outcome: "dropped_by_generator"},
	}

	summary := summarize(scenario, time.Now(), time.Second, results)

	require.InDelta(t, 1.0/3, summary.ErrorRate, 1e-9)
	require.InDelta(t, 0.25, summary.DroppedRate, 1e-9)
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

// Test flow:
// 1. Feed readSSE the streams recorded from mock-openai (plus the empty-stream shape) and a few hand-cut streams.
// 2. Check each lands in its outcome class with the expected chunk and content counts.
func TestReadSSEOutcomeClasses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		outcome string
		errText string
	}{
		{name: "completed.sse", outcome: streamCompleted},
		{name: "empty_stream.sse", outcome: streamEmpty, errText: "no content after 2 chunks"},
		{name: "partial.sse", outcome: streamPartial, errText: "before [DONE]"},
		{name: "error_envelope.sse", outcome: streamErrorEnvelope, errText: "upstream overloaded"},
		{name: "engine_error.sse", outcome: streamErrorEnvelope, errText: "EngineCore"},
		{name: "done only", body: "data: [DONE]\n\n", outcome: streamEmpty},
		{name: "garbled chunk", body: "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: {\"choi\n\n", outcome: streamPartial, errText: "decode chunk 2"},
		{name: "legacy text choice", body: "data: {\"choices\":[{\"text\":\"ok\"}]}\n\ndata: [DONE]\n\n", outcome: streamCompleted},
		{name: "null error field", body: "data: {\"error\":null,\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: [DONE]\n\n", outcome: streamCompleted},
		{name: "empty body", body: "", outcome: streamPartial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if strings.HasSuffix(tc.name, ".sse") {
				raw, err := os.ReadFile(filepath.Join("testdata", "sse", tc.name))
				require.NoError(t, err)
				body = string(raw)
			}
			result := readSSE(strings.NewReader(body))
			require.Equal(t, tc.outcome, result.Outcome)
			if tc.outcome == streamCompleted {
				require.NoError(t, result.Err)
				require.Positive(t, result.ContentChars)
			}
			if tc.errText != "" {
				require.ErrorContains(t, result.Err, tc.errText)
			}
		})
	}
}

func mixScenario() Scenario {
	scenario := testScenario()
	scenario.Workload.Request.Mix = &RequestMix{
		StreamRatio:   0.5,
		LogprobsRatio: 0.5,
		TopLogprobs:   5,
		MaxTokens:     []Weighted[int]{{Value: 32, Weight: 40}, {Value: 128, Weight: 35}, {Value: 512, Weight: 20}, {Value: 2048, Weight: 5}},
		Temperature:   []Weighted[float64]{{Value: 0, Weight: 50}, {Value: 0.7, Weight: 50}},
		Prompts: []Weighted[[]Message]{
			{Value: []Message{{Role: "user", Content: "short"}}, Weight: 1},
			{Value: []Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "long"}}, Weight: 1},
		},
		Models: []Weighted[string]{{Value: "model-a", Weight: 70}, {Value: "model-b", Weight: 30}},
	}
	return scenario
}

// Test flow:
// 1. Sample 20000 requests from the prod-like mix twice with one seed, and once with another seed.
// 2. Check the same seed gives the same sequence and another seed a different one.
// 3. Check each dimension's frequencies match its weights within 2 points, and that no mix returns the fixed request.
func TestSampleRequestMixIsSeededAndWeighted(t *testing.T) {
	scenario := mixScenario()
	require.NoError(t, scenario.Validate())
	const samples = 20000
	first := make([]requestSpec, samples)
	counts := map[string]float64{}
	for i := range first {
		spec := sampleRequest(scenario, uint64(i+1))
		first[i] = spec
		require.Equal(t, spec, sampleRequest(scenario, uint64(i+1)))
		counts[fmt.Sprintf("max_tokens=%d", spec.MaxTokens)]++
		counts[fmt.Sprintf("temperature=%g", *spec.Temperature)]++
		counts["model="+spec.Model]++
		counts[fmt.Sprintf("prompt=%d", len(spec.Messages))]++
		if spec.Stream {
			counts["stream"]++
		}
		if spec.Logprobs {
			counts["logprobs"]++
			require.Equal(t, 5, spec.TopLogprobs)
		}
	}
	for key, want := range map[string]float64{
		"max_tokens=32": 0.40, "max_tokens=128": 0.35, "max_tokens=512": 0.20, "max_tokens=2048": 0.05,
		"temperature=0": 0.5, "temperature=0.7": 0.5, "model=model-a": 0.7, "model=model-b": 0.3,
		"prompt=1": 0.5, "prompt=2": 0.5, "stream": 0.5, "logprobs": 0.5,
	} {
		require.InDelta(t, want, counts[key]/samples, 0.02, key)
	}
	other := scenario
	other.Seed = 7
	differs := 0
	for i := range first {
		if spec := sampleRequest(other, uint64(i+1)); spec.Model != first[i].Model || spec.MaxTokens != first[i].MaxTokens {
			differs++
		}
	}
	require.Greater(t, differs, samples/4)
	fixed := sampleRequest(testScenario(), 1)
	require.Equal(t, requestSpec{Model: "test-model", Messages: testScenario().Workload.Request.Messages}, fixed)
}

// Test flow:
// 1. Run the generator with an all-stream mix against a server that replays the recorded streams in turn.
// 2. Check request bodies carry the sampled fields, including an explicit temperature 0.
// 3. Check each request's stream outcome, that only completed streams count as completed, and the summary counts.
func TestRunGenerator_StreamOutcomes(t *testing.T) {
	files := []string{"completed.sse", "empty_stream.sse", "partial.sse", "error_envelope.sse"}
	var mu sync.Mutex
	served := 0
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		mu.Lock()
		file := files[served%len(files)]
		served++
		bodies = append(bodies, body)
		mu.Unlock()
		raw, err := os.ReadFile(filepath.Join("testdata", "sse", file))
		require.NoError(t, err)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	scenario := mixScenario()
	scenario.Workload.Request.Mix.StreamRatio = 1
	scenario.Workload.Request.Mix.Temperature = []Weighted[float64]{{Value: 0, Weight: 1}}
	scenario.Workload.Request.Mix.LogprobsRatio = 1
	scenario.Workload.Traffic = TrafficProfile{Type: "constant", RPS: 400}
	scenario.Workload.Duration = "50ms"
	summary, err := RunGenerator(context.Background(), GeneratorConfig{GatewayURL: server.URL, Scenario: scenario, OutputDir: t.TempDir()})
	require.NoError(t, err)
	require.GreaterOrEqual(t, served, 4)
	for _, body := range bodies {
		require.Equal(t, true, body["stream"])
		require.Contains(t, body, "temperature")
		require.Equal(t, 0.0, body["temperature"])
		require.Equal(t, true, body["logprobs"])
		require.Equal(t, 5.0, body["top_logprobs"])
		require.Contains(t, []any{"model-a", "model-b"}, body["model"])
	}
	total := 0
	for _, result := range summary.Results {
		if result.Outcome == "dropped_by_generator" {
			continue
		}
		total++
		require.True(t, result.Stream)
		require.Contains(t, []string{streamCompleted, streamEmpty, streamPartial, streamErrorEnvelope}, result.StreamOutcome)
		require.Equal(t, result.StreamOutcome == streamCompleted, result.Outcome == "completed", result.StreamOutcome)
	}
	require.Equal(t, served, total)
	sum := 0
	for _, count := range summary.StreamOutcomes {
		sum += count
	}
	require.Equal(t, total, sum)
	require.Positive(t, summary.StreamOutcomes[streamEmpty])
	require.Positive(t, summary.StreamOutcomes[streamErrorEnvelope])
}

type stallingClock struct {
	now      time.Time
	stallAt  time.Time
	stall    time.Duration
	stalled  bool
	sentAt   []time.Time
	sentSlot []time.Time
	missed   []time.Time
}

func (c *stallingClock) rateClock() rateClock {
	return rateClock{
		now: func() time.Time { return c.now },
		wait: func(_ context.Context, at time.Time) bool {
			if at.After(c.now) {
				c.now = at
			}
			if !c.stalled && !c.now.Before(c.stallAt) {
				c.now, c.stalled = c.now.Add(c.stall), true
			}
			return true
		},
	}
}

func (c *stallingClock) run(rps float64, duration time.Duration) {
	started := c.now
	profile := TrafficProfile{Type: "constant", RPS: rps}
	send := func(slot time.Time) { c.sentAt, c.sentSlot = append(c.sentAt, c.now), append(c.sentSlot, slot) }
	miss := func(slot time.Time) { c.missed = append(c.missed, slot) }
	paceArrivals(context.Background(), profile, duration, started, started.Add(duration), c.rateClock(), send, miss)
}

// Test flow:
// 1. Pace a 10 s constant 10 rps profile on a fake clock whose wake-up at 2 s stalls for 3 s.
// 2. Require every planned slot to be either sent or counted as missed, 100 in total.
// 3. Require the slots more than one second behind, 2.0 s to 3.9 s, to be the missed ones.
// 4. Require the catch-up burst at the wake-up to stay within one second's worth of slots.
func TestPaceArrivalsCatchesUpAStalledTickWithinOneSecond(t *testing.T) {
	clock := &stallingClock{now: fixtureT0, stallAt: fixtureT0.Add(2 * time.Second), stall: 3 * time.Second}

	clock.run(10, 10*time.Second)

	require.Len(t, clock.sentSlot, 80)
	require.Len(t, clock.missed, 20)
	require.Equal(t, fixtureT0.Add(2*time.Second), clock.missed[0])
	require.Equal(t, fixtureT0.Add(3900*time.Millisecond), clock.missed[19])
	burst := 0
	for _, sent := range clock.sentAt {
		if sent.Equal(fixtureT0.Add(5 * time.Second)) {
			burst++
		}
	}
	require.Equal(t, 11, burst)
	for i, slot := range clock.sentSlot {
		require.False(t, slot.After(clock.sentAt[i]))
		require.LessOrEqual(t, clock.sentAt[i].Sub(slot), time.Second)
	}
}

// Test flow:
// 1. Pace a 10 s constant 10 rps profile on a fake clock that stalls for 2 s at 9.5 s, past the deadline.
// 2. Require the five slots from 9.5 s to the deadline to be counted as missed and nothing sent after the deadline.
func TestPaceArrivalsCountsSlotsLeftAtTheDeadline(t *testing.T) {
	clock := &stallingClock{now: fixtureT0, stallAt: fixtureT0.Add(9500 * time.Millisecond), stall: 2 * time.Second}

	clock.run(10, 10*time.Second)

	require.Len(t, clock.sentSlot, 95)
	require.Len(t, clock.missed, 5)
	require.Equal(t, fixtureT0.Add(9500*time.Millisecond), clock.missed[0])
	require.True(t, clock.sentAt[len(clock.sentAt)-1].Before(fixtureT0.Add(10*time.Second)))
}

// Test flow:
// 1. Summarize one completed request, one max_in_flight drop and two slots the generator fell behind on.
// 2. Require all three unsent slots in the drop count and rate, and the two behind slots counted on their own.
func TestSummarizeCountsGeneratorBehindAsNeverSent(t *testing.T) {
	scenario := testScenario()
	results := []RequestResult{
		{Outcome: scenario.Assertions.Requests.TerminalOutcome},
		{Outcome: "dropped_by_generator", Error: "max_in_flight reached"},
		behindRequest(scenario, 3, fixtureT0),
		behindRequest(scenario, 4, fixtureT0),
	}

	summary := summarize(scenario, fixtureT0, time.Second, results)

	require.Equal(t, 3, summary.Dropped)
	require.Equal(t, 2, summary.Behind)
	require.InDelta(t, 0.75, summary.DroppedRate, 1e-9)
	require.InDelta(t, 0.0, summary.ErrorRate, 1e-9)
}
