package inference

import (
	"bytes"
	"encoding/json"
	"strings"

	"common/completionapi"
)

// wireOnlyFields are dropped from what the executor sends the gateway. The gateway strips all four on
// arrival, so carrying them buys nothing; they stay in what the executor accumulates for validation.
var wireOnlyFields = []string{"prompt_token_ids", "prompt_logprobs", "token_ids", "logprobs"}

// stripWireOnlyFields removes them from one SSE line, returning anything it cannot parse unchanged so a
// host's malformed answer reaches the gateway as malformed rather than as empty.
func stripWireOnlyFields(line string) string {
	if !strings.HasPrefix(line, completionapi.DataPrefix) {
		return line
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, completionapi.DataPrefix))
	stripped, ok := stripWireOnlyFieldsFromJSON([]byte(payload))
	if !ok {
		return line
	}
	return completionapi.DataPrefix + string(stripped)
}

// stripWireOnlyFieldsFromJSON reports false when the body is not an object it could rewrite.
func stripWireOnlyFieldsFromJSON(body []byte) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, false
	}
	if _, isObject := document.(map[string]any); !isObject {
		return nil, false
	}
	dropWireOnlyFields(document)
	rewritten, err := json.Marshal(document)
	if err != nil {
		return nil, false
	}
	return rewritten, true
}

func dropWireOnlyFields(node any) {
	switch typed := node.(type) {
	case map[string]any:
		for _, field := range wireOnlyFields {
			delete(typed, field)
		}
		for _, child := range typed {
			dropWireOnlyFields(child)
		}
	case []any:
		for _, child := range typed {
			dropWireOnlyFields(child)
		}
	}
}
