package types

import "slices"

// Model ids of the network's served models whose counts the challenge path knows.
const (
	DeepSeekV4FlashModelID = "deepseek-ai/DeepSeek-V4-Flash-0731"
	MiniMaxM27ModelID      = "MiniMaxAI/MiniMax-M2.7"
	GLM53FlashModelID      = "zai-org/GLM-5.3-Flash"
)

// UsageAllowance is how far an honest answer of a model may count past what the gateway and the
// voters can see of it. The numbers come from answers recorded through vLLM's own serving code on
// the ML node builds the network runs (3.0.16, 3.1.0, 4.0.0); testenv/fixturegen rebuilds them.
type UsageAllowance struct {
	// ServiceTokens are generated tokens no client sees: the stop token that closes the turn and the
	// markup the template puts around it.
	ServiceTokens uint64
	// ToolCallHeader is the markup a tool parser strips from each call before the client gets its
	// name and arguments.
	ToolCallHeader uint64
	// ArgumentHeader is the markup a tool parser strips from each top-level argument of a call, past
	// what the argument's own bytes cover. DeepSeek-V4 writes each argument as a DSML parameter
	// block of about 16 tokens; a short non-string argument (`"a":0,`) is 6 bytes, and the bound's
	// factor of 2 leaves 4 tokens of it uncovered.
	ArgumentHeader uint64
	// PromptTemplate bounds the prompt tokens the chat template adds to a request of a few bytes:
	// role markers, a default system prompt, a reasoning preamble.
	PromptTemplate uint64
	// InputSpread is how many prompt tokens honest builds may differ by on one request. DeepSeek-V4
	// on 3.1.0 and 4.0.0 adds a reasoning-effort preamble of 79 tokens (91 in a tool loop) that 3.0.16
	// does not, so a voter's render of another build can be that far from an honest claim.
	InputSpread uint64
	// ThinkingKwarg is the chat template option that turns the model's reasoning on, for a model
	// whose builds differ in the mode they default to. DeepSeek-V4 reasons by default on 3.1.0 and
	// 4.0.0 and answers in chat mode on 3.0.16, whose template then drops the reasoning of a turn.
	ThinkingKwarg string
}

var usageAllowances = map[string]UsageAllowance{
	DeepSeekV4FlashModelID: {ServiceTokens: 8, ToolCallHeader: 14, ArgumentHeader: 3, PromptTemplate: 128, InputSpread: 96, ThinkingKwarg: "thinking"},
	MiniMaxM27ModelID:      {ServiceTokens: 8, ToolCallHeader: 9, PromptTemplate: 64, InputSpread: 8},
	GLM53FlashModelID:      {ServiceTokens: 8, ToolCallHeader: 0, PromptTemplate: 32, InputSpread: 8},
}

// UsageAllowanceModels lists the models with an allowance, sorted.
func UsageAllowanceModels() []string {
	models := make([]string, 0, len(usageAllowances))
	for model := range usageAllowances {
		models = append(models, model)
	}
	slices.Sort(models)
	return models
}

// UsageAllowanceFor is the allowance of a model; a model the table does not know has none, and
// the checks that need it are not applied to it.
func UsageAllowanceFor(model string) (UsageAllowance, bool) {
	allowance, ok := usageAllowances[model]
	return allowance, ok
}

// OutputByteBound is the most output tokens the gateway takes for a full bound stream that carried
// textBytes of text. A text token decodes to at least one byte once its character is complete, so
// the bytes bound the tokens even when a sampler spells every character byte by byte; the bound
// takes twice that, plus the tokens the client never sees: service tokens, the longest stop string
// (stopBytes), a header per tool call and one per top-level argument. maxTokens caps it, since an engine stops there. A model
// without an allowance gets maxTokens alone. Tokens a client forces with a positive logit_bias onto
// a byte fragment decode to nothing and can pass the bound; the gateway rejects logit_bias.
func OutputByteBound(model string, textBytes, stopBytes, toolCalls, arguments, maxTokens uint64) uint64 {
	allowance, ok := UsageAllowanceFor(model)
	if !ok {
		return maxTokens
	}
	bound := 2 * (textBytes + allowance.ServiceTokens + stopBytes + toolCalls*allowance.ToolCallHeader + arguments*allowance.ArgumentHeader)
	if maxTokens > 0 && maxTokens < bound {
		return maxTokens
	}
	return bound
}

// InputByteBound is the most input tokens the gateway takes for a text-only request of inputLength
// bytes: twice its bytes plus the template's own tokens. Zero means no bound: a model without an
// allowance. A request with non-text parts gets no bound, as an image costs more tokens than its
// bytes.
func InputByteBound(model string, inputLength uint64) uint64 {
	allowance, ok := UsageAllowanceFor(model)
	if !ok {
		return 0
	}
	return 2 * (inputLength + allowance.PromptTemplate)
}
