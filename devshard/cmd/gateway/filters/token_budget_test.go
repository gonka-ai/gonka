package filters

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"common/completionapi"
)

const (
	testVocabularySize = 1000
	weatherTool        = `"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}]`
)

func normalizeWith(t *testing.T, body, routedModel string, vocabulary func(string) int) (map[string]any, Result, error) {
	t.Helper()
	result, err := NormalizeRequest([]byte(body), Options{
		RoutedModel:         routedModel,
		DefaultMaxTokens:    3072,
		MaxTokensCap:        4096,
		StopTokenVocabulary: vocabulary,
	})
	if err != nil {
		return nil, Result{}, err
	}
	var document map[string]any
	if err := json.Unmarshal(result.Body, &document); err != nil {
		t.Fatalf("unmarshal normalized body: %v", err)
	}
	return document, result, nil
}

func normalizedDocument(t *testing.T, body, routedModel string) map[string]any {
	t.Helper()
	document, _, err := normalizeWith(t, body, routedModel, nil)
	if err != nil {
		t.Fatalf("NormalizeRequest() = %v, want nil", err)
	}
	return document
}

func requireUintField(t *testing.T, document map[string]any, name string, want uint64) {
	t.Helper()
	raw, present := document[name]
	if !present {
		t.Fatalf("%s missing from %v", name, document)
	}
	number, ok := raw.(float64)
	if !ok || uint64(number) != want {
		t.Errorf("%s = %v, want %d", name, raw, want)
	}
}

func requireAbsent(t *testing.T, document map[string]any, name string) {
	t.Helper()
	if raw, present := document[name]; present {
		t.Errorf("%s = %v, want it absent from %v", name, raw, document)
	}
}

func fixedVocabulary(size int) func(string) int {
	return func(string) int { return size }
}

// Test flow:
//  1. Normalize a request with max_tokens set and min_tokens omitted.
//  2. Assert no min_tokens is injected: only a min_tokens the client sent is forwarded.
func TestAnOmittedMinTokensStaysOmitted(t *testing.T) {
	document := normalizedDocument(t, `{"messages":[{"role":"user","content":"x"}],"max_tokens":4096}`, "Qwen/Test")
	requireAbsent(t, document, "min_tokens")
}

// Test flow:
//  1. Normalize a request with a small min_tokens.
//  2. Assert min_tokens is forwarded as the client sent it, not raised to a floor.
func TestTheCallersMinTokensIsKept(t *testing.T) {
	document := normalizedDocument(t, `{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"min_tokens":4}`, "Qwen/Test")
	requireUintField(t, document, "min_tokens", 4)
}

// Test flow:
//  1. Normalize a request whose min_tokens is far larger than its max_tokens.
//  2. Assert max_tokens stays at the value the client asked for and min_tokens is clamped to it.
func TestTheCallersMinTokensIsClampedToTheResolvedBudget(t *testing.T) {
	document := normalizedDocument(t, `{"messages":[{"role":"user","content":"x"}],"max_tokens":300,"min_tokens":9000}`, "Qwen/Test")
	requireUintField(t, document, "max_tokens", 300)
	requireUintField(t, document, "min_tokens", 300)
}

// Test flow:
//  1. Normalize a request with min_tokens 0.
//  2. Assert min_tokens is dropped: a zero asks for nothing.
func TestAZeroMinTokensIsDropped(t *testing.T) {
	document := normalizedDocument(t, `{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"min_tokens":0}`, "Qwen/Test")
	requireAbsent(t, document, "min_tokens")
}

// Test flow:
//  1. Normalize requests that make the engine decode under a grammar, each with a min_tokens: a json_object or json_schema response format, a structured_outputs regex, and a tool choice naming a function.
//  2. Assert min_tokens is dropped from each: once the grammar allows only EOS, masking EOS leaves every logit at -inf.
func TestAGrammarConstrainedRequestCarriesNoMinTokens(t *testing.T) {
	for name, fields := range map[string]string{
		"json_object":         `"response_format":{"type":"json_object"}`,
		"json_schema":         `"response_format":{"type":"json_schema","json_schema":{"name":"a","schema":{"type":"object"}}}`,
		"structured_outputs":  `"structured_outputs":{"regex":"[a-z]+"}`,
		"named_function_tool": weatherTool + `,"tool_choice":{"type":"function","function":{"name":"get_weather"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			document := normalizedDocument(t, `{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"min_tokens":10,`+fields+`}`, "Qwen/Test")
			requireAbsent(t, document, "min_tokens")
		})
	}
}

// Test flow:
//  1. Normalize a request with tools, tool_choice "required" and a min_tokens.
//  2. Assert tool_choice goes out as "auto" and min_tokens is dropped: masking EOS past the forced call pads the call's arguments into invalid JSON.
func TestARequiredToolChoiceTheGatewayRelaxesCarriesNoMinTokens(t *testing.T) {
	document := normalizedDocument(t, `{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"min_tokens":10,`+weatherTool+`,"tool_choice":"required"}`, "Qwen/Test")
	if got := document["tool_choice"]; got != "auto" {
		t.Errorf("tool_choice = %v, want auto", got)
	}
	requireAbsent(t, document, "min_tokens")
}

// Test flow:
//  1. Normalize requests with a min_tokens that force no tool call: tools with tool_choice "auto" or with no tool_choice, and tool_choice "required" without tools or with an empty tools array.
//  2. Assert min_tokens is kept on each.
func TestAnUnforcedToolChoiceKeepsMinTokens(t *testing.T) {
	for name, fields := range map[string]string{
		"explicit_auto":             weatherTool + `,"tool_choice":"auto"`,
		"default_auto":              weatherTool,
		"required_without_tools":    `"tool_choice":"required"`,
		"required_with_empty_tools": `"tools":[],"tool_choice":"required"`,
	} {
		t.Run(name, func(t *testing.T) {
			document := normalizedDocument(t, `{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"min_tokens":10,`+fields+`}`, "Qwen/Test")
			requireUintField(t, document, "min_tokens", 10)
		})
	}
}

// Test flow:
//  1. Normalize a request with tools, tool_choice "required" and a min_tokens that is not an integer.
//  2. Assert it is rejected: min_tokens is validated before the required choice drops it.
func TestARequiredToolChoiceStillRejectsAMalformedMinTokens(t *testing.T) {
	_, _, err := normalizeWith(t, `{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"min_tokens":"ten",`+weatherTool+`,"tool_choice":"required"}`, "Qwen/Test", nil)
	if err == nil || err.Error() != "min_tokens: must be a non-negative integer" {
		t.Fatalf("NormalizeRequest() = %v, want the min_tokens rejection", err)
	}
}

// Test flow:
//  1. Normalize a request with max_tokens 8 once per routed model without a floor of its own: `minimaxModelID` and a plain model.
//  2. Assert max_tokens stays 8 and no min_tokens appears: a short answer is a request the protocol now prices and validates as it is.
func TestASmallMaxTokensIsNotRaised(t *testing.T) {
	for _, model := range []string{minimaxModelID, "Qwen/Test"} {
		t.Run(model, func(t *testing.T) {
			document := normalizedDocument(t, `{"messages":[{"role":"user","content":"x"}],"max_tokens":8}`, model)
			requireUintField(t, document, "max_tokens", 8)
			requireAbsent(t, document, "min_tokens")
		})
	}
}

// Test flow:
//  1. Normalize a request that sends max_completion_tokens 8 instead of max_tokens.
//  2. Assert max_tokens and max_completion_tokens both carry 8.
func TestASmallMaxCompletionTokensIsMirroredUnraised(t *testing.T) {
	document := normalizedDocument(t, `{"messages":[{"role":"user","content":"x"}],"max_completion_tokens":8}`, "Qwen/Test")
	requireUintField(t, document, "max_tokens", 8)
	requireUintField(t, document, "max_completion_tokens", 8)
}

// Test flow:
//  1. Normalize requests where one output budget field is tiny and the other large.
//  2. Assert the smaller one wins on both fields, unraised.
func TestTheSmallerOutputBudgetFieldWins(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"max_completion_tokens":16}`,
		`{"messages":[{"role":"user","content":"x"}],"max_tokens":16,"max_completion_tokens":4096}`,
	} {
		t.Run(body, func(t *testing.T) {
			document := normalizedDocument(t, body, "Qwen/Test")
			requireUintField(t, document, "max_tokens", 16)
			requireUintField(t, document, "max_completion_tokens", 16)
		})
	}
}

// Test flow:
//  1. Normalize a request with max_tokens 1 and capture the declared MaxTokens.
//  2. Assert the body's max_tokens matches the declared MaxTokens exactly, since the gateway signs its reservation against that same field.
func TestTheBodyAgreesWithTheDeclaredBudget(t *testing.T) {
	document, result, err := normalizeWith(t, `{"messages":[{"role":"user","content":"x"}],"max_tokens":1}`, "Qwen/Test", nil)
	if err != nil {
		t.Fatalf("NormalizeRequest() = %v, want nil", err)
	}
	if uint64(document["max_tokens"].(float64)) != result.MaxTokens || result.MaxTokens != 1 {
		t.Errorf("body max_tokens = %v, declared %d, want both 1", document["max_tokens"], result.MaxTokens)
	}
}

// Test flow:
//  1. Normalize a request where max_tokens and max_completion_tokens agree.
//  2. Assert both fields leave the normalizer agreeing on that value.
func TestBothOutputBudgetFieldsLeaveAgreeing(t *testing.T) {
	document := normalizedDocument(t, `{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"max_completion_tokens":200}`, "Qwen/Test")
	requireUintField(t, document, "max_tokens", 200)
	requireUintField(t, document, "max_completion_tokens", 200)
}

// Test flow:
//  1. Normalize a request whose stop_token_ids are all inside the model's vocabulary, with a min_tokens beside them.
//  2. Assert both are forwarded as sent: the ids reach the engine and cost the request nothing else.
func TestStopTokenIdsInsideTheVocabularyAreKept(t *testing.T) {
	document, _, err := normalizeWith(t, `{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"min_tokens":100,"stop_token_ids":[1,2,999]}`, "Qwen/Test", fixedVocabulary(testVocabularySize))
	if err != nil {
		t.Fatalf("NormalizeRequest() = %v, want nil", err)
	}
	ids, _ := document["stop_token_ids"].([]any)
	if len(ids) != 3 {
		t.Errorf("stop_token_ids = %v, want [1 2 999]", document["stop_token_ids"])
	}
	requireUintField(t, document, "min_tokens", 100)
}

// Test flow:
//  1. Normalize requests whose stop_token_ids hold an id past the vocabulary, a negative id, a non-numeric id, or a non-array value.
//  2. Assert each is rejected as a client error naming stop_token_ids: an out-of-vocabulary id asserts on the node.
func TestStopTokenIdsOutsideTheVocabularyAreRejected(t *testing.T) {
	for _, field := range []string{`[1000]`, `[-5]`, `["not-a-number"]`, `"nonsense"`} {
		t.Run(field, func(t *testing.T) {
			_, _, err := normalizeWith(t, `{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"stop_token_ids":`+field+`}`, "Qwen/Test", fixedVocabulary(testVocabularySize))
			if err == nil || ErrorStatus(err, 0) != http.StatusBadRequest || !errors.Is(err, completionapi.ErrStopTokenIDs) {
				t.Fatalf("NormalizeRequest() = %v, want a 400 wrapping completionapi.ErrStopTokenIDs", err)
			}
		})
	}
}

// Test flow:
//  1. Normalize a request carrying stop_token_ids with no vocabulary wired, and once with a vocabulary that answers unknown.
//  2. Assert both are rejected: without the size no id can be proven safe.
func TestStopTokenIdsAreRejectedWhenTheVocabularyIsUnknown(t *testing.T) {
	for name, vocabulary := range map[string]func(string) int{"unwired": nil, "unknown": fixedVocabulary(VocabularySizeUnknown)} {
		t.Run(name, func(t *testing.T) {
			_, _, err := normalizeWith(t, `{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"stop_token_ids":[1]}`, "Qwen/Test", vocabulary)
			if err == nil || !errors.Is(err, completionapi.ErrStopTokenIDs) {
				t.Fatalf("NormalizeRequest() = %v, want completionapi.ErrStopTokenIDs", err)
			}
		})
	}
}

// Test flow:
//  1. Normalize a request with no stop_token_ids, and one with an empty list, through a vocabulary that counts its lookups.
//  2. Assert both pass and the vocabulary was never asked: its lookup can block on the chain.
func TestTheVocabularyIsAskedOnlyForStopTokenIds(t *testing.T) {
	lookups := 0
	vocabulary := func(string) int {
		lookups++
		return testVocabularySize
	}
	for _, body := range []string{
		`{"messages":[{"role":"user","content":"x"}],"max_tokens":4096}`,
		`{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"stop_token_ids":[]}`,
	} {
		if _, _, err := normalizeWith(t, body, "Qwen/Test", vocabulary); err != nil {
			t.Fatalf("NormalizeRequest(%s) = %v, want nil", body, err)
		}
	}
	if lookups != 0 {
		t.Errorf("vocabulary looked up %d times, want 0", lookups)
	}
}
