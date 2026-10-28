package api

import (
	"net/http"
	"testing"

	"devshard/cmd/gateway/config"
	"devshard/user"
)

// Test flow:
//  1. Run one chat through the harness with the logprobs optimization unset, forced off, and forced on.
//  2. Read the inference parameters the race was handed.
//  3. Assert unset reaches the host as nil, so the host's own default applies, and a forced value reaches it as itself.
func TestTheLogprobsOptimizationOverrideReachesTheHostAsSet(t *testing.T) {
	off, on := false, true
	for _, testCase := range []struct {
		name    string
		setting *bool
	}{
		{name: "unset defers to the host"},
		{name: "forced off", setting: &off},
		{name: "forced on", setting: &on},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t, func(configuration *config.Config) {
				configuration.Limits.LogprobsOptimization = testCase.setting
			})

			if recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil); recorder.Code != http.StatusOK {
				t.Fatalf("chat: got %d (%s)", recorder.Code, recorder.Body.String())
			}

			params := live.inference.raced.Load().Params.(user.InferenceParams)
			got := params.LogprobsOptimizationOverride
			if (got == nil) != (testCase.setting == nil) || (got != nil && *got != *testCase.setting) {
				t.Fatalf("LogprobsOptimizationOverride = %v, want %v", got, testCase.setting)
			}
		})
	}
}
