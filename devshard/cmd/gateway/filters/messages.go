package filters

import (
	"fmt"
	"strings"
)

// See README.md, "Message hygiene".
const (
	roleDeveloper = "developer"
	roleSystem    = "system"
	roleUser      = "user"
	roleAssistant = "assistant"
	roleTool      = "tool"
	roleFunction  = "function"

	emptyToolResultContent = "<empty tool result>"
)

var (
	disallowedAgenticFields = []string{"tool_calls", "tool_call_id", "function_call"}

	messageRolePolicies = map[string]messageRolePolicy{
		roleDeveloper: {disallowedFields: disallowedAgenticFields},
		roleSystem:    {disallowedFields: disallowedAgenticFields},
		roleUser:      {disallowedFields: disallowedAgenticFields},
		roleAssistant: {disallowedFields: []string{"tool_call_id"}},
		roleTool:      {disallowedFields: []string{"tool_calls", "function_call"}, requireToolCallID: true},
		roleFunction:  {disallowedFields: disallowedAgenticFields, requireName: true},
	}

	messageNormalizerChain = []messageNormalizer{
		dropOrphanToolMessages,
		dropEmptyAssistantTurns,
		normalizeEmptyMessageContent,
		stripLegacyToolName,
		flattenMessageTextParts,
	}
)

type messageRolePolicy struct {
	disallowedFields  []string
	requireName       bool
	requireToolCallID bool
}

// messageNormalizer rewrites the messages array before validateMessages, reporting whether anything changed.
type messageNormalizer func(messages []any) ([]any, bool, error)

func normalizeMessages(document *Document) error {
	messages, ok := document.Array("messages")
	if !ok {
		return nil
	}
	changed := false
	for _, normalize := range messageNormalizerChain {
		rewritten, wasChanged, err := normalize(messages)
		if err != nil {
			return WrapReject(err)
		}
		if wasChanged {
			messages = rewritten
			changed = true
		}
	}
	if changed {
		document.Set("messages", messages)
	}
	return nil
}

// retainMessages copies the history only once something is dropped; a history nothing touches keeps its own array.
func retainMessages(messages []any, keep func(message map[string]any) bool) ([]any, bool) {
	retained, dropped := messages, false
	for index, raw := range messages {
		if message, isObject := raw.(map[string]any); isObject && !keep(message) {
			if !dropped {
				retained, dropped = append(make([]any, 0, len(messages)), messages[:index]...), true
			}
			continue
		}
		if dropped {
			retained = append(retained, raw)
		}
	}
	return retained, dropped
}

// dropOrphanToolMessages removes role:"tool" entries whose tool_call_id has no matching prior assistant tool_call.
func dropOrphanToolMessages(messages []any) ([]any, bool, error) {
	var pending map[string]struct{}
	retained, dropped := retainMessages(messages, func(message map[string]any) bool {
		switch role, _ := message["role"].(string); role {
		case roleAssistant:
			calls, isList := message["tool_calls"].([]any)
			if !isList {
				return true
			}
			for _, rawCall := range calls {
				call, isObject := rawCall.(map[string]any)
				if !isObject {
					continue
				}
				if id, isString := call["id"].(string); isString && id != "" {
					if pending == nil {
						pending = map[string]struct{}{}
					}
					pending[id] = struct{}{}
				}
			}
		case roleTool:
			id, isString := message["tool_call_id"].(string)
			if !isString || id == "" {
				return true
			}
			if _, matched := pending[id]; !matched {
				return false
			}
			delete(pending, id)
		}
		return true
	})
	return retained, dropped, nil
}

// dropEmptyAssistantTurns removes assistant messages with no content and no call -- placeholders some clients resend.
func dropEmptyAssistantTurns(messages []any) ([]any, bool, error) {
	retained, dropped := retainMessages(messages, func(message map[string]any) bool {
		role, _ := message["role"].(string)
		return role != roleAssistant || !isAssistantTurnEmpty(message)
	})
	return retained, dropped, nil
}

// normalizeEmptyMessageContent fills empty tool content with the sentinel, and nullifies empty assistant content carrying a call.
func normalizeEmptyMessageContent(messages []any) ([]any, bool, error) {
	changed := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		content, exists := message["content"]
		switch {
		case !presentField(content, exists):
			if role == roleTool {
				message["content"] = emptyToolResultContent
				changed = true
			}
		case isEmptyContent(content):
			switch role {
			case roleAssistant:
				_, hasToolCalls := message["tool_calls"]
				_, hasFunctionCall := message["function_call"]
				if hasToolCalls || hasFunctionCall {
					message["content"] = nil
					changed = true
				}
			case roleTool:
				message["content"] = emptyToolResultContent
				changed = true
			}
		}
	}
	return messages, changed, nil
}

// stripLegacyToolName drops the `name` field from role:"tool" messages, left over from the retired role:"function".
func stripLegacyToolName(messages []any) ([]any, bool, error) {
	changed := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := message["role"].(string); role != roleTool {
			continue
		}
		if _, exists := message["name"]; !exists {
			continue
		}
		delete(message, "name")
		changed = true
	}
	return messages, changed, nil
}

// flattenMessageTextParts joins {type:"text",text} parts into one string; other shapes are left for validateMessages.
func flattenMessageTextParts(messages []any) ([]any, bool, error) {
	changed := false
	for index, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		content, exists := message["content"]
		if !presentField(content, exists) {
			continue
		}
		parts, ok := content.([]any)
		if !ok {
			continue
		}
		combined, err := combineTextContentParts(parts)
		if err != nil {
			return nil, false, fmt.Errorf("messages[%d].content%w", index, err)
		}
		if combined != "" {
			message["content"] = combined
			changed = true
		}
	}
	return messages, changed, nil
}

func requiredTextContentPart(part map[string]any, partIndex int) (string, error) {
	partType, err := requiredNonEmptyStringField(part, "type")
	if err != nil {
		return "", fmt.Errorf("[%d].type: %w", partIndex, err)
	}
	if partType != "text" {
		return "", fmt.Errorf("[%d].type has unsupported value %q", partIndex, partType)
	}
	text, err := requiredNonEmptyStringField(part, "text")
	if err != nil {
		return "", fmt.Errorf("[%d].text: %w", partIndex, err)
	}
	return text, nil
}

func combineTextContentParts(parts []any) (string, error) {
	texts := make([]string, 0, len(parts))
	for partIndex, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			return "", fmt.Errorf("[%d] must be an object", partIndex)
		}
		text, err := requiredTextContentPart(part, partIndex)
		if err != nil {
			return "", err
		}
		texts = append(texts, text)
	}
	if len(texts) == 0 {
		return "", nil
	}
	return strings.Join(texts, "\n"), nil
}

// isEmptyContent reports a blank string or an empty parts array; nil means "missing" rather than "empty".
func isEmptyContent(content any) bool {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value) == ""
	case []any:
		return len(value) == 0
	default:
		return false
	}
}

func isAssistantTurnEmpty(message map[string]any) bool {
	if raw, exists := message["tool_calls"]; presentField(raw, exists) {
		if calls, ok := raw.([]any); ok && len(calls) > 0 {
			return false
		}
	}
	if raw, exists := message["function_call"]; presentField(raw, exists) {
		if functionCall, ok := raw.(map[string]any); ok && len(functionCall) > 0 {
			return false
		}
	}
	content, exists := message["content"]
	if !presentField(content, exists) {
		return true
	}
	return isEmptyContent(content)
}
