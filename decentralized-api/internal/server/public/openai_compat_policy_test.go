package public

import (
	"testing"

	"common/completionapi"

	"github.com/stretchr/testify/require"
)

func TestDesiredPolicy_OpenAICompat(t *testing.T) {
	// Reference spec:
	// https://platform.openai.com/docs/api-reference/chat/create
	//
	// This matrix is our guardrail for early request rejection at TA/API layer,
	// so malformed payloads are blocked before they reach vLLM and fail there
	cases := []struct {
		name       string
		body       string
		shouldPass bool
	}{
		// developer
		{"developer text valid", `{"model":"m","messages":[{"role":"developer","content":"always answer in json"}]}`, true},
		{"developer with name valid", `{"model":"m","messages":[{"role":"developer","name":"router","content":"always answer in json"}]}`, true},
		{"developer empty invalid", `{"model":"m","messages":[{"role":"developer","content":""}]}`, false},
		{"developer null invalid", `{"model":"m","messages":[{"role":"developer","content":null}]}`, false},
		{"developer missing invalid", `{"model":"m","messages":[{"role":"developer"}]}`, false},
		{"developer tool_calls invalid", `{"model":"m","messages":[{"role":"developer","content":"x","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`, false},

		// system
		{"system text valid", `{"model":"m","messages":[{"role":"system","content":"rules"}]}`, true},
		{"system with name valid", `{"model":"m","messages":[{"role":"system","name":"policy","content":"rules"}]}`, true},
		{"system whitespace invalid", `{"model":"m","messages":[{"role":"system","content":"   "}]}`, false},
		{"system null invalid", `{"model":"m","messages":[{"role":"system","content":null}]}`, false},
		{"system missing invalid", `{"model":"m","messages":[{"role":"system"}]}`, false},
		{"system tool_call_id invalid", `{"model":"m","messages":[{"role":"system","content":"x","tool_call_id":"c1"}]}`, false},

		// user
		{"user text valid", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, true},
		{"user with name valid", `{"model":"m","messages":[{"role":"user","name":"alice","content":"hi"}]}`, true},
		{"user image_url part valid", `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/cat.png"}}]}]}`, true},
		{"user custom typed part valid", `{"model":"m","messages":[{"role":"user","content":[{"type":"vendor_custom","payload":{"k":"v"}}]}]}`, true},
		{"user empty invalid", `{"model":"m","messages":[{"role":"user","content":""}]}`, false},
		{"user null invalid", `{"model":"m","messages":[{"role":"user","content":null}]}`, false},
		{"user missing invalid", `{"model":"m","messages":[{"role":"user"}]}`, false},
		{"user tool_calls invalid", `{"model":"m","messages":[{"role":"user","content":"hi","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`, false},

		// assistant
		{"assistant text valid", `{"model":"m","messages":[{"role":"assistant","content":"ok"}]}`, true},
		{"assistant with name valid", `{"model":"m","messages":[{"role":"assistant","name":"helper","content":"ok"}]}`, true},
		{"assistant null+tool_calls valid", `{"model":"m","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`, true},
		{"assistant missing+tool_calls valid", `{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`, true},
		{"assistant null without tool_calls invalid", `{"model":"m","messages":[{"role":"assistant","content":null}]}`, false},
		{"assistant missing without tool_calls invalid", `{"model":"m","messages":[{"role":"assistant"}]}`, false},
		{"assistant tool_calls wrong type invalid", `{"model":"m","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"custom","custom":{"name":"x","input":"y"}}]}]}`, false},
		{"assistant tool_calls not array invalid", `{"model":"m","messages":[{"role":"assistant","content":null,"tool_calls":{"id":"c1"}}]}`, false},
		{"assistant legacy function_call valid", `{"model":"m","messages":[{"role":"assistant","content":null,"function_call":{"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}]}`, true},

		// tool
		{"tool valid", `{"model":"m","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"{\"temp\":72}"}]}`, true},
		{"tool missing tool_call_id invalid", `{"model":"m","messages":[{"role":"tool","content":"x"}]}`, false},
		{"tool empty tool_call_id invalid", `{"model":"m","messages":[{"role":"tool","tool_call_id":"","content":"x"}]}`, false},
		{"tool null content invalid", `{"model":"m","messages":[{"role":"tool","tool_call_id":"c1","content":null}]}`, false},
		{"tool missing content invalid", `{"model":"m","messages":[{"role":"tool","tool_call_id":"c1"}]}`, false},

		// legacy role=function
		{"legacy function role valid", `{"model":"m","messages":[{"role":"assistant","content":null,"function_call":{"name":"get_weather","arguments":"{}"}},{"role":"function","name":"get_weather","content":"{\"temp\":72}"}]}`, true},
		{"legacy function role missing name invalid", `{"model":"m","messages":[{"role":"function","content":"x"}]}`, false},

		// sequence integrity
		{"sequence valid", `{"model":"m","messages":[{"role":"user","content":"weather?"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"72F"},{"role":"assistant","content":"72F and sunny"}]}`, true},
		{"orphan tool invalid", `{"model":"m","messages":[{"role":"tool","tool_call_id":"call_404","content":"x"}]}`, false},
		{"mismatched tool_call_id invalid", `{"model":"m","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_2","content":"x"}]}`, false},

		// malformed shapes
		{"messages not array invalid", `{"model":"m","messages":{"role":"user","content":"x"}}`, false},
		{"messages missing invalid for chat", `{"model":"m"}`, false},
		{"message not object invalid", `{"model":"m","messages":["bad"]}`, false},
		{"content wrong type invalid", `{"model":"m","messages":[{"role":"user","content":123}]}`, false},
		{"content bool invalid", `{"model":"m","messages":[{"role":"user","content":true}]}`, false},
		{"content object invalid", `{"model":"m","messages":[{"role":"user","content":{"key":"val"}}]}`, false},

		// unknown role
		{"unknown role invalid", `{"model":"m","messages":[{"role":"moderator","content":"x"}]}`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := createTestRequest([]byte(tc.body))
			_, readErr := readRequest(req, "transfer-agent", []byte(tc.body), "", "", nil)
			_, modifyErr := completionapi.ModifyRequestBody([]byte(tc.body), 7)

			gotPass := readErr == nil && modifyErr == nil
			require.Equalf(t, tc.shouldPass, gotPass, "readErr=%v modifyErr=%v", readErr, modifyErr)
		})
	}
}
