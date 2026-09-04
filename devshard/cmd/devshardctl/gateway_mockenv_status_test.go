package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Steps:
// - Create one active runtime with a status handler.
// - Request pooled gateway status.
// - Assert single-runtime status is proxied to that runtime instead of aggregated.
func TestGatewayMockEnvSingleRuntimeStatusProxiesRuntime(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/v1/status", r.URL.Path)
			writeJSON(w, map[string]any{
				"mode": "runtime",
				"id":   "12",
			})
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})

	rec := env.get("/v1/status")

	require.Equal(t, http.StatusOK, rec.Code)
	requireMockenvJSONField(t, rec.Body, "mode", "runtime")
	requireMockenvJSONField(t, rec.Body, "id", "12")
	require.EqualValues(t, 1, rt.calls.Load())
}

// Steps:
// - Create two active runtimes.
// - Request pooled gateway status.
// - Assert the gateway returns aggregate status instead of proxying a runtime.
func TestGatewayMockEnvMultiRuntimeStatusIsAggregate(t *testing.T) {
	alpha := &gatewayMockRuntime{
		id:     "11",
		model:  "Qwen/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("multi-runtime pooled status should not proxy runtime handlers")
		},
	}
	beta := &gatewayMockRuntime{
		id:     "22",
		model:  "Kimi/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("multi-runtime pooled status should not proxy runtime handlers")
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{alpha, beta}, withMockenvSettings(func(settings *GatewaySettings) {
		settings.ModelLimits = append(settings.ModelLimits, GatewayModelLimitSettings{
			ModelID:    "Kimi/Test",
			AccessMode: string(gatewayAccessModeOpen),
		})
	}))

	rec := env.get("/v1/status")

	require.Equal(t, http.StatusOK, rec.Code)
	requireMockenvJSONField(t, rec.Body, "mode", "gateway")
	requireMockenvJSONField(t, rec.Body, "runtimes", float64(2))
	require.EqualValues(t, 0, alpha.calls.Load())
	require.EqualValues(t, 0, beta.calls.Load())
}

// Steps:
//   - Build a gateway with no resident runtimes (post-retire / pre-create).
//   - Request pooled /v1/status.
//   - Assert HTTP 200 with phase=not_found and no escrow_id (absence is named,
//     not an empty object that looks like a parse miss).
func TestGatewayMockEnvZeroRuntimeStatusIsNotFound(t *testing.T) {
	env := newGatewayMockEnv(t, nil)

	rec := env.get("/v1/status")

	require.Equal(t, http.StatusOK, rec.Code)
	requireMockenvJSONField(t, rec.Body, "mode", "gateway")
	requireMockenvJSONField(t, rec.Body, "runtimes", float64(0))
	requireMockenvJSONField(t, rec.Body, "phase", "not_found")

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotContains(t, body, "escrow_id")
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "zero-runtime status must include error: %v", body)
	require.Equal(t, "not_found", errObj["type"])
	require.Equal(t, "no active escrow", errObj["message"])
}
