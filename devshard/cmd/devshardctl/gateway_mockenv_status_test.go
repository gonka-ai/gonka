package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

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
