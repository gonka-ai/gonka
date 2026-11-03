package main

import (
	"bytes"
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

const scenarioHeader = `schema_version: v1
scenario: remote-fixture-test
seed: 1515
topology:
  versiond_mode: multi
  storage: per_participant
  participants: 3
  chain: {escrow_amount: 1000000, max_nonce: 100000}
  mock_ml:
    allocator: round_robin
    nodes:
      - {name: mock-openai-0, profile: fast}
      - {name: mock-openai-1, profile: fast}
workload:
  duration: 60ms
  max_in_flight: 1
  traffic: {type: closed_loop}
  request:
    mix:
      models:
        - {value: a, weight: 50}
        - {value: b, weight: 50}
assertions:
  requests: {http_status: 200, terminal_outcome: completed, error_rate: 0}
drain_timeout: 1s
`

func TestRemoteDatasetKeepsBothModelsAndIgnoresSingleModelEnvironment(t *testing.T) {
	dir := t.TempDir()
	scenario := filepath.Join(dir, "scenario.yaml")
	dataset := filepath.Join(dir, "dataset.jsonl")
	output := filepath.Join(dir, "output")
	require.NoError(t, os.WriteFile(scenario, []byte(scenarioHeader), 0o600))
	require.NoError(t, os.WriteFile(dataset, []byte(`{"model":"a","temperature":0,"max_tokens":32,"messages":[{"role":"user","content":"a-prompt"}],"response":{"role":"assistant","content":"unused fixture"}}
{"model":"b","temperature":0.7,"max_tokens":64,"messages":[{"role":"user","content":"b-prompt"}],"response":{"role":"assistant","content":"unused fixture"}}
`), 0o600))
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveHealthyEnvironment(w, r) {
			return
		}
		require.Equal(t, "/gateway/v1/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer test-secret", r.Header.Get("Authorization"))
		var body struct {
			Model       string   `json:"model"`
			MaxTokens   int      `json:"max_tokens"`
			Temperature *float64 `json:"temperature"`
			Messages    []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Contains(t, []string{"a", "b"}, body.Model)
		require.Equal(t, body.Model+"-prompt", body.Messages[0].Content)
		require.NotNil(t, body.Temperature)
		if body.Model == "a" {
			require.Equal(t, 32, body.MaxTokens)
			require.Zero(t, *body.Temperature)
		} else {
			require.Equal(t, 64, body.MaxTokens)
			require.Equal(t, 0.7, *body.Temperature)
		}
		counts[body.Model]++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"real server answer"}}]}`))
	}))
	defer server.Close()
	env := map[string]string{"GONKA_API_KEY": "test-secret", "GONKA_BASE": server.URL + "/gateway/", "GONKA_MODEL": "wrong-single-model"}
	var out bytes.Buffer
	require.NoError(t, run(context.Background(), []string{"-scenario", scenario, "-load-dataset", dataset, "-output", output}, func(key string) string { return env[key] }, &out))
	require.Positive(t, counts["a"])
	require.Positive(t, counts["b"])
	require.Contains(t, out.String(), "Result: PASS")
	require.Contains(t, out.String(), "REPORT SUMMARY")
	require.Less(t, strings.Index(out.String(), "PRE-FLIGHT CHECK"), strings.Index(out.String(), "LOAD TEST"))
	require.Less(t, strings.Index(out.String(), "LOAD TEST"), strings.Index(out.String(), "REPORT SUMMARY"))
	require.NotContains(t, out.String(), "MODEL SUMMARY")
	require.NotContains(t, out.String(), "| Metric |")
	require.Regexp(t, `Errors total +0 +0 +0`, out.String())
	report, readErr := os.ReadFile(filepath.Join(output, "report.md"))
	require.NoError(t, readErr)
	require.Contains(t, string(report), "| Metric | a | b | Total |")
	require.NotContains(t, out.String(), "↳ HTTP")
	require.NotContains(t, out.String(), "test-secret")
	for _, name := range []string{"run.yaml", "summary.json", "requests.jsonl", "assertions.json", "report.md", "environment.json"} {
		_, err := os.Stat(filepath.Join(output, name))
		require.NoError(t, err)
	}
	var failed bytes.Buffer
	err := run(context.Background(), []string{"-scenario", scenario, "-load-dataset", dataset, "-model", "a", "-output", filepath.Join(dir, "override")}, func(key string) string { return env[key] }, &failed)
	require.ErrorContains(t, err, "-model cannot override mix.models")
}

func TestRemoteMissingDatasetModelFailsBeforeRequests(t *testing.T) {
	dir := t.TempDir()
	scenario := filepath.Join(dir, "scenario.yaml")
	dataset := filepath.Join(dir, "dataset.jsonl")
	output := filepath.Join(dir, "output")
	require.NoError(t, os.WriteFile(scenario, []byte(scenarioHeader), 0o600))
	require.NoError(t, os.WriteFile(dataset, []byte(`{"model":"a","messages":[{"role":"user","content":"only-a"}],"response":{"role":"assistant","content":"fixture"}}`), 0o600))
	var out bytes.Buffer
	env := map[string]string{"GONKA_API_KEY": "test-secret", "GONKA_BASE": "http://127.0.0.1:1/gateway"}
	err := run(context.Background(), []string{"-scenario", scenario, "-load-dataset", dataset, "-output", output}, func(key string) string { return env[key] }, &out)
	require.ErrorContains(t, err, `dataset has no samples for configured model "b"`)
	_, err = os.Stat(output)
	require.True(t, os.IsNotExist(err))
}

func TestRemoteHTTPFailureProducesFailedReport(t *testing.T) {
	dir := t.TempDir()
	scenario := filepath.Join(dir, "scenario.yaml")
	body := strings.Replace(scenarioHeader, "    mix:\n      models:\n        - {value: a, weight: 50}\n        - {value: b, weight: 50}", "    model: a\n    messages: [{role: user, content: test}]", 1)
	require.NoError(t, os.WriteFile(scenario, []byte(body), 0o600))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveHealthyEnvironment(w, r) {
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
	}))
	defer server.Close()
	var out bytes.Buffer
	env := map[string]string{"GONKA_API_KEY": "test-secret", "GONKA_BASE": server.URL}
	err := run(context.Background(), []string{"-scenario", scenario, "-output", filepath.Join(dir, "output")}, func(key string) string { return env[key] }, &out)
	require.ErrorContains(t, err, "remote client workload failed")
	require.Contains(t, out.String(), "Result: FAIL")
	require.Contains(t, out.String(), "HTTP 401")
	require.NotContains(t, out.String(), "HTTP 500")
}
