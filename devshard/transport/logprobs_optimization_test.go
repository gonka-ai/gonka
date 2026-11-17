package transport

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
)

// The gateway's choice must survive the wire in all three states, and a request that says nothing
// must not carry the field at all, so a host keeps its own default.
func TestLogprobsOptimizationChoiceSurvivesTheWire(t *testing.T) {
	optimize, forwardStored := true, false
	for _, testCase := range []struct {
		name   string
		choice *bool
	}{
		{name: "gateway says nothing"},
		{name: "gateway asks to optimize", choice: &optimize},
		{name: "gateway asks for the stored bytes", choice: &forwardStored},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			encoded, err := HostRequestToJSON(host.HostRequest{Nonce: 7, LogprobsOptimizationOverride: testCase.choice})
			require.NoError(t, err)

			body, err := json.Marshal(encoded)
			require.NoError(t, err)
			if testCase.choice == nil {
				require.NotContains(t, string(body), "logprobs_optimization_override",
					"a gateway with no opinion must leave the host's default alone: %s", body)
			}

			var received InferenceRequest
			require.NoError(t, json.Unmarshal(body, &received))
			restored, err := HostRequestFromJSON(received)
			require.NoError(t, err)
			require.Equal(t, testCase.choice, restored.LogprobsOptimizationOverride)
		})
	}
}
