package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Aggregation preserves upstream finish reasons and opaque tool arguments.
func TestAggregateSSEStreamPreservesToolFields(t *testing.T) {
	for _, tc := range []struct{ name, finish, arguments string }{
		{"stop", "stop", `{"value":"OK"}`},
		{"tool_calls", "tool_calls", `{"value":"OK"}`},
		{"malformed_arguments", "tool_calls", `{"" "value": "OK"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, err := json.Marshal(tc.arguments)
			require.NoError(t, err)
			raw := sseData(fmt.Sprintf(`{"id":"fixture","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"answer","arguments":%s}}]},"finish_reason":%q}],"usage":{"prompt_tokens":19,"completion_tokens":20}}`, args, tc.finish))
			var response map[string]any
			require.NoError(t, json.Unmarshal(aggregateSSEStream(raw, clientResponseIntent{}), &response))
			choice := response["choices"].([]any)[0].(map[string]any)
			require.Equal(t, tc.finish, choice["finish_reason"])
			message := choice["message"].(map[string]any)
			calls := message["tool_calls"].([]any)
			require.Len(t, calls, 1)
			fn := calls[0].(map[string]any)["function"].(map[string]any)
			require.Equal(t, "answer", fn["name"])
			require.Equal(t, tc.arguments, fn["arguments"])
			require.EqualValues(t, 20, response["usage"].(map[string]any)["completion_tokens"])
		})
	}
}
