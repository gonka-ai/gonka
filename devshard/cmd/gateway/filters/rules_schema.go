package filters

import (
	"regexp"
	"strings"
)

// See README.md, "`structured_outputs`" and "`chat_template_kwargs`".
var (
	responseFormatNameRegex = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

	structuredOutputsConstraintFields = []string{"json", "regex", "choice", "grammar", "json_object", "structural_tag"}

	structuredOutputsAuxiliaryFields = []string{"whitespace_pattern", "disable_any_whitespace", "disable_additional_properties"}

	structuredOutputsPrivateFields = []string{"_backend", "_backend_was_auto"}

	structuredOutputsKnownFields = buildStructuredOutputsKnownFields()

	forbiddenChatTemplateKwargsKeys = map[string]struct{}{
		"chat_template":          {}, // CVE-2025-61620: arbitrary Jinja template
		"tokenize":               {}, // CVE-2025-62426: stalls the request handler
		"tools":                  {},
		"documents":              {},
		"conversation":           {},
		"continue_final_message": {},
		"padding":                {},
		"truncation":             {},
		"max_length":             {},
		"return_tensors":         {},
		"return_dict":            {},
	}
)

// validTools enforces the OpenAI tool contract plus tool_choice and min_tokens cross-field cleanup. See README.md, "`tools` and `tool_choice`".
func validTools(bounds SchemaBounds, defaultToolChoice string) RuleFunc {
	return func(ctx RuleContext) error {
		raw, exists := ctx.Document.Get("tools")
		if !exists {
			ctx.Document.Delete("tool_choice")
			return nil
		}
		if ctx.Profile.rejectsTools() {
			return Reject("tools: not supported by this model")
		}
		tools, ok := raw.([]any)
		if !ok {
			return Reject("tools: invalid array shape: must be an array")
		}
		if len(tools) == 0 {
			ctx.Document.Delete("tools")
			ctx.Document.Delete("tool_choice")
			return nil
		}
		if choice, _ := ctx.Document.Get("tool_choice"); choice == "required" {
			ctx.Document.Set("tool_choice", defaultToolChoice)
			ctx.Document.Delete("min_tokens")
		}
		if !ctx.Document.Has("tool_choice") && defaultToolChoice != "" {
			ctx.Document.Set("tool_choice", defaultToolChoice)
		}
		for index, item := range tools {
			tool, ok := item.(map[string]any)
			if !ok {
				return Reject("tools[i]: invalid tool shape: tools[%d] must be an object", index)
			}
			toolType, ok := tool["type"].(string)
			if !ok || toolType != "function" {
				return Reject("tools[i].type: must be \"function\" (tools[%d])", index)
			}
			function, ok := tool["function"].(map[string]any)
			if !ok {
				return Reject("tools[i].function: invalid wrapper shape: tools[%d].function must be an object", index)
			}
			name, ok := function["name"].(string)
			if !ok || strings.TrimSpace(name) == "" {
				return Reject("tools[i].function.name: must be a non-empty string (tools[%d])", index)
			}
			delete(function, "strict")
			parameters, ok := function["parameters"].(map[string]any)
			if !ok {
				continue
			}
			if err := bounds.Check(parameters); err != nil {
				return Reject("tools[%d].function.parameters: %v", index, err)
			}
		}
		return nil
	}
}

// validToolChoice accepts "auto" | "none" | {type:"function", function:{name}}; "required" is coerced by validTools first.
func validToolChoice(maxNameLen int) RuleFunc {
	return func(ctx RuleContext) error {
		raw, exists := ctx.Document.Get("tool_choice")
		if !exists {
			return nil
		}
		switch typed := raw.(type) {
		case string:
			if typed != "auto" && typed != "none" {
				return Reject("tool_choice: invalid value: must be \"auto\", \"none\", or a function object")
			}
			return nil
		case map[string]any:
			choiceType, _ := typed["type"].(string)
			if choiceType != "function" {
				return Reject("tool_choice.function: invalid shape: type must be \"function\"")
			}
			function, ok := typed["function"].(map[string]any)
			if !ok {
				return Reject("tool_choice.function: invalid shape: function must be an object")
			}
			name, ok := function["name"].(string)
			if !ok || strings.TrimSpace(name) == "" {
				return Reject("tool_choice.function: invalid shape: function.name must be a non-empty string")
			}
			if maxNameLen > 0 && len(name) > maxNameLen {
				return Reject("tool_choice.function: invalid shape: function.name length %d exceeds limit %d", len(name), maxNameLen)
			}
			return nil
		default:
			return Reject("tool_choice: invalid value: must be \"auto\", \"none\", or a function object")
		}
	}
}

// parallelToolCalls drops the field from a request without tools and otherwise requires a boolean. See README.md, "`tools` and `tool_choice`".
func parallelToolCalls() RuleFunc {
	validate := requireBool()
	return func(ctx RuleContext) error {
		if !ctx.Document.Has("tools") || ctx.Profile.stripsParallelToolCalls() {
			ctx.Document.Delete(ctx.Param)
			return nil
		}
		return validate(ctx)
	}
}

// validResponseFormat accepts text/json_object as-is; json_schema also needs a valid name and a bounded schema.
func validResponseFormat(bounds SchemaBounds, maxNameLen int) RuleFunc {
	return func(ctx RuleContext) error {
		raw, exists := ctx.Document.Get("response_format")
		if !exists {
			return nil
		}
		responseFormat, ok := raw.(map[string]any)
		if !ok {
			return Reject("response_format: invalid wrapper shape: must be an object")
		}
		responseType, ok := responseFormat["type"].(string)
		if !ok || strings.TrimSpace(responseType) == "" {
			return Reject("response_format.type: missing or unsupported: must be a non-empty string")
		}
		switch responseType {
		case "text", "json_object":
			return nil
		case "json_schema":
			if ctx.Profile.rejectsJSONSchema() {
				return Reject("response_format.type: json_schema is not supported by this model")
			}
			return validResponseFormatJSONSchema(responseFormat, bounds, maxNameLen)
		default:
			return Reject("response_format.type: missing or unsupported: %q is not supported (allowed: text, json_object, json_schema)", responseType)
		}
	}
}

func validResponseFormatJSONSchema(responseFormat map[string]any, bounds SchemaBounds, maxNameLen int) error {
	wrapper, ok := responseFormat["json_schema"].(map[string]any)
	if !ok {
		return Reject("response_format.json_schema: invalid wrapper shape: must be an object")
	}
	name, ok := wrapper["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return Reject("response_format.json_schema.name: invalid: must be a non-empty string")
	}
	if len(name) > maxNameLen {
		return Reject("response_format.json_schema.name: invalid: must be %d characters or fewer", maxNameLen)
	}
	if !responseFormatNameRegex.MatchString(name) {
		return Reject("response_format.json_schema.name: invalid: must match %s", responseFormatNameRegex.String())
	}
	schema, ok := wrapper["schema"].(map[string]any)
	if !ok {
		return Reject("response_format.json_schema.schema: invalid shape: must be an object")
	}
	if err := bounds.Check(schema); err != nil {
		return Reject("response_format.json_schema.schema: %v", err)
	}
	return nil
}

// validChatTemplateKwargs uses plain object bounds: this feeds a Jinja renderer, not a grammar compiler.
func validChatTemplateKwargs(bounds ObjectBounds) RuleFunc {
	return func(ctx RuleContext) error {
		raw, exists := ctx.Document.Get("chat_template_kwargs")
		if !exists {
			return nil
		}
		object, ok := raw.(map[string]any)
		if !ok {
			return Reject("chat_template_kwargs: invalid wrapper shape: must be an object")
		}
		for key := range object {
			if _, forbidden := forbiddenChatTemplateKwargsKeys[key]; forbidden {
				return Reject("chat_template_kwargs: forbidden key (overrides apply_hf_chat_template positional argument): %q", key)
			}
		}
		if err := bounds.Check(object); err != nil {
			return Reject("chat_template_kwargs: %v", err)
		}
		return nil
	}
}
