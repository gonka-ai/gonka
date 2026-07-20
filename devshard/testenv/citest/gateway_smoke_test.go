package citest

import (
	"os"
	"testing"
	"time"

	"devshard/testenv/citest/harness"

	"github.com/stretchr/testify/require"
)

// TestGatewayPhase7_Smoke exercises devshardctl → versiond-router → devshardd → mock-openai.
// Requires Docker, a linux devshardd at ../../build/devshardd, and TESTENV_GATEWAY_SMOKE=1.
func TestGatewayPhase7_Smoke(t *testing.T) {
	if os.Getenv("TESTENV_GATEWAY_SMOKE") != "1" {
		t.Skip("set TESTENV_GATEWAY_SMOKE=1 to run full-stack gateway smoke (Docker)")
	}

	stack := harness.NewStack(t, "citest-gateway-*")
	harness.RequireLinuxDevshardd(t, stack.TestenvDir)

	harness.WriteSingleVersiondConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	stack.Up(t)

	cfg := stack.LoadConfig(t)
	eps := harness.EndpointsFromConfig(cfg)
	client := harness.HTTPClient()
	poll := 3 * time.Minute

	harness.WaitGETOK(t, client, eps.GatewayHTTP+"/v1/status", poll, "gateway /v1/status")

	var status map[string]any
	require.NoError(t, harness.GetJSON(client, eps.GatewayHTTP+"/v1/status", &status))
	t.Logf("gateway status: %v", status)

	chatBody := map[string]any{
		"model": "test-model",
		"messages": []map[string]string{
			{"role": "user", "content": "phase7 smoke"},
		},
		"max_tokens": 32,
	}
	var completion map[string]any
	require.NoError(t, harness.PostJSON(client, eps.GatewayHTTP+"/v1/chat/completions", chatBody, &completion))
	choices, ok := completion["choices"].([]any)
	require.True(t, ok, "completion response: %v", completion)
	require.NotEmpty(t, choices)

	createBody := map[string]any{
		"amount":   500_000,
		"model_id": "test-model",
		"register": true,
	}
	var created map[string]any
	require.NoError(t, harness.PostJSON(client, eps.GatewayHTTP+"/v1/admin/escrows", createBody, &created))
	require.NotZero(t, created["escrow_id"])
	t.Logf("created escrow via gateway REST: %v", created)
}
