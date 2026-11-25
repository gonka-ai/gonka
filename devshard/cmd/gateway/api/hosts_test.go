package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/perf"
)

// Test flow:
//  1. Seed the harness with one host state, one degraded flag and one host window for the same participant/model pair.
//  2. Request the /v1/admin/hosts endpoint.
//  3. Assert the single returned host row carries the routing state and window fields from both sources.
func TestAdminHostsReportsRoutingStateAndWindow(t *testing.T) {
	live := newHarness(t)
	live.hosts.states = []perf.HostState{{
		Participant: "gonka1aaaa", Model: "qwen", Ejected: true, Inflight: 2,
		TimePerOutputToken: 25 * time.Millisecond,
	}}
	live.hosts.degraded = map[string]bool{"gonka1aaaa|qwen": true}
	live.hosts.windows = []limits.HostWindow{{
		Participant: "gonka1aaaa", Model: "qwen",
		InputWindowTokens: 8_192, OutputWindowTokens: 4_096,
		InflightInputTokens: 2_048, InflightOutputTokens: 512,
		Cutoff: limits.CutoffOpen, BackoffCount: 3, Available: false,
	}}

	recorder := live.request(t, http.MethodGet, "/v1/admin/hosts", "", adminHeaders())

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var answer struct {
		Hosts []map[string]any `json:"hosts"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &answer))
	require.Len(t, answer.Hosts, 1)
	host := answer.Hosts[0]
	require.Equal(t, "gonka1aaaa", host["participant_key"])
	require.Equal(t, "qwen", host["model"])
	require.Equal(t, true, host["ejected"])
	require.Equal(t, true, host["degraded"])
	require.Equal(t, float64(8_192), host["input_window_tokens"])
	require.Equal(t, float64(4_096), host["output_window_tokens"])
	require.Equal(t, float64(2_048), host["inflight_input_tokens"])
	require.Equal(t, float64(512), host["inflight_output_tokens"])
	require.Equal(t, string(limits.CutoffOpen), host["cutoff"])
	require.Equal(t, float64(3), host["backoff_count"])
	require.Equal(t, false, host["available"])
}

// Test flow:
//  1. Seed one host state for one participant/model pair and one host window for a different pair.
//  2. Request the /v1/admin/hosts endpoint.
//  3. Assert both pairs appear as separate rows, since either source alone should list a host.
func TestAdminHostsJoinsTheTwoSourcesByPair(t *testing.T) {
	live := newHarness(t)
	live.hosts.states = []perf.HostState{{Participant: "gonka1aaaa", Model: "qwen"}}
	live.hosts.windows = []limits.HostWindow{{Participant: "gonka1bbbb", Model: "qwen", InputWindowTokens: 4_096}}

	recorder := live.request(t, http.MethodGet, "/v1/admin/hosts", "", adminHeaders())

	var answer struct {
		Hosts []map[string]any `json:"hosts"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &answer))
	require.Len(t, answer.Hosts, 2, "a pair known to either source is a host the operator can ask about")
}

// Test flow:
//  1. Request the /v1/admin/hosts endpoint without any admin key.
//  2. Assert the response is 401.
func TestAdminHostsNeedsTheAdminKey(t *testing.T) {
	live := newHarness(t)

	recorder := live.request(t, http.MethodGet, "/v1/admin/hosts", "", nil)

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}

// Test flow:
//  1. Clear the harness's host states and windows.
//  2. Request the /v1/admin/hosts endpoint with the admin key.
//  3. Assert the response is 200 with an empty hosts list.
func TestAdminHostsWithoutSourcesAnswersEmpty(t *testing.T) {
	live := newHarness(t)
	live.hosts.states = nil
	live.hosts.windows = nil

	recorder := live.request(t, http.MethodGet, "/v1/admin/hosts", "", adminHeaders())

	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"hosts":[]}`, recorder.Body.String())
}
