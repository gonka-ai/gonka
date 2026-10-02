package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeChatRequestDropsOutOfRangeLogitBiasKeys(t *testing.T) {
	for _, tc := range []struct {
		name      string
		logitBias string
		want      any
	}{
		{name: "out of range keys are dropped", logitBias: `{"999999999": 100, "-1": 5, "9_999_999": 5, "7": 10}`, want: map[string]any{"7": float64(10)}},
		{name: "field is dropped when no key remains", logitBias: `{"999999999": 100}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _, err := normalizeChatRequest([]byte(`{"logit_bias": ` + tc.logitBias + `, "messages": [{"role": "user", "content": "hello"}]}`))
			require.NoError(t, err)
			var request map[string]any
			require.NoError(t, json.Unmarshal(body, &request))
			require.Equal(t, tc.want, request["logit_bias"])
		})
	}
}
