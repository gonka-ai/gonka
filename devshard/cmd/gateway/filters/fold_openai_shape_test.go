package filters

import (
	"encoding/json"
	"testing"
)

func foldedChoice(t *testing.T, events ...string) map[string]any {
	t.Helper()
	body := foldedBody(t, events...)
	choices, _ := body["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("folded body %v, want one choice", body)
	}
	choice, _ := choices[0].(map[string]any)
	return choice
}

func foldedBody(t *testing.T, events ...string) map[string]any {
	t.Helper()
	folder := NewBodyFolder(LogprobIntent{})
	for _, event := range events {
		if _, err := folder.Write([]byte("data: " + event + "\n\n")); err != nil {
			t.Fatalf("Write(): %v", err)
		}
	}
	var body map[string]any
	if err := json.Unmarshal(folder.Body(), &body); err != nil {
		t.Fatalf("folded body is not JSON: %v", err)
	}
	return body
}

// Test flow:
//  1. Fold a streamed tool call whose chunks carry no role and an indexed tool call entry.
//  2. Assert the message has role assistant, a null content, and tool_calls without index: what an OpenAI client reads from a non-streamed tool call.
func TestAFoldedToolCallReadsAsAnOpenAIMessage(t *testing.T) {
	choice := foldedChoice(t,
		`{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"get_weather","arguments":"{\"city\""}}]}}]}`,
		`{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`,
	)
	message, _ := choice["message"].(map[string]any)
	if message["role"] != "assistant" {
		t.Errorf("message role = %v, want assistant", message["role"])
	}
	if content, present := message["content"]; !present || content != nil {
		t.Errorf("message content = %v (present %v), want null", content, present)
	}
	toolCalls, _ := message["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("tool_calls = %v, want one", message["tool_calls"])
	}
	if _, indexed := toolCalls[0].(map[string]any)["index"]; indexed {
		t.Errorf("tool call %v still carries index", toolCalls[0])
	}
}

// Test flow:
//  1. Fold a plain answer whose chunks carry an empty tool_calls list, as Kimi streams one.
//  2. Assert the message carries the text and no tool_calls key.
func TestAFoldedPlainAnswerDropsAnEmptyToolCallsList(t *testing.T) {
	choice := foldedChoice(t,
		`{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hi","tool_calls":[]}}]}`,
		`{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" there"},"finish_reason":"stop"}]}`,
	)
	message, _ := choice["message"].(map[string]any)
	if message["content"] != "hi there" {
		t.Errorf("message content = %v, want hi there", message["content"])
	}
	if toolCalls, present := message["tool_calls"]; present {
		t.Errorf("message tool_calls = %v, want the key absent", toolCalls)
	}
}

// Test flow:
//  1. Fold a stream that carries nothing but an error document.
//  2. Assert the body is that error and is not relabelled a chat.completion.
func TestAFoldedErrorIsNotRelabelledACompletion(t *testing.T) {
	body := foldedBody(t, `{"error":{"message":"boom","type":"server_error"}}`)
	if object, present := body["object"]; present {
		t.Errorf("error body object = %v, want none", object)
	}
	if _, isError := body["error"].(map[string]any); !isError {
		t.Errorf("body %v, want the error kept", body)
	}
}
