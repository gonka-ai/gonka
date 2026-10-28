package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
)

const (
	// Routed model ids the filter profiles dispatch on.
	minimaxModel = "MiniMaxAI/MiniMax-M2.7"
	kimiModel    = "moonshotai/Kimi-K2.6"

	// Above the threshold below which Kimi silences thinking.
	kimiThinkingMaxTokens = 1024

	kimiOutputTokensFloor = 16
)

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Read the session nonce before any traffic.
//  3. Send one completion carrying a parameter the table does not know.
//  4. Assert the gateway answers 400 and names the offending parameter.
//  5. Assert the session nonce did not move, so the refusal reserved nothing.
func TestE2E_GatewayRejectsAnUnsupportedParameterWithoutSpendingANonce(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	before := gatewaySessionNonce(t, client, env.clientURL)
	body := testutil.ChatCompletionBody("unsupported parameter", false)
	body["audio"] = map[string]any{"voice": "alloy", "format": "wav"}
	rejected := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey)

	if rejected.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unsupported parameter was answered %d %s, want 400", rejected.StatusCode, rejected.Body)
	}
	if !strings.Contains(rejected.Body, "audio") {
		t.Errorf("rejection = %s, want it to name the parameter the client has to remove", rejected.Body)
	}
	if after := gatewaySessionNonce(t, client, env.clientURL); after != before {
		t.Errorf("nonce moved %d -> %d on a request the filters refused", before, after)
	}
}

// Test flow:
//  1. Start the three-host environment with every host echoing the request it received.
//  2. Open the served model to callers without a key through the settings route.
//  3. Send one completion asking for more output tokens than the configured cap.
//  4. Assert the host received the request clamped to the cap.
//  5. Send the same body with the admin key.
//  6. Assert the host received the requested budget unclamped.
func TestE2E_GatewayCapsOutputTokensUntilTheAdminKeyLiftsIt(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{hostEnvOverrides: testutil.EchoingHosts(e2eHostCount)})
	putSettings(t, client, env.clientURL, map[string]any{"model_access": map[string]string{"stub-model": "open"}})

	const requested = 8192
	body := testutil.ChatCompletionBody("output token cap", false)
	body["max_tokens"] = requested

	capped := testutil.EchoedRequest(t, testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, ""))
	if got := capped["max_tokens"]; got != float64(e2eMaxTokensCap) {
		t.Errorf("an ordinary request reached the host with max_tokens = %v, want the cap %d", got, e2eMaxTokensCap)
	}

	lifted := testutil.EchoedRequest(t, testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey))
	if got := lifted["max_tokens"]; got != float64(requested) {
		t.Errorf("an admin request reached the host with max_tokens = %v, want the requested %d", got, requested)
	}
}

// Test flow:
//  1. Start the three-host environment with every host echoing the request it received.
//  2. Send one non-streaming completion asking for no logprobs.
//  3. Assert the host was asked to stream, with usage, and was sent no return_token_ids, which it sets itself.
//  4. Assert the host was sent no logprobs and no top_logprobs, because the gateway carries the client's ask rather than forcing one.
func TestE2E_GatewayForcesTheUpstreamShapeEveryHostSees(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{hostEnvOverrides: testutil.EchoingHosts(e2eHostCount)})

	body := testutil.ChatCompletionBody("forced upstream shape", false)
	received := testutil.EchoedRequest(t, testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey))

	if got := received["stream"]; got != true {
		t.Errorf("host received stream = %v, want the forced true", got)
	}
	for _, carried := range []string{"logprobs", "top_logprobs"} {
		if got, held := received[carried]; held {
			t.Errorf("host received %s = %v, want it absent: the client asked for none", carried, got)
		}
	}
	if got, held := received["return_token_ids"]; held {
		t.Errorf("host received return_token_ids = %v, want it absent: the host sets it itself", got)
	}
	options, ok := received["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("host received stream_options = %v, want the forced object", received["stream_options"])
	}
	if got := options["include_usage"]; got != true {
		t.Errorf("host received stream_options.include_usage = %v, want true", got)
	}
}

// Test flow:
//  1. Start the three-host environment serving the Kimi model, with every host echoing its request.
//  2. Send one completion asking for fewer output tokens than the Kimi floor.
//  3. Assert the host received max_tokens raised to that floor, and no min_tokens the client did not send.
func TestE2E_GatewayFloorsTheOutputBudgetBeforeAHostSeesIt(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{model: kimiModel, hostEnvOverrides: testutil.EchoingHosts(e2eHostCount)})

	body := testutil.ChatCompletionBody("below the floor", false)
	body["model"] = kimiModel
	body["max_tokens"] = 1
	received := testutil.EchoedRequest(t, testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey))

	if got := received["max_tokens"]; got != float64(kimiOutputTokensFloor) {
		t.Errorf("host received max_tokens = %v, want the floor %d", got, kimiOutputTokensFloor)
	}
	if got, held := received["min_tokens"]; held {
		t.Errorf("host received min_tokens = %v, want it absent: the client sent none", got)
	}
}

// Test flow:
//  1. Start the three-host environment with every host echoing the request it received.
//  2. Send one completion carrying top_k inside an extra_body envelope.
//  3. Assert the host received top_k at the top level and no envelope at all.
func TestE2E_GatewayUnwrapsExtraBodyBeforeAHostSeesIt(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{hostEnvOverrides: testutil.EchoingHosts(e2eHostCount)})

	const nestedTopK = 40
	body := testutil.ChatCompletionBody("enveloped parameter", false)
	body["extra_body"] = map[string]any{"top_k": nestedTopK}
	received := testutil.EchoedRequest(t, testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey))

	if got := received["top_k"]; got != float64(nestedTopK) {
		t.Errorf("host received top_k = %v, want the %d lifted out of extra_body", got, nestedTopK)
	}
	if _, present := received["extra_body"]; present {
		t.Errorf("host received extra_body = %v, want the envelope dropped", received["extra_body"])
	}
}

// Test flow:
//  1. Start the three-host environment serving the MiniMax model, with every host echoing its request.
//  2. Send one completion carrying reasoning_split and a thinking wrapper.
//  3. Assert the host received the caller's reasoning_split, which only this profile serves.
//  4. Assert the thinking wrapper was stripped, which only this profile does.
func TestE2E_GatewayAppliesTheRoutedModelsFilterProfile(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{model: minimaxModel, hostEnvOverrides: testutil.EchoingHosts(e2eHostCount)})

	body := testutil.ChatCompletionBody("routed profile", false)
	body["model"] = minimaxModel
	body["reasoning_split"] = false
	body["thinking"] = map[string]any{"type": "enabled"}
	received := testutil.EchoedRequest(t, testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey))

	if got, present := received["reasoning_split"]; !present || got != false {
		t.Errorf("host received reasoning_split = %v (present=%t), want the caller's false kept on the MiniMax route", got, present)
	}
	if _, present := received["thinking"]; present {
		t.Errorf("host received thinking = %v, want it stripped on the MiniMax route", received["thinking"])
	}
}

// Test flow:
//  1. Start the default three-host environment, with every host echoing its request.
//  2. Send one completion carrying reasoning_split on a route with no profile for it.
//  3. Assert the host received no reasoning_split at all.
func TestE2E_GatewayStripsAProfileFieldOnARouteThatCannotServeIt(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{hostEnvOverrides: testutil.EchoingHosts(e2eHostCount)})

	body := testutil.ChatCompletionBody("no profile", false)
	body["reasoning_split"] = false
	received := testutil.EchoedRequest(t, testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey))

	if _, present := received["reasoning_split"]; present {
		t.Errorf("host received reasoning_split = %v, want it stripped on a route with no profile for it", received["reasoning_split"])
	}
}

// Test flow:
//  1. Start the default three-host environment, with every host echoing its request.
//  2. Send one completion carrying reasoning_effort on a route with no profile.
//  3. Assert the host received the effort the caller asked for.
func TestE2E_GatewayForwardsReasoningEffortToEveryRoute(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{hostEnvOverrides: testutil.EchoingHosts(e2eHostCount)})

	const effort = "medium"
	body := testutil.ChatCompletionBody("reasoning effort", false)
	body["reasoning_effort"] = effort
	received := testutil.EchoedRequest(t, testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey))

	if got := received["reasoning_effort"]; got != effort {
		t.Errorf("host received reasoning_effort = %v, want the caller's %q forwarded", got, effort)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Send one completion asking for zero output tokens.
//  3. Assert the gateway answers 400 and names max_tokens.
func TestE2E_GatewayRefusesAZeroOutputBudget(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	body := testutil.ChatCompletionBody("zero budget", false)
	body["max_tokens"] = 0
	refused := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey)

	if refused.StatusCode != http.StatusBadRequest {
		t.Fatalf("a zero output budget was answered %d %s, want 400", refused.StatusCode, refused.Body)
	}
	if !strings.Contains(refused.Body, "max_tokens") {
		t.Errorf("refusal = %s, want it to name the field the caller has to fix", refused.Body)
	}
}

// Test flow:
//  1. Start the three-host environment serving the Kimi model, with every host echoing its request.
//  2. Send one completion asking for zero output tokens.
//  3. Assert the host received the zero raised to the Kimi floor rather than a refusal.
func TestE2E_GatewayLiftsAZeroOutputBudgetOnTheRouteThatAsksForIt(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{model: kimiModel, hostEnvOverrides: testutil.EchoingHosts(e2eHostCount)})

	body := testutil.ChatCompletionBody("zero budget lifted", false)
	body["model"] = kimiModel
	body["max_tokens"] = 0
	received := testutil.EchoedRequest(t, testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey))

	if got := received["max_tokens"]; got != float64(kimiOutputTokensFloor) {
		t.Errorf("host received max_tokens = %v, want the zero lifted to the floor %d", got, kimiOutputTokensFloor)
	}
}

// Test flow:
//  1. Start the three-host environment with every host answering with the internal fields present.
//  2. Send one completion asking for no logprobs.
//  3. Assert none of the six hidden fields reached the client.
func TestE2E_GatewayHidesEveryInternalFieldFromAClientThatAskedForNothing(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{hostEnvOverrides: testutil.HostsAnswering(e2eHostCount, testutil.LeakyHostAnswer)})

	body := testutil.ChatCompletionBody("nothing asked", false)
	served := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey)
	require.Equal(t, http.StatusOK, served.StatusCode, "completion should be served: %s", served.Body)

	for _, hidden := range []string{"logprob", "logprobs", "top_logprobs", "token_ids", "prompt_token_ids", "prompt_logprobs"} {
		if strings.Contains(served.Body, `"`+hidden+`"`) {
			t.Errorf("answer carried %q to a client that asked for nothing: %s", hidden, served.Body)
		}
	}
}

// Test flow:
//  1. Start the three-host environment with every host answering with the internal fields present.
//  2. Send one completion asking for logprobs.
//  3. Assert the logprobs reached the client.
//  4. Assert the three fields no client may see did not.
func TestE2E_GatewayReturnsTheLogprobsAClientAskedForAndNothingElse(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{hostEnvOverrides: testutil.HostsAnswering(e2eHostCount, testutil.LeakyHostAnswer)})

	body := testutil.ChatCompletionBody("logprobs asked", false)
	body["logprobs"] = true
	body["top_logprobs"] = 2
	served := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey)
	require.Equal(t, http.StatusOK, served.StatusCode, "completion should be served: %s", served.Body)

	if !strings.Contains(served.Body, `"logprobs"`) {
		t.Errorf("answer dropped the logprobs the client asked for: %s", served.Body)
	}
	for _, hidden := range []string{"token_ids", "prompt_token_ids", "prompt_logprobs"} {
		if strings.Contains(served.Body, `"`+hidden+`"`) {
			t.Errorf("answer carried %q, which no client may ever see: %s", hidden, served.Body)
		}
	}
}

// Test flow:
//  1. Start the three-host environment with every host echoing the request it received.
//  2. Send one completion whose only turn carries two text parts.
//  3. Assert the host received one turn whose content is the parts joined with a newline.
func TestE2E_GatewayFlattensMessagePartsBeforeAHostSeesIt(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{hostEnvOverrides: testutil.EchoingHosts(e2eHostCount)})

	body := testutil.ChatCompletionBody("flattened parts", false)
	body["messages"] = []map[string]any{{
		"role": "user",
		"content": []map[string]any{
			{"type": "text", "text": "hello"},
			{"type": "text", "text": "world"},
		},
	}}
	received := testutil.EchoedRequest(t, testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey))

	messages, ok := received["messages"].([]any)
	require.True(t, ok, "host should have received a messages array: %v", received["messages"])
	require.Len(t, messages, 1, "flattening should not change how many turns there are")
	turn, ok := messages[0].(map[string]any)
	require.True(t, ok, "a turn should be an object: %v", messages[0])
	if got := turn["content"]; got != "hello\nworld" {
		t.Errorf("host received content = %q, want the parts joined with a newline", got)
	}
}

// Test flow:
//  1. Start the three-host environment with every host echoing the request it received.
//  2. Send one completion carrying a tool turn answering a call the assistant never made.
//  3. Assert the host received every turn but that one.
func TestE2E_GatewayDropsAnOrphanToolTurnBeforeAHostSeesIt(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{hostEnvOverrides: testutil.EchoingHosts(e2eHostCount)})

	body := testutil.ChatCompletionBody("orphan tool turn", false)
	body["messages"] = []map[string]any{
		{"role": "user", "content": "q"},
		{"role": "assistant", "content": nil, "tool_calls": []map[string]any{
			{"id": "c1", "type": "function", "function": map[string]any{"name": "f", "arguments": "{}"}},
		}},
		{"role": "tool", "content": "answered", "tool_call_id": "c1"},
		{"role": "tool", "content": "orphan", "tool_call_id": "never_emitted"},
	}
	received := testutil.EchoedRequest(t, testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey))

	messages, ok := received["messages"].([]any)
	require.True(t, ok, "host should have received a messages array: %v", received["messages"])
	require.Len(t, messages, 3, "only the orphan tool turn should be gone: %v", messages)
	if strings.Contains(fmt.Sprint(messages), "never_emitted") {
		t.Errorf("host received the orphan tool turn: %v", messages)
	}
}

// Test flow:
//  1. Start the three-host environment serving the Kimi model, with every host echoing its request.
//  2. Send one completion carrying a thinking wrapper and a budget above the silencing threshold.
//  3. Assert the host received no top-level thinking field.
//  4. Assert the caller's answer arrived inside chat_template_kwargs instead.
func TestE2E_GatewayMirrorsThinkingIntoTemplateKwargsOnTheKimiRoute(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{model: kimiModel, hostEnvOverrides: testutil.EchoingHosts(e2eHostCount)})

	body := testutil.ChatCompletionBody("mirrored thinking", false)
	body["model"] = kimiModel
	body["max_tokens"] = kimiThinkingMaxTokens
	body["thinking"] = map[string]any{"type": "enabled"}
	received := testutil.EchoedRequest(t, testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey))

	if _, present := received["thinking"]; present {
		t.Errorf("host received a top-level thinking = %v, want it mirrored away", received["thinking"])
	}
	kwargs, ok := received["chat_template_kwargs"].(map[string]any)
	require.True(t, ok, "host should have received chat_template_kwargs: %v", received["chat_template_kwargs"])
	if got := kwargs["thinking"]; got != true {
		t.Errorf("host received chat_template_kwargs.thinking = %v, want the caller's enabled mirrored in", got)
	}
}

// Test flow:
//  1. Start the three-host environment serving the Kimi model, with every host echoing its request.
//  2. Send one completion with no thinking budget and a max_tokens above the silencing threshold.
//  3. Assert the host received a budget of half that max_tokens.
func TestE2E_GatewayInventsAThinkingBudgetOnTheRouteThatDeclaresOne(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{model: kimiModel, hostEnvOverrides: testutil.EchoingHosts(e2eHostCount)})

	body := testutil.ChatCompletionBody("invented budget", false)
	body["model"] = kimiModel
	body["max_tokens"] = kimiThinkingMaxTokens
	received := testutil.EchoedRequest(t, testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey))

	if got := received["thinking_token_budget"]; got != float64(kimiThinkingMaxTokens/2) {
		t.Errorf("host received thinking_token_budget = %v, want half of max_tokens (%d)", got, kimiThinkingMaxTokens/2)
	}
}

// Test flow:
//  1. Start the three-host environment serving the Kimi model.
//  2. Send one completion whose thinking budget is a string.
//  3. Assert the gateway answers 400 and names thinking_token_budget.
func TestE2E_GatewayRefusesANonNumericThinkingBudget(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{model: kimiModel})

	body := testutil.ChatCompletionBody("string budget", false)
	body["model"] = kimiModel
	body["thinking_token_budget"] = "99999999"
	refused := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey)

	if refused.StatusCode != http.StatusBadRequest {
		t.Fatalf("a string thinking budget was answered %d %s, want 400", refused.StatusCode, refused.Body)
	}
	if !strings.Contains(refused.Body, "thinking_token_budget") {
		t.Errorf("refusal = %s, want it to name the field the caller has to fix", refused.Body)
	}
}

// Test flow:
//  1. Start the three-host environment serving the Kimi model.
//  2. Send one completion carrying structured_outputs.
//  3. Assert the gateway answers 400 and points at response_format instead.
func TestE2E_GatewayRefusesStructuredOutputsOnTheRouteThatCannotServeThem(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{model: kimiModel})

	body := testutil.ChatCompletionBody("structured outputs", false)
	body["model"] = kimiModel
	body["structured_outputs"] = map[string]any{"type": "json_object"}
	refused := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey)

	if refused.StatusCode != http.StatusBadRequest {
		t.Fatalf("structured_outputs was answered %d %s, want 400", refused.StatusCode, refused.Body)
	}
	if !strings.Contains(refused.Body, "response_format") {
		t.Errorf("refusal = %s, want it to point at the field that works instead", refused.Body)
	}
}

// Test flow:
//  1. Start the three-host environment with every host answering with a bareword -Infinity logprob.
//  2. Send one completion asking for logprobs.
//  3. Assert the answer parses as JSON and carries null where the bareword was.
//  4. Assert every assigned nonce is still counted once.
func TestE2E_GatewayNormalisesANonFiniteNumberAHostWrote(t *testing.T) {
	const nonFiniteAnswer = `{"choices":[{"message":{"content":"stub"},"logprobs":{"content":[{"logprob":-Infinity,"top_logprobs":[]}]}}],"usage":{"prompt_tokens":80,"completion_tokens":40}}`
	env, client := startGatewayEnv(t, e2eEnvOptions{hostEnvOverrides: testutil.HostsAnswering(e2eHostCount, nonFiniteAnswer)})

	body := testutil.ChatCompletionBody("non-finite logprob", false)
	body["logprobs"] = true
	body["top_logprobs"] = 2
	served := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey)

	require.Equal(t, http.StatusOK, served.StatusCode, "completion should be served: %s", served.Body)
	require.NotNil(t, served.JSON, "answer should parse as JSON: %s", served.Body)
	if strings.Contains(served.Body, "Infinity") {
		t.Errorf("answer carried a bareword no JSON parser accepts: %s", served.Body)
	}
	if !strings.Contains(served.Body, `"logprob":null`) {
		t.Errorf("answer = %s, want the bareword rewritten to null rather than dropped", served.Body)
	}
	testutil.RequireNonceAccountingBalanced(t, gatewayLedger(t, client, env.statsURL))
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Send one completion reserving the full output-token cap.
//  3. Read the escrow balance once that reservation is outstanding.
//  4. Send a second completion, which is what carries the first one's finish.
//  5. Assert the settled request cost far less than it reserved.
func TestE2E_GatewayChargesForWhatWasGeneratedNotForWhatWasReserved(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	const reserved = e2eMaxTokensCap
	send := func(content string) {
		body := testutil.ChatCompletionBody(content, false)
		body["max_tokens"] = reserved
		testutil.RequireOpenAINonStreamingCompletion(t,
			testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey))
	}

	send("takes the reservation")
	settled := testutil.EscrowBalance(t, client, env.clientURL, defaultEscrowID, testutil.AdminAPIKey)
	send("returns the surplus")
	spent := settled - testutil.EscrowBalance(t, client, env.clientURL, defaultEscrowID, testutil.AdminAPIKey)

	if spent == 0 {
		t.Fatalf("balance stayed at %d across a served request: the escrow paid for nothing", settled)
	}
	if spent >= reserved {
		t.Errorf("a settled request cost %d, want well below the %d it reserved: the surplus never came back", spent, reserved)
	}
}
