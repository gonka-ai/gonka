package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// Steps:
// - Assert a fresh gateway states no choice, leaving every executor its own default.
// - From a known prior state, set the choice through admin settings and assert the live reader follows it.
// - Assert the store holds the same choice, so it survives a restart.
func TestAdminSettingsCarryTheLogprobsOptimizationChoice(t *testing.T) {
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{{id: "12", model: "Qwen/Test", active: true}})
	require.Nil(t, env.gateway.logprobsOptimizationOverride(), "a gateway states nothing until it is told to")

	for _, testCase := range []struct {
		name  string
		prior string
		body  string
		want  *bool
	}{
		{name: "ask executors to forward what they stored", prior: `{"logprobs_optimization":{"enabled":true}}`, body: `{"logprobs_optimization":{"enabled":false}}`, want: boolPtr(false)},
		{name: "ask executors to optimize", prior: `{"logprobs_optimization":{"enabled":false}}`, body: `{"logprobs_optimization":{"enabled":true}}`, want: boolPtr(true)},
		{name: "clear the choice", prior: `{"logprobs_optimization":{"enabled":true}}`, body: `{"logprobs_optimization":{}}`, want: nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, http.StatusOK, env.do(http.MethodPost, "/v1/admin/settings", testCase.prior, withBearer(mockenvAdminKey)).Code)

			resp := env.do(http.MethodPost, "/v1/admin/settings", testCase.body, withBearer(mockenvAdminKey))
			require.Equal(t, http.StatusOK, resp.Code, "admin settings rejected the choice: %s", resp.Body)
			require.Equal(t, testCase.want, env.gateway.logprobsOptimizationOverride())

			stored, ok, err := env.gateway.store.LoadState()
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, testCase.want, stored.Settings.LogprobsOptimizationOverride,
				"the choice must survive a restart, not just live in memory")
		})
	}
}
