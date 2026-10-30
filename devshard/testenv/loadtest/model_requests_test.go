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

	"devshard/testenv/config"
	"devshard/testenv/replay"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func modelScenario(t *testing.T, requestYAML string) Scenario {
	t.Helper()
	s := testScenario()
	s.Workload.Request = Request{}
	require.NoError(t, yaml.Unmarshal([]byte(requestYAML), &s.Workload.Request))
	return s
}

func TestModelRequestsInheritAndOverrideCommonSettings(t *testing.T) {
	s := modelScenario(t, `
messages: [{role: user, content: common}]
max_tokens: 128
temperature: 0.7
stream: true
logprobs: true
top_logprobs: 5
mix:
  models:
    - {value: model-a, weight: 70}
    - value: model-b
      weight: 30
      messages: [{role: user, content: model-b-only}]
      max_tokens: 1024
      temperature: 0
      stream: false
      logprobs: false
      top_logprobs: 0
`)
	require.NoError(t, s.Validate())
	counts := map[string]int{}
	for i := uint64(1); i <= 10000; i++ {
		spec := sampleRequest(s, i)
		require.Equal(t, spec, sampleRequest(s, i))
		counts[spec.Model]++
		if spec.Model == "model-a" {
			require.Equal(t, "common", spec.Messages[0].Content)
			require.EqualValues(t, 128, spec.MaxTokens)
			require.Equal(t, 0.7, *spec.Temperature)
			require.True(t, spec.Stream)
			require.True(t, spec.Logprobs)
			require.Equal(t, 5, spec.TopLogprobs)
		} else {
			require.Equal(t, "model-b", spec.Model)
			require.Equal(t, "model-b-only", spec.Messages[0].Content)
			require.EqualValues(t, 1024, spec.MaxTokens)
			require.Zero(t, *spec.Temperature)
			require.False(t, spec.Stream)
			require.False(t, spec.Logprobs)
			require.Zero(t, spec.TopLogprobs)
		}
	}
	require.InDelta(t, 7000, counts["model-a"], 200)
	require.Equal(t, []string{"model-a", "model-b"}, s.RequestModels())
	require.Equal(t, "common", s.Workload.Request.Messages[0].Content)
}

func TestModelRequestsLocalMixHasHighestPriority(t *testing.T) {
	s := modelScenario(t, `
stream: true
mix:
  max_tokens: [{value: 999, weight: 1}]
  models:
    - value: model-a
      weight: 1
      max_tokens: 777
      logprobs: true
      mix:
        stream_ratio: 0
        logprobs_ratio: 0
        top_logprobs: 0
        max_tokens: [{value: 32, weight: 1}, {value: 64, weight: 1}]
        temperature: [{value: 0, weight: 1}]
        prompts:
          - {value: [{role: user, content: a-one}], weight: 1}
          - {value: [{role: user, content: a-two}], weight: 1}
    - value: model-b
      weight: 1
      messages: [{role: user, content: b-only}]
      max_tokens: 1024
`)
	require.NoError(t, s.Validate())
	seen := map[string]bool{}
	for i := uint64(1); i <= 1000; i++ {
		spec := sampleRequest(s, i)
		if spec.Model == "model-a" {
			require.Contains(t, []int64{32, 64}, spec.MaxTokens)
			require.Contains(t, []string{"a-one", "a-two"}, spec.Messages[0].Content)
			seen[fmt.Sprintf("%d/%s", spec.MaxTokens, spec.Messages[0].Content)] = true
			require.Zero(t, *spec.Temperature)
			require.False(t, spec.Stream)
			require.False(t, spec.Logprobs)
			require.Zero(t, spec.TopLogprobs)
		} else {
			require.Equal(t, "b-only", spec.Messages[0].Content)
			require.EqualValues(t, 1024, spec.MaxTokens)
			require.True(t, spec.Stream)
		}
	}
	require.Len(t, seen, 4)
}

func TestModelRequestsValidation(t *testing.T) {
	for _, tc := range []struct{ name, request, want string }{
		{"missing prompt", `mix: {models: [{value: a, weight: 1}]}`, "messages are required"},
		{"duplicate", `mix: {models: [{value: a, weight: 1}, {value: a, weight: 1}]}`, "mix.models[1]"},
		{"weight", `mix: {models: [{value: a, weight: .nan}]}`, "mix.models[0]"},
		{"nested models", `mix: {models: [{value: a, weight: 1, mix: {models: [{value: b, weight: 1}]}}]}`, "nested models"},
		{"local ratio", `mix: {models: [{value: a, weight: 1, mix: {stream_ratio: 2}}]}`, "stream_ratio"},
		{"tokens", `mix: {models: [{value: a, weight: 1, max_tokens: 0}]}`, "max_tokens"},
		{"temperature", `mix: {models: [{value: a, weight: 1, temperature: 3}]}`, "temperature"},
		{"messages", `mix: {models: [{value: a, weight: 1, messages: []}]}`, "messages"},
		{"local prompt", `mix: {models: [{value: a, weight: 1, mix: {prompts: [{value: [], weight: 1}]}}]}`, "mix.prompts[0]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := modelScenario(t, tc.request)
			require.ErrorContains(t, s.Validate(), tc.want)
		})
	}
	s := modelScenario(t, `model: a`)
	require.NoError(t, s.validate(true))
	require.Error(t, s.Validate())
}

func TestDatasetMissingModelFailsBeforeStackStartup(t *testing.T) {
	s := modelScenario(t, `mix: {models: [{value: a, weight: 70}, {value: b, weight: 30}]}`)
	dir := t.TempDir()
	scenarioPath := filepath.Join(dir, "scenario.yaml")
	body, err := yaml.Marshal(s)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(scenarioPath, body, 0o600))
	datasetPath := filepath.Join(dir, "dataset.jsonl")
	require.NoError(t, os.WriteFile(datasetPath, []byte(`{"model":"a","messages":[{"role":"user","content":"a"}],"response":{"role":"assistant","content":"answer"}}`), 0o600))
	output := filepath.Join(dir, "results")
	_, err = RunScenario(context.Background(), RunnerConfig{ScenarioPath: scenarioPath, TestenvDir: filepath.Join(dir, "no-stack"), OutputDir: output, LoadDataset: datasetPath})
	require.ErrorContains(t, err, `dataset has no samples for configured model "b"`)
	_, err = os.Stat(output)
	require.True(t, os.IsNotExist(err))
}

func TestDatasetUsesYAMLWeightsAndPreservesRows(t *testing.T) {
	s := modelScenario(t, `
messages: [{role: user, content: yaml-must-not-be-sent}]
max_tokens: 999
stream: true
temperature: 1
mix:
  models:
    - {value: a, weight: 70, max_tokens: 888}
    - {value: b, weight: 30, temperature: 2}
`)
	var lines []string
	// Deliberately make the dataset distribution unlike the YAML distribution.
	for i := 0; i < 101; i++ {
		model := "b"
		if i == 0 {
			model = "a"
		}
		lines = append(lines, fmt.Sprintf(`{"model":%q,"stream":false,"temperature":0,"logprobs":true,"top_logprobs":3,"max_tokens":%d,"messages":[{"role":"user","content":%q}],"response":{"role":"assistant","content":"answer"}}`, model, i+1, fmt.Sprintf("row-%d", i)))
	}
	lines = append(lines, `{"model":"extra","messages":[{"role":"user","content":"extra"}],"response":{"role":"assistant","content":"extra"}}`)
	path := filepath.Join(t.TempDir(), "dataset.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600))
	dataset, err := replay.LoadFile(path)
	require.NoError(t, err)
	require.NoError(t, validateDatasetModels(s, dataset))
	selector := replay.NewSelector(dataset, s.Seed)
	counts := map[string]int{}
	selectedRows := map[int]bool{}
	for i := uint64(1); i <= 10000; i++ {
		model := sampleRequest(s, i).Model
		row, index, ok := selector.SelectModel(model, i)
		require.True(t, ok)
		require.Equal(t, model, row.Model)
		counts[row.Model]++
		selectedRows[index] = true
	}
	require.InDelta(t, 7000, counts["a"], 200)
	require.Len(t, selectedRows, 101)
	require.Zero(t, counts["extra"])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model       string    `json:"model"`
			Stream      bool      `json:"stream"`
			Temperature *float64  `json:"temperature"`
			MaxTokens   int       `json:"max_tokens"`
			Logprobs    bool      `json:"logprobs"`
			TopLogprobs int       `json:"top_logprobs"`
			Messages    []Message `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Contains(t, []string{"a", "b"}, body.Model)
		require.False(t, body.Stream)
		require.NotNil(t, body.Temperature)
		require.Zero(t, *body.Temperature)
		require.True(t, body.Logprobs)
		require.Equal(t, 3, body.TopLogprobs)
		row := 0
		_, err := fmt.Sscanf(body.Messages[0].Content, "row-%d", &row)
		require.NoError(t, err)
		require.Equal(t, row+1, body.MaxTokens)
		require.Equal(t, body.Model == "a", row == 0)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()
	for i := uint64(1); i <= 50; i++ {
		result := executeRequest(context.Background(), GeneratorConfig{GatewayURL: server.URL, Scenario: s, Replay: selector, Client: server.Client()}, i)
		require.Equal(t, "completed", result.Outcome, result.Error)
		require.NotNil(t, result.SampleIndex)
	}
}

func TestRunnerConfigRegistersYAMLModels(t *testing.T) {
	for _, models := range []string{
		`model: custom-model`,
		`mix: {models: [{value: a, weight: 70}, {value: b, weight: 30}]}`,
	} {
		s := modelScenario(t, "messages: [{role: user, content: common}]\n"+models)
		workDir := t.TempDir()
		profiles := map[string]Profile{"fast": {TTFT: "1ms", TokenInterval: "1ms", Workers: 1}}
		require.NoError(t, writeRunnerConfig(t.TempDir(), workDir, s, profiles, false, nil, nil))
		cfg, err := config.Load(filepath.Join(workDir, "config.yaml"))
		require.NoError(t, err)
		var escrowModels, groupModels []string
		for _, escrow := range cfg.Escrows {
			escrowModels = append(escrowModels, escrow.ModelID)
		}
		for _, group := range cfg.EpochGroups {
			groupModels = append(groupModels, group.ModelID)
		}
		require.Equal(t, s.RequestModels(), escrowModels)
		require.Equal(t, s.RequestModels(), groupModels)
	}
}

func TestModelRequestHTTPBodiesStayBoundToTheirModel(t *testing.T) {
	s := modelScenario(t, `
max_tokens: 999
stream: true
logprobs: true
top_logprobs: 5
mix:
  models:
    - value: a
      weight: 1
      messages: [{role: user, content: prompt-a}]
      max_tokens: 32
      temperature: 0
      stream: false
    - value: b
      weight: 1
      logprobs: false
      stream: false
      mix:
        max_tokens: [{value: 64, weight: 1}]
        prompts: [{value: [{role: user, content: prompt-b}], weight: 1}]
`)
	require.NoError(t, s.Validate())
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		model := body["model"].(string)
		seen[model] = true
		message := body["messages"].([]any)[0].(map[string]any)["content"].(string)
		require.True(t, strings.HasPrefix(message, "prompt-"+model+" [load-request:"))
		require.NotContains(t, body, "stream")
		if model == "a" {
			require.Equal(t, 32.0, body["max_tokens"])
			require.Equal(t, 0.0, body["temperature"])
			require.Equal(t, true, body["logprobs"])
			require.Equal(t, 5.0, body["top_logprobs"])
		} else {
			require.Equal(t, "b", model)
			require.Equal(t, 64.0, body["max_tokens"])
			require.NotContains(t, body, "top_logprobs")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()
	for i := uint64(1); i <= 30; i++ {
		result := executeRequest(context.Background(), GeneratorConfig{GatewayURL: server.URL, Client: server.Client(), Scenario: s}, i)
		require.Equal(t, "completed", result.Outcome, result.Error)
	}
	require.Len(t, seen, 2)
}
