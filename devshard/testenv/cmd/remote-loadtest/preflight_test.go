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
	"sync/atomic"
	"testing"
	"time"

	"devshard/testenv/loadtest"
	"github.com/stretchr/testify/require"
)

const healthyModelList = `{"data":[{"id":"a","context_length":4096,"max_completion_tokens":256},{"id":"b","max_model_len":4096,"max_completion_tokens":256},{"id":"extra"}]}`
const healthyGatewayStatus = `{"capacity":{"host_count":7,"available_host_count":7,"available_percent":100,"models":{"a":{"current_weight":100,"available_percent":100,"routable":true,"access_enabled":true},"b":{"current_weight":100,"available_percent":100,"routable":true,"access_enabled":true}},"escrow_weights":{"10":100,"11":100}},"limiter":{"models":{"a":{"max_concurrent_requests":10,"effective_max_concurrent_requests":10,"in_flight_requests":2},"b":{"max_concurrent_requests":7,"effective_max_concurrent_requests":7,"in_flight_requests":1}}},"devshards":[{"id":"10","model":"a","active":true,"phase":"active","balance":9007199254740993,"nonce":21,"reserved_tokens":32,"active_requests":2,"chain_phase":"Inference"},{"id":"11","model":"b","active":true,"phase":"active","balance":"5000000000","nonce":12,"reserved_tokens":10,"active_requests":1,"chain_phase":"Inference"}]}`
const healthyEpoch = `{"block_height":100,"phase":"Inference","is_confirmation_poc_active":false,"epoch_stages":{"epoch_index":5,"next_poc_start":200}}`

func serveHealthyEnvironment(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/gateway/v1/models", "/v1/models":
		_, _ = w.Write([]byte(healthyModelList))
	case "/gateway/v1/status", "/v1/status":
		_, _ = w.Write([]byte(healthyGatewayStatus))
	case "/gateway/metrics", "/metrics":
		_, _ = w.Write([]byte("# TYPE process_cpu_seconds_total counter\nprocess_cpu_seconds_total 1\n"))
	case "/v1/epochs/latest":
		_, _ = w.Write([]byte(healthyEpoch))
	default:
		return false
	}
	return true
}

func environmentScenario(t *testing.T) loadtest.Scenario {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scenario.yaml")
	require.NoError(t, os.WriteFile(path, []byte(scenarioHeader), 0o600))
	s, err := loadtest.LoadDatasetScenario(path)
	require.NoError(t, err)
	return s
}

func TestEnvironmentSnapshotSingleAndMultipleModels(t *testing.T) {
	s := environmentScenario(t)
	for _, single := range []bool{false, true} {
		t.Run(map[bool]string{false: "multiple", true: "single"}[single], func(t *testing.T) {
			scenario := s
			if single {
				scenario.Workload.Request.Mix = nil
				scenario.Workload.Request.Model = "b"
			}
			var modelAuth, statusAuth, epochAuth atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/gateway/v1/models":
					if r.Header.Get("Authorization") == "Bearer secret-key" {
						modelAuth.Add(1)
					}
				case "/gateway/v1/status":
					if r.Header.Get("Authorization") == "Bearer secret-key" {
						statusAuth.Add(1)
					}
				case "/v1/epochs/latest":
					if r.Header.Get("Authorization") == "" {
						epochAuth.Add(1)
					}
				}
				if !serveHealthyEnvironment(w, r) {
					http.Error(w, "no inference in preflight", 500)
				}
			}))
			defer server.Close()
			e := collectEnvironment(context.Background(), server.Client(), server.URL+"/gateway", "secret-key", scenario, nil, time.Second)
			require.True(t, e.Ready, e.Problems)
			require.Equal(t, len(scenario.RequestModels()), len(e.Models))
			require.EqualValues(t, 1, modelAuth.Load())
			require.EqualValues(t, 1, statusAuth.Load())
			require.EqualValues(t, 1, epochAuth.Load())
			require.Equal(t, []string{"a", "b", "extra"}, e.AdvertisedModels)
			require.Equal(t, "Inference", e.Network.Phase)
			require.NotNil(t, e.Network.ConfirmationPoCActive)
			require.False(t, *e.Network.ConfirmationPoCActive)
			require.Equal(t, "100", e.Network.BlockHeight.String())
			require.Equal(t, "9007199254740993", e.Escrows[0].Balance.String())
			if !single {
				require.EqualValues(t, 8, *e.Models[0].FreeConcurrency)
			}
			text := environmentText(e)
			require.Contains(t, text, "READY")
			require.Contains(t, text, "9007199254740993")
		})
	}
}

func TestEnvironmentReadinessUsesKnownModelAndEscrowState(t *testing.T) {
	s := environmentScenario(t)
	s.Workload.Request.MaxTokens = 64
	for _, tc := range []struct {
		name, models, status, epoch string
		ready                       bool
		reason                      string
	}{
		{"healthy", healthyModelList, healthyGatewayStatus, healthyEpoch, true, ""},
		{"output cap mismatch", `{"data":[{"id":"a","max_completion_tokens":32},{"id":"b"}]}`, healthyGatewayStatus, healthyEpoch, false, "exceeds advertised completion limit"},
		{"missing model", `{"data":[{"id":"a"}]}`, healthyGatewayStatus, healthyEpoch, false, "not advertised"},
		{"invalid listing", `{"object":"list"}`, healthyGatewayStatus, healthyEpoch, false, "valid data array"},
		{"zero model capacity", healthyModelList, strings.Replace(healthyGatewayStatus, `"current_weight":100`, `"current_weight":0`, 1), healthyEpoch, false, "no live host capacity"},
		{"zero effective limit", healthyModelList, strings.Replace(healthyGatewayStatus, `"effective_max_concurrent_requests":10`, `"effective_max_concurrent_requests":0`, 1), healthyEpoch, false, "effective concurrency limit is zero"},
		{"zero balance", healthyModelList, strings.Replace(healthyGatewayStatus, `"balance":9007199254740993`, `"balance":0`, 1), healthyEpoch, false, "no usable escrow"},
		{"held escrow", healthyModelList, strings.Replace(healthyGatewayStatus, `"id":"10"`, `"id":"10","on_hold":true`, 1), healthyEpoch, false, "no usable escrow"},
		{"PoC blocked", healthyModelList, strings.Replace(healthyGatewayStatus, `"id":"10"`, `"id":"10","requests_blocked":true`, 1), healthyEpoch, false, "no usable escrow"},
		{"no model runtime", healthyModelList, strings.Replace(healthyGatewayStatus, `"model":"b"`, `"model":"other"`, 1), healthyEpoch, false, "no escrow for this model"},
		{"PoC with capacity", healthyModelList, strings.ReplaceAll(healthyGatewayStatus, "Inference", "PoCGenerate"), strings.Replace(strings.Replace(healthyEpoch, "Inference", "PoCGenerate", 1), "false", "true", 1), true, ""},
		{"unlimited is not zero capacity", healthyModelList, `{"limiter":{"max_concurrent_requests":0,"effective_max_concurrent_requests":0}}`, healthyEpoch, true, ""},
		{"empty telemetry", healthyModelList, `{}`, `{}`, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/gateway/v1/models":
					_, _ = w.Write([]byte(tc.models))
				case "/gateway/v1/status":
					_, _ = w.Write([]byte(tc.status))
				case "/v1/epochs/latest":
					_, _ = w.Write([]byte(tc.epoch))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			e := collectEnvironment(context.Background(), server.Client(), server.URL+"/gateway", "key", s, nil, time.Second)
			require.Equal(t, tc.ready, e.Ready, e.Problems)
			if tc.reason != "" {
				require.Contains(t, strings.Join(e.Problems, ";"), tc.reason)
			}
			if tc.name == "empty telemetry" {
				require.Contains(t, strings.Join(e.Warnings, ";"), "unknown")
			}
		})
	}
}

func TestEnvironmentMissingOptionalEndpointsAndCredentialRedaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway/v1/models" {
			_, _ = w.Write([]byte(healthyModelList))
			return
		}
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"not available secret-key"}`))
	}))
	defer server.Close()
	e := collectEnvironment(context.Background(), server.Client(), server.URL+"/gateway", "secret-key", environmentScenario(t), nil, time.Second)
	require.True(t, e.Ready, e.Problems)
	require.GreaterOrEqual(t, len(e.Warnings), 2)
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret-key")
	require.Contains(t, string(raw), "[REDACTED]")
}

func TestRemoteBlockedRunsNeverSendInference(t *testing.T) {
	for _, mode := range []string{"missing-model", "zero-capacity"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			scenario := filepath.Join(dir, "scenario.yaml")
			output := filepath.Join(dir, "output")
			// No dataset is needed for this test, so provide valid fallback messages.
			source := strings.Replace(scenarioHeader, "  request:\n", "  request:\n    messages: [{role: user, content: test}]\n", 1)
			require.NoError(t, os.WriteFile(scenario, []byte(source), 0o600))
			var inference atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					inference.Add(1)
					http.Error(w, "unexpected inference", 500)
					return
				}
				if mode == "missing-model" && r.URL.Path == "/gateway/v1/models" {
					_, _ = w.Write([]byte(`{"data":[{"id":"a"}]}`))
					return
				}
				if mode == "zero-capacity" && r.URL.Path == "/gateway/v1/status" {
					_, _ = w.Write([]byte(strings.Replace(healthyGatewayStatus, `"current_weight":100`, `"current_weight":0`, 1)))
					return
				}
				if !serveHealthyEnvironment(w, r) {
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			env := map[string]string{"GONKA_BASE": server.URL + "/gateway", "GONKA_API_KEY": "test-secret"}
			args := []string{"-scenario", scenario, "-output", output}
			var stdout bytes.Buffer
			err := run(context.Background(), args, func(key string) string { return env[key] }, &stdout)
			require.ErrorContains(t, err, "preflight blocked workload")
			require.Zero(t, inference.Load())
			require.Contains(t, stdout.String(), "PRE-FLIGHT CHECK")
			require.Contains(t, stdout.String(), "REPORT SUMMARY")
			require.NotContains(t, stdout.String(), "  LOAD TEST\n")
			for _, name := range []string{"environment.json", "run.yaml", "report.md", "assertions.json"} {
				_, err := os.Stat(filepath.Join(output, name))
				require.NoError(t, err)
			}
			_, err = os.Stat(filepath.Join(output, "requests.jsonl"))
			require.True(t, os.IsNotExist(err))
			var assertion struct {
				WorkloadStarted bool `json:"workload_started"`
			}
			raw, err := os.ReadFile(filepath.Join(output, "assertions.json"))
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &assertion))
			require.False(t, assertion.WorkloadStarted)
		})
	}
}

func TestRemoteSingleModelOverrideIsCheckedBeforeWorkload(t *testing.T) {
	dir := t.TempDir()
	scenario := filepath.Join(dir, "scenario.yaml")
	source := strings.Replace(scenarioHeader, "    mix:\n      models:\n        - {value: a, weight: 50}\n        - {value: b, weight: 50}", "    model: original\n    messages: [{role: user, content: test}]", 1)
	require.NoError(t, os.WriteFile(scenario, []byte(source), 0o600))
	var sent atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveHealthyEnvironment(w, r) {
			return
		}
		require.Equal(t, http.MethodPost, r.Method)
		var request struct {
			Model string `json:"model"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, "b", request.Model)
		sent.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()
	var out bytes.Buffer
	env := map[string]string{"GONKA_BASE": server.URL + "/gateway", "GONKA_API_KEY": "secret", "GONKA_MODEL": "b"}
	err := run(context.Background(), []string{"-scenario", scenario, "-output", filepath.Join(dir, "output")}, func(key string) string { return env[key] }, &out)
	require.NoError(t, err)
	require.Positive(t, sent.Load())
	raw, err := os.ReadFile(filepath.Join(dir, "output", "environment.json"))
	require.NoError(t, err)
	var e environmentSnapshot
	require.NoError(t, json.Unmarshal(raw, &e))
	require.Len(t, e.Models, 1)
	require.Equal(t, "b", e.Models[0].ID)
	require.True(t, e.Ready)
}

func TestEnvironmentModelDiscoveryTimeoutStopsReadiness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway/v1/models" {
			<-r.Context().Done()
			return
		}
		if !serveHealthyEnvironment(w, r) {
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	e := collectEnvironment(context.Background(), server.Client(), server.URL+"/gateway", "secret", environmentScenario(t), nil, 50*time.Millisecond)
	require.False(t, e.Ready)
	require.Contains(t, strings.Join(e.Problems, ";"), "model discovery failed")
	require.Contains(t, e.Probes[0].Error, "context deadline exceeded")
}

func TestEnvironmentCanUseAnotherEscrowForTheSameModel(t *testing.T) {
	var status environmentStatus
	require.NoError(t, json.Unmarshal([]byte(healthyGatewayStatus), &status))
	inactive := false
	status.Devshards[0].Active = &inactive
	replacement := status.Devshards[0]
	active := true
	replacement.Active = &active
	replacement.ID = "12"
	status.Devshards = append(status.Devshards, replacement)
	raw, err := json.Marshal(status)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway/v1/status" {
			_, _ = w.Write(raw)
			return
		}
		if !serveHealthyEnvironment(w, r) {
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	e := collectEnvironment(context.Background(), server.Client(), server.URL+"/gateway", "secret", environmentScenario(t), nil, time.Second)
	require.True(t, e.Ready, e.Problems)
	require.Equal(t, []string{"10", "12"}, e.Models[0].EscrowIDs)
}
