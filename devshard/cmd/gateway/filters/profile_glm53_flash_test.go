package filters

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Assert the GLM-5.3 Flash model ID is bound to glm53FlashProfile.
//  2. Assert every boolean hook (ForceZeroPenalties, RejectStructuredOutputs, AllowSafetyIdentifier, KeepReasoningSplit, EmptyAnswerCountsTokens) is false and it declares no output token floor.
//  3. Assert glm53FlashProfile.Thinking is ThinkingForceOn.
func TestGLM53FlashProfileHooks(t *testing.T) {
	require.Same(t, glm53FlashProfile, BuiltInProfileFor(glm53FlashModelID))

	boolHooks := []struct {
		name string
		got  bool
	}{
		{"ForceZeroPenalties", glm53FlashProfile.ForceZeroPenalties},
		{"RejectStructuredOutputs", glm53FlashProfile.RejectStructuredOutputs},
		{"AllowSafetyIdentifier", glm53FlashProfile.AllowSafetyIdentifier},
		{"KeepReasoningSplit", glm53FlashProfile.KeepReasoningSplit},
		{"EmptyAnswerCountsTokens", glm53FlashProfile.EmptyAnswerCountsTokens},
	}
	for _, hook := range boolHooks {
		require.Falsef(t, hook.got, "glm53FlashProfile.%s", hook.name)
	}
	require.Zero(t, glm53FlashProfile.OutputTokensFloor)
	require.Equal(t, ThinkingForceOn, glm53FlashProfile.Thinking)
}

// Test flow:
//  1. Normalize a GLM-5.3 Flash request whose caller signals thinking should be off, varying how: via chat_template_kwargs enable_thinking or thinking, a top-level enable_thinking, reasoning_effort "none", or reasoning.enabled false.
//  2. Assert the normalized chat_template_kwargs reports enable_thinking as true while keeping the caller's other kwargs as sent.
//  3. Assert no top-level enable_thinking field reaches the document.
func TestGLM53FlashReportsThinkingOnWhateverTheCallerSent(t *testing.T) {
	testCases := []struct {
		name               string
		body               string
		chatTemplateKwargs map[string]any
	}{
		{name: "chat_template_kwargs enable_thinking false", body: `{"messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":false}}`, chatTemplateKwargs: map[string]any{"enable_thinking": true}},
		{name: "chat_template_kwargs thinking false", body: `{"messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"thinking":false}}`, chatTemplateKwargs: map[string]any{"thinking": false, "enable_thinking": true}},
		{name: "top-level enable_thinking false", body: `{"messages":[{"role":"user","content":"hi"}],"enable_thinking":false}`, chatTemplateKwargs: map[string]any{"enable_thinking": true}},
		{name: "reasoning_effort none", body: `{"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`, chatTemplateKwargs: map[string]any{"enable_thinking": true}},
		{name: "reasoning enabled false", body: `{"messages":[{"role":"user","content":"hi"}],"reasoning":{"enabled":false}}`, chatTemplateKwargs: map[string]any{"enable_thinking": true}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			document := normalizedDocument(t, testCase.body, glm53FlashModelID)

			require.Equal(t, testCase.chatTemplateKwargs, document["chat_template_kwargs"],
				"enable_thinking must reach vLLM as true, with the caller's other kwargs as sent")
			require.NotContains(t, document, "enable_thinking")
		})
	}
}

// Test flow:
//  1. Normalize a GLM-5.3 Flash request with chat_template_kwargs setting enable_thinking false and clear_thinking true.
//  2. Assert the normalized chat_template_kwargs forces enable_thinking to true while keeping clear_thinking as sent.
func TestGLM53FlashKeepsTheCallersOtherTemplateKwargs(t *testing.T) {
	document := normalizedDocument(t, `{"messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":false,"clear_thinking":true}}`, glm53FlashModelID)

	require.Equal(t, map[string]any{"enable_thinking": true, "clear_thinking": true}, document["chat_template_kwargs"])
}

// Test flow:
//  1. Normalize a GLM-5.3 Flash request with reasoning_effort set to "none".
//  2. Assert the normalized document keeps reasoning_effort as "none".
func TestGLM53FlashLeavesTheCallersReasoningEffortAsSent(t *testing.T) {
	document := normalizedDocument(t, `{"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`, glm53FlashModelID)

	require.Equal(t, "none", document["reasoning_effort"])
}

// Test flow:
//  1. Normalize a GLM-5.2-FP8 request with chat_template_kwargs setting enable_thinking false.
//  2. Assert the normalized chat_template_kwargs keeps enable_thinking false, since GLM-5.3 Flash's force-on hook does not apply to this model.
func TestGLM52FP8KeepsTheCallersEnableThinkingFalse(t *testing.T) {
	document := normalizedDocument(t, `{"messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":false}}`, "zai-org/GLM-5.2-FP8")

	require.Equal(t, map[string]any{"enable_thinking": false}, document["chat_template_kwargs"])
}

// Test flow:
//  1. Normalize a GLM-5.3 Flash request carrying a top-level thinking object of type "disabled".
//  2. Assert the normalized document keeps the thinking object unchanged, the same as the default route would.
//  3. Assert chat_template_kwargs still forces enable_thinking to true.
func TestGLM53FlashNormalizesATopLevelThinkingObjectLikeTheDefaultRoute(t *testing.T) {
	document := normalizedDocument(t, `{"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`, glm53FlashModelID)

	require.Equal(t, map[string]any{"type": "disabled"}, document["thinking"])
	require.Equal(t, map[string]any{"enable_thinking": true}, document["chat_template_kwargs"])
}
