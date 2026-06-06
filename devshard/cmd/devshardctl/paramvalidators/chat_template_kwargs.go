package paramvalidators

import (
	"errors"
	"fmt"
)

// ErrChatTemplateKwargsShape covers the wrapper-level rejection: chat_template_kwargs must
// be a JSON object. Bounds rejections (depth/nodes/size) come back as the shared ErrSchema*
// sentinels via ObjectBounds.
var ErrChatTemplateKwargsShape = errors.New("chat_template_kwargs: invalid wrapper shape")

// ErrChatTemplateKwargsForbiddenKey marks an attempt to set a top-level key that overrides
// `apply_hf_chat_template()`'s positional arguments (not a template variable). This is the
// CVE-2025-61620 / CVE-2025-62426 class -- e.g. `chat_template_kwargs: {"chat_template": "..."}`
// smuggles a malicious Jinja template, and `{"tokenize": true}` stalls the request handler
// in synchronous tokenization.
var ErrChatTemplateKwargsForbiddenKey = errors.New("chat_template_kwargs: forbidden key (overrides apply_hf_chat_template positional argument)")

func getOrCreateChatTemplateKwargs(document map[string]any) (map[string]any, error) {
	raw, exists := document["chat_template_kwargs"]
	if !exists {
		kwargs := map[string]any{}
		document["chat_template_kwargs"] = kwargs
		return kwargs, nil
	}
	kwargs, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: must be an object", ErrChatTemplateKwargsShape)
	}
	return kwargs, nil
}

// forbiddenChatTemplateKwargsKeys are top-level keys that, when set via
// chat_template_kwargs, override `apply_hf_chat_template(conversation, chat_template,
// add_generation_prompt, continue_final_message, tokenize, padding, truncation, max_length,
// return_tensors, return_dict, tools, documents, **kwargs)` positional arguments instead of
// becoming template variables. They are all known CVE vectors when reachable from client
// input. `add_generation_prompt` is intentionally allowed -- it is a legitimate knob clients
// sometimes pass and not a code-injection vector.
var forbiddenChatTemplateKwargsKeys = map[string]struct{}{
	"chat_template":          {}, // CVE-2025-61620: arbitrary Jinja template
	"tokenize":               {}, // CVE-2025-62426: stalls request handler
	"tools":                  {}, // overrides the request's tools list
	"documents":              {}, // overrides documents context
	"conversation":           {}, // overrides messages list
	"continue_final_message": {},
	"padding":                {},
	"truncation":             {},
	"max_length":             {},
	"return_tensors":         {},
	"return_dict":            {},
}

// ChatTemplateKwargsValidator bounds the depth/breadth/size of chat_template_kwargs before
// vLLM hands the object to Jinja's chat-template renderer, and rejects keys that override
// `apply_hf_chat_template()` positional arguments instead of feeding template variables.
// Unlike response_format.json_schema, the body is NOT a JSON Schema -- no $ref ban, no
// anyOf/enum semantics. Plain depth/nodes/size plus the forbidden-key filter.
type ChatTemplateKwargsValidator struct {
	MaxDepth int
	MaxSize  int
	MaxNodes int
}

func (v ChatTemplateKwargsValidator) Validate(vctx ValidatorContext) error {
	raw, exists := vctx.Document["chat_template_kwargs"]
	if !exists {
		return nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: must be an object", ErrChatTemplateKwargsShape)
	}
	for key := range obj {
		if _, forbidden := forbiddenChatTemplateKwargsKeys[key]; forbidden {
			return fmt.Errorf("%w: %q", ErrChatTemplateKwargsForbiddenKey, key)
		}
	}
	bounds := ObjectBounds{
		MaxDepth: v.MaxDepth,
		MaxSize:  v.MaxSize,
		MaxNodes: v.MaxNodes,
	}
	if err := bounds.Walk(obj); err != nil {
		return fmt.Errorf("chat_template_kwargs: %w", err)
	}
	if err := bounds.CheckSize(obj); err != nil {
		return fmt.Errorf("chat_template_kwargs: %w", err)
	}
	return nil
}
