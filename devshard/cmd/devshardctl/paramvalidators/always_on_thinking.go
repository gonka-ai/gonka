package paramvalidators

// AlwaysOnThinkingValidator reports thinking as on to vLLM's reasoning parser for a template that opens <think> on every turn, overruling any caller switch.
type AlwaysOnThinkingValidator struct{}

func (v AlwaysOnThinkingValidator) Validate(vctx ValidatorContext) error {
	chatTemplateKwargs, err := getOrCreateChatTemplateKwargs(vctx.Document)
	if err != nil {
		return err
	}
	chatTemplateKwargs["enable_thinking"] = true
	delete(chatTemplateKwargs, "thinking")
	return nil
}
