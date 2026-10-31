package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// StreamReply is a streaming answer read whatever its status, so a failed stream can be compared too.
type StreamReply struct {
	StatusCode int
	Body       string
	Events     []string
	Err        error
}

// SendStreamingRaw posts a streaming body and reads every SSE event without asserting on the status.
func SendStreamingRaw(t *testing.T, client *http.Client, url string, body map[string]any, bearerToken string) StreamReply {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(data)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return StreamReply{Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, events := readSSEEvents(t, resp.Body)
	return StreamReply{StatusCode: resp.StatusCode, Body: raw, Events: events}
}

// ReplyText is the assistant's text of a served answer, or the error message of a refused one.
func ReplyText(resp RawResponse) string {
	if resp.StatusCode == http.StatusOK {
		if choices, ok := resp.JSON["choices"].([]any); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]any); ok {
				if message, ok := choice["message"].(map[string]any); ok {
					content, _ := message["content"].(string)
					return content
				}
			}
		}
		return resp.Body
	}
	return errorText(resp.JSON, resp.Body)
}

// StreamText joins a stream's content deltas, and ends with the error an event carried if one did.
func StreamText(reply StreamReply) string {
	if reply.Err != nil {
		return "no answer: " + reply.Err.Error()
	}
	if reply.StatusCode != http.StatusOK {
		var payload map[string]any
		_ = json.Unmarshal([]byte(reply.Body), &payload)
		return errorText(payload, reply.Body)
	}
	var text strings.Builder
	for _, event := range reply.Events {
		var payload map[string]any
		if json.Unmarshal([]byte(event), &payload) != nil {
			continue
		}
		if _, failed := payload["error"]; failed {
			text.WriteString("[stream error: " + errorText(payload, event) + "]")
			continue
		}
		choices, _ := payload["choices"].([]any)
		for _, rawChoice := range choices {
			choice, _ := rawChoice.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			content, _ := delta["content"].(string)
			text.WriteString(content)
		}
	}
	return text.String()
}

func errorText(payload map[string]any, body string) string {
	if failure, ok := payload["error"].(map[string]any); ok {
		message, _ := failure["message"].(string)
		kind, _ := failure["type"].(string)
		return strings.TrimSpace(kind + ": " + message)
	}
	return strings.TrimSpace(body)
}

// DiffJSON lists every path where two decoded JSON values differ, one line each, in path order.
func DiffJSON(path string, left, right any) []string {
	leftObject, leftIsObject := left.(map[string]any)
	rightObject, rightIsObject := right.(map[string]any)
	if leftIsObject && rightIsObject {
		keys := make([]string, 0, len(leftObject)+len(rightObject))
		for key := range leftObject {
			keys = append(keys, key)
		}
		for key := range rightObject {
			if _, shared := leftObject[key]; !shared {
				keys = append(keys, key)
			}
		}
		slices.Sort(keys)
		var differences []string
		for _, key := range keys {
			leftValue, inLeft := leftObject[key]
			rightValue, inRight := rightObject[key]
			switch {
			case !inRight:
				differences = append(differences, fmt.Sprintf("%s.%s: only left = %s", path, key, compactJSON(leftValue)))
			case !inLeft:
				differences = append(differences, fmt.Sprintf("%s.%s: only right = %s", path, key, compactJSON(rightValue)))
			default:
				differences = append(differences, DiffJSON(path+"."+key, leftValue, rightValue)...)
			}
		}
		return differences
	}
	if reflect.DeepEqual(left, right) {
		return nil
	}
	return []string{fmt.Sprintf("%s: left = %s, right = %s", path, compactJSON(left), compactJSON(right))}
}

func compactJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}
