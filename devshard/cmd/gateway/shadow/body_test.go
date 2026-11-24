package shadow

import (
	"bytes"
	"maps"
	"reflect"
	"testing"

	json "github.com/goccy/go-json"

	"devshard"
	"devshard/cmd/gateway/scheduler"
	"devshard/user"
)

const (
	clientBody      = `{"max_tokens":50,"messages":[{"content":"summarise the quarterly report","role":"user"}],"model":"model-a","stream":true,"stream_options":{"include_usage":true},"temperature":0.2}`
	otherClientBody = `{"max_tokens":50,"messages":[{"content":"translate the memo into French","role":"user"}],"model":"model-a","stream":true,"stream_options":{"include_usage":true},"temperature":0.2}`
	drawCount       = 3000
	minimumDistinct = 100
	probeStartedAt  = 1_700_000_123

	smallPromptTokens = 12
)

var roomyBounds = fixedBounds{probeModel: {OutputCap: 4096, ContextLength: 32768}}

func clientRequest(body string) user.InferenceParams {
	return user.InferenceParams{Model: probeModel, Prompt: []byte(body), InputLength: uint64(len(body)), MaxTokens: 50, StartedAt: 1}
}

func decoded(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var members map[string]any
	if err := json.Unmarshal(body, &members); err != nil {
		t.Fatalf("body %q is not JSON: %v", body, err)
	}
	return members
}

func queuedProfile(params any) *scheduler.RequestProfile {
	return &scheduler.RequestProfile{Model: probeModel, InputTokens: smallPromptTokens, Params: params}
}

func probeParams(t *testing.T, request any) user.InferenceParams {
	t.Helper()
	params, skip := New(steppedClock(), roomyBounds).Params(queuedProfile(request), probeStartedAt)
	if skip != scheduler.ProbeSkipNone {
		t.Fatalf("skip = %v for %v, want a probe", skip, request)
	}
	probe, ok := params.(user.InferenceParams)
	if !ok {
		t.Fatalf("params are %T, want user.InferenceParams", params)
	}
	return probe
}

// Test flow:
//  1. Build probe params from a client request that capped its answer at 50 tokens.
//  2. Assert every member of the probe body equals the client's except `max_tokens` and `min_tokens`, which are both the one drawn value.
//  3. Assert the params carry the model, the body's byte length as `InputLength`, the drawn value as `MaxTokens` and the given start time.
//  4. Assert the client's own body was not touched.
func TestProbeBodyIsTheClientBodyWithTheDrawnTokenBudget(t *testing.T) {
	t.Parallel()
	client := clientRequest(clientBody)
	before := bytes.Clone(client.Prompt)

	probe := probeParams(t, client)

	want := decoded(t, client.Prompt)
	got := decoded(t, probe.Prompt)
	drawn := got["max_tokens"]
	want["max_tokens"], want["min_tokens"] = drawn, drawn
	if drawn != got["min_tokens"] || !maps.EqualFunc(got, want, func(a, b any) bool { return string(mustEncode(t, a)) == string(mustEncode(t, b)) }) {
		t.Errorf("probe body = %s, want the client's %s with max_tokens and min_tokens both %v", probe.Prompt, clientBody, drawn)
	}
	if probe.Model != probeModel || probe.InputLength != uint64(len(probe.Prompt)) || float64(probe.MaxTokens) != drawn || probe.StartedAt != probeStartedAt {
		t.Errorf("params = %+v, want the model, InputLength = len(Prompt), MaxTokens = %v and StartedAt %d", probe, drawn, probeStartedAt)
	}
	if !bytes.Equal(client.Prompt, before) {
		t.Errorf("client body = %s after the probe was built, want it unchanged", client.Prompt)
	}
}

func mustEncode(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %v: %v", value, err)
	}
	return encoded
}

// Test flow:
//  1. Build probe params from two client requests with different prompts.
//  2. Assert the probe bodies differ and each still carries its own client's prompt.
//  3. Assert the two probes' prompt hashes differ, which is what the nonce commits to.
func TestProbesOfTwoClientRequestsDiffer(t *testing.T) {
	t.Parallel()
	first := probeParams(t, clientRequest(clientBody))
	second := probeParams(t, clientRequest(otherClientBody))

	if bytes.Equal(first.Prompt, second.Prompt) {
		t.Fatalf("both probes are %s, want bodies that follow their clients", first.Prompt)
	}
	if !bytes.Contains(first.Prompt, []byte("quarterly report")) || !bytes.Contains(second.Prompt, []byte("memo into French")) {
		t.Errorf("probe bodies %s and %s, want each to carry its client's prompt", first.Prompt, second.Prompt)
	}
	firstHash, _ := devshard.CanonicalPromptHash(first.Prompt)
	secondHash, _ := devshard.CanonicalPromptHash(second.Prompt)
	if bytes.Equal(firstHash, secondHash) {
		t.Error("both probes commit to one prompt hash, want one per client request")
	}
}

// Test flow:
//  1. Build probe params many times from one client request.
//  2. Assert in every body `min_tokens` equals `max_tokens` and lies in the declared bounds, and equals the params' `MaxTokens`.
//  3. Assert the draws are not constant.
func TestProbeTokenBudgetIsDrawnWithinTheBounds(t *testing.T) {
	t.Parallel()
	client := clientRequest(clientBody)
	distinct := map[uint64]bool{}

	for range drawCount {
		probe := probeParams(t, client)
		members := decoded(t, probe.Prompt)
		maxTokens, minTokens := uint64(members["max_tokens"].(float64)), uint64(members["min_tokens"].(float64))
		if minTokens != maxTokens || maxTokens != probe.MaxTokens || maxTokens < ProbeMinOutputTokens || maxTokens > ProbeMaxOutputTokens {
			t.Fatalf("max_tokens %d, min_tokens %d, MaxTokens %d, want one value in [%d, %d]", maxTokens, minTokens, probe.MaxTokens, ProbeMinOutputTokens, ProbeMaxOutputTokens)
		}
		distinct[maxTokens] = true
	}

	if len(distinct) < minimumDistinct {
		t.Errorf("%d distinct budgets in %d draws, want at least %d", len(distinct), drawCount, minimumDistinct)
	}
}

// Test flow:
//  1. Build probe params from a client request that sent `max_completion_tokens` too, a larger budget than the probe's, and no `min_tokens`.
//  2. Assert all three members carry the one drawn value.
//  3. Build them from a client body that did not send `max_completion_tokens` and assert none is introduced.
func TestProbeRewritesMaxCompletionTokensOnlyWhereTheClientSentIt(t *testing.T) {
	t.Parallel()
	withField := `{"max_completion_tokens":4096,"max_tokens":4096,"messages":[{"content":"hi","role":"user"}],"model":"model-a"}`

	probe := decoded(t, probeParams(t, clientRequest(withField)).Prompt)
	plain := decoded(t, probeParams(t, clientRequest(clientBody)).Prompt)

	if probe["max_completion_tokens"] != probe["max_tokens"] || probe["min_tokens"] != probe["max_tokens"] {
		t.Errorf("probe = %v, want max_completion_tokens, max_tokens and min_tokens equal", probe)
	}
	if _, present := plain["max_completion_tokens"]; present {
		t.Errorf("probe = %v, want no max_completion_tokens the client never sent", plain)
	}
}

// Test flow:
//  1. Build probe params from a client body that asked for a reply in one piece, with no usage.
//  2. Assert the probe body streams with `include_usage`, so the first byte can be timed and the tokens counted.
func TestProbeStreamsWithUsageWhateverTheClientSent(t *testing.T) {
	t.Parallel()
	body := `{"max_tokens":50,"messages":[{"content":"hi","role":"user"}],"model":"model-a","stream":false}`

	members := decoded(t, probeParams(t, clientRequest(body)).Prompt)

	options, _ := members["stream_options"].(map[string]any)
	if members["stream"] != true || options["include_usage"] != true {
		t.Errorf("probe = %v, want stream true with include_usage true", members)
	}
}

// Test flow:
//  1. For each grammar-constrained client body (json_object, json_schema, a required tool choice, a named tool choice, structured outputs), each sending `min_tokens` and `max_completion_tokens`, build probe params.
//  2. Assert each is repeated: no skip, `stream` on with `include_usage`, `max_tokens` and `max_completion_tokens` the drawn value that `MaxTokens` carries, no `min_tokens`, and the constraint kept as the client sent it.
func TestGrammarConstrainedRequestIsRepeatedWithoutMinTokens(t *testing.T) {
	t.Parallel()
	constraints := map[string]string{
		"json_object":        `"response_format":{"type":"json_object"}`,
		"json_schema":        `"response_format":{"type":"json_schema"}`,
		"required tool":      `"tool_choice":"required"`,
		"named tool":         `"tool_choice":{"function":{"name":"lookup"},"type":"function"}`,
		"structured outputs": `"structured_outputs":{"regex":"[a-z]+"}`,
	}
	for name, constraint := range constraints {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := `{"max_completion_tokens":4096,"max_tokens":4096,"messages":[{"content":"hi","role":"user"}],"min_tokens":30,"model":"model-a",` + constraint + `}`

			probe := probeParams(t, clientRequest(body))

			members := decoded(t, probe.Prompt)
			options, _ := members["stream_options"].(map[string]any)
			if _, present := members["min_tokens"]; present {
				t.Errorf("probe = %s, want no min_tokens on a grammar-constrained request", probe.Prompt)
			}
			if members["max_tokens"] != float64(probe.MaxTokens) || members["max_completion_tokens"] != float64(probe.MaxTokens) {
				t.Errorf("probe = %s, want max_tokens and max_completion_tokens both the drawn %d", probe.Prompt, probe.MaxTokens)
			}
			if members["stream"] != true || options["include_usage"] != true {
				t.Errorf("probe = %s, want stream on with include_usage", probe.Prompt)
			}
			for key, want := range decoded(t, []byte("{"+constraint+"}")) {
				if !reflect.DeepEqual(members[key], want) {
					t.Errorf("probe = %s, want %s kept as %v", probe.Prompt, key, want)
				}
			}
		})
	}
}

// Test flow:
//  1. Build probe params from a client body that carries tools with `tool_choice` auto and a `min_tokens`, the shape of a body whose `required` choice the filters relaxed.
//  2. Assert it is repeated: no skip, the tools kept, `stream` on with `include_usage`, `max_tokens` the drawn value that `MaxTokens` carries, and no `min_tokens`.
func TestToolsRequestIsRepeatedWithoutMinTokens(t *testing.T) {
	t.Parallel()
	body := `{"messages":[{"content":"hi","role":"user"}],"min_tokens":30,"model":"model-a","tool_choice":"auto","tools":[{"function":{"name":"lookup"},"type":"function"}]}`

	probe := probeParams(t, clientRequest(body))

	members := decoded(t, probe.Prompt)
	options, _ := members["stream_options"].(map[string]any)
	if _, kept := members["tools"]; !kept {
		t.Errorf("probe = %s, want the tools kept", probe.Prompt)
	}
	if _, present := members["min_tokens"]; present {
		t.Errorf("probe = %s, want no min_tokens on a request that carries tools", probe.Prompt)
	}
	if members["max_tokens"] != float64(probe.MaxTokens) {
		t.Errorf("probe = %s, want max_tokens the drawn %d", probe.Prompt, probe.MaxTokens)
	}
	if members["stream"] != true || options["include_usage"] != true {
		t.Errorf("probe = %s, want stream on with include_usage", probe.Prompt)
	}
}

// Test flow:
//  1. Ask for probe params with no request at all, with params of another type, and with a body that is not JSON.
//  2. Assert each is skipped as having no request to repeat and builds no params.
func TestNoClientRequestGivesNoProbe(t *testing.T) {
	t.Parallel()
	requests := map[string]*scheduler.RequestProfile{
		"nil":        nil,
		"wrong type": queuedProfile("payload"),
		"not json":   queuedProfile(user.InferenceParams{Prompt: []byte("plain text")}),
	}
	for name, request := range requests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			params, skip := New(steppedClock(), roomyBounds).Params(request, probeStartedAt)

			if skip != scheduler.ProbeSkipNoRequest || params != nil {
				t.Errorf("params = %v, skip = %v, want no params and ProbeSkipNoRequest", params, skip)
			}
		})
	}
}
