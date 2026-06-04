package main

import (
	"fmt"
	"strings"
)

type MessageRole string

const (
	MessageRoleDeveloper MessageRole = "developer"
	MessageRoleSystem    MessageRole = "system"
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleTool      MessageRole = "tool"
	MessageRoleFunction  MessageRole = "function"
)

type MessageContentRule int

const (
	MessageContentRequired MessageContentRule = iota
	MessageContentOptionalWithCalls
)

type MessageRolePolicy struct {
	Role              MessageRole
	DisallowedFields  []string
	RequireName       bool
	RequireToolCallID bool
	ContentRule       MessageContentRule
}

type ChatMessageProcessor struct {
	roles map[string]MessageRolePolicy
}

var defaultMessageProcessor = defaultChatMessageProcessor()

func defaultChatMessageProcessor() ChatMessageProcessor {
	policies := []MessageRolePolicy{
		{Role: MessageRoleDeveloper, DisallowedFields: []string{"tool_calls", "tool_call_id", "function_call"}, ContentRule: MessageContentRequired},
		{Role: MessageRoleSystem, DisallowedFields: []string{"tool_calls", "tool_call_id", "function_call"}, ContentRule: MessageContentRequired},
		{Role: MessageRoleUser, DisallowedFields: []string{"tool_calls", "tool_call_id", "function_call"}, ContentRule: MessageContentRequired},
		{Role: MessageRoleAssistant, DisallowedFields: []string{"tool_call_id"}, ContentRule: MessageContentOptionalWithCalls},
		{Role: MessageRoleTool, DisallowedFields: []string{"tool_calls", "function_call"}, RequireToolCallID: true, ContentRule: MessageContentRequired},
		{Role: MessageRoleFunction, DisallowedFields: []string{"tool_calls", "tool_call_id", "function_call"}, RequireName: true, ContentRule: MessageContentRequired},
	}
	byRole := make(map[string]MessageRolePolicy, len(policies))
	for _, policy := range policies {
		byRole[string(policy.Role)] = policy
	}
	return ChatMessageProcessor{roles: byRole}
}

// NormalizeDocument only fixes shapes we intentionally accept, for example empty tool content or text-part arrays.
func (p ChatMessageProcessor) NormalizeDocument(document *ChatRequestDocument) error {
	messages, ok := document.Array("messages")
	if !ok {
		return nil
	}
	changed := false
	if filtered, dropped := p.dropOrphanToolMessages(messages); dropped {
		messages = filtered
		changed = true
	}
	if filtered, dropped := p.dropEmptyAssistantTurns(messages); dropped {
		messages = filtered
		changed = true
	}
	for index, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		if p.normalizeMissingContent(message, role) {
			changed = true
		}
		if p.normalizeEmptyContent(message, role) {
			changed = true
		}
		if p.normalizeStripLegacyToolName(message, role) {
			changed = true
		}
		content, exists := message["content"]
		if !exists || content == nil {
			continue
		}
		parts, ok := content.([]any)
		if !ok {
			continue
		}
		combined, err := combineTextContentParts(parts, index)
		if err != nil {
			return err
		}
		if combined != "" {
			message["content"] = combined
			changed = true
		}
	}
	if changed {
		document.Set("messages", messages)
	}
	return nil
}

// Drops role:"tool" entries whose tool_call_id has no matching prior assistant.tool_call.
// Mirrors ValidateDocument's pending-set accounting so survivors pass the strict check.
func (p ChatMessageProcessor) dropOrphanToolMessages(messages []any) ([]any, bool) {
	pending := map[string]struct{}{}
	filtered := make([]any, 0, len(messages))
	dropped := false
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			filtered = append(filtered, raw)
			continue
		}
		role, _ := msg["role"].(string)
		switch role {
		case string(MessageRoleAssistant):
			if calls, ok := msg["tool_calls"].([]any); ok {
				for _, rawCall := range calls {
					call, ok := rawCall.(map[string]any)
					if !ok {
						continue
					}
					if id, ok := call["id"].(string); ok && id != "" {
						pending[id] = struct{}{}
					}
				}
			}
		case string(MessageRoleTool):
			if id, ok := msg["tool_call_id"].(string); ok && id != "" {
				if _, matched := pending[id]; !matched {
					dropped = true
					continue
				}
				delete(pending, id)
			}
		}
		filtered = append(filtered, raw)
	}
	return filtered, dropped
}

// Drops role:"assistant" messages with no content and no tool_calls/function_call —
// informationless placeholders left by session-resume serializers.
func (p ChatMessageProcessor) dropEmptyAssistantTurns(messages []any) ([]any, bool) {
	filtered := make([]any, 0, len(messages))
	dropped := false
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			filtered = append(filtered, raw)
			continue
		}
		if role, _ := msg["role"].(string); role == string(MessageRoleAssistant) && isAssistantTurnEmpty(msg) {
			dropped = true
			continue
		}
		filtered = append(filtered, raw)
	}
	return filtered, dropped
}

func isAssistantTurnEmpty(msg map[string]any) bool {
	if raw, exists := msg["tool_calls"]; exists && raw != nil {
		if calls, ok := raw.([]any); ok && len(calls) > 0 {
			return false
		}
	}
	if raw, exists := msg["function_call"]; exists && raw != nil {
		if fc, ok := raw.(map[string]any); ok && len(fc) > 0 {
			return false
		}
	}
	content, exists := msg["content"]
	if !exists || content == nil {
		return true
	}
	return isEmptyContent(content)
}

// Strips legacy `name` from role:"tool" messages (artifact of the old role:"function" API).
func (p ChatMessageProcessor) normalizeStripLegacyToolName(message map[string]any, role string) bool {
	if role != string(MessageRoleTool) {
		return false
	}
	if _, exists := message["name"]; !exists {
		return false
	}
	delete(message, "name")
	return true
}

func (p ChatMessageProcessor) normalizeMissingContent(message map[string]any, role string) bool {
	if _, exists := message["content"]; exists {
		return false
	}
	if role == string(MessageRoleTool) {
		message["content"] = emptyToolResultContent
		return true
	}
	return false
}

// Empty assistant content (string/array/null) is allowed only when a call payload is present;
// empty tool content is normalized to a sentinel string.
func (p ChatMessageProcessor) normalizeEmptyContent(message map[string]any, role string) bool {
	content, exists := message["content"]
	if !exists {
		return false
	}
	if content == nil {
		if role == string(MessageRoleTool) {
			message["content"] = emptyToolResultContent
			return true
		}
		return false
	}
	if !isEmptyContent(content) {
		return false
	}
	switch role {
	case string(MessageRoleAssistant):
		if _, hasToolCalls := message["tool_calls"]; hasToolCalls {
			message["content"] = nil
			return true
		}
		if _, hasFunctionCall := message["function_call"]; hasFunctionCall {
			message["content"] = nil
			return true
		}
	case string(MessageRoleTool):
		message["content"] = emptyToolResultContent
		return true
	}
	return false
}

func isEmptyContent(content any) bool {
	switch v := content.(type) {
	case string:
		return strings.TrimSpace(v) == ""
	case []any:
		return len(v) == 0
	default:
		return false
	}
}

// ValidateDocument enforces the OpenAI-compatible message contract and makes sure tool responses match earlier assistant tool_calls.
func (p ChatMessageProcessor) ValidateDocument(document *ChatRequestDocument) error {
	rawMessages, exists := document.Array("messages")
	if !exists {
		return badChatRequest("messages is required")
	}
	if len(rawMessages) == 0 {
		return badChatRequest("messages must not be empty")
	}

	pendingToolCalls := map[string]struct{}{}
	for i, rawMessage := range rawMessages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			return badChatRequest("messages[%d] must be an object", i)
		}
		role, err := requiredNonEmptyString(message, "role")
		if err != nil {
			return badChatRequest("messages[%d].role: %v", i, err)
		}
		policy, ok := p.roles[role]
		if !ok {
			return badChatRequest("messages[%d].role has unsupported value %q", i, role)
		}
		if err := ensureFieldsAbsent(message, policy.DisallowedFields...); err != nil {
			return badChatRequest("messages[%d]: %v", i, err)
		}

		switch policy.Role {
		case MessageRoleDeveloper, MessageRoleSystem, MessageRoleUser:
			if err := validateRequiredContent(message); err != nil {
				return badChatRequest("messages[%d].content: %v", i, err)
			}
		case MessageRoleAssistant:
			toolCallIDs, hasToolCalls, err := validateToolCallsField(message, i)
			if err != nil {
				return err
			}
			hasFunctionCall, err := validateFunctionCallField(message, i)
			if err != nil {
				return err
			}
			if err := validateAssistantContent(message, hasToolCalls || hasFunctionCall); err != nil {
				return badChatRequest("messages[%d].content: %v", i, err)
			}
			for _, id := range toolCallIDs {
				pendingToolCalls[id] = struct{}{}
			}
		case MessageRoleTool:
			toolCallID, err := requiredNonEmptyString(message, "tool_call_id")
			if err != nil {
				return badChatRequest("messages[%d].tool_call_id: %v", i, err)
			}
			if _, ok := pendingToolCalls[toolCallID]; !ok {
				return badChatRequest("messages[%d].tool_call_id does not match any previous assistant tool_calls", i)
			}
			delete(pendingToolCalls, toolCallID)
			if err := validateRequiredContent(message); err != nil {
				return badChatRequest("messages[%d].content: %v", i, err)
			}
		case MessageRoleFunction:
			if _, err := requiredNonEmptyString(message, "name"); err != nil {
				return badChatRequest("messages[%d].name: %v", i, err)
			}
			if err := validateRequiredContent(message); err != nil {
				return badChatRequest("messages[%d].content: %v", i, err)
			}
		}
	}
	return nil
}

func validateOpenAICompatChatMessages(request map[string]any) error {
	return defaultMessageProcessor.ValidateDocument(&ChatRequestDocument{raw: request})
}

func validateToolCallsField(message map[string]any, messageIndex int) ([]string, bool, error) {
	rawToolCalls, exists := message["tool_calls"]
	if !exists {
		return nil, false, nil
	}
	// Treat explicit null as absent (some SDKs serialize empty slots that way).
	if rawToolCalls == nil {
		delete(message, "tool_calls")
		return nil, false, nil
	}
	toolCalls, ok := rawToolCalls.([]any)
	if !ok {
		return nil, true, badChatRequest("messages[%d].tool_calls must be an array", messageIndex)
	}
	if len(toolCalls) == 0 {
		return nil, true, badChatRequest("messages[%d].tool_calls must not be empty", messageIndex)
	}
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(toolCalls))
	for callIndex, rawCall := range toolCalls {
		call, ok := rawCall.(map[string]any)
		if !ok {
			return nil, true, badChatRequest("messages[%d].tool_calls[%d] must be an object", messageIndex, callIndex)
		}
		id, err := requiredNonEmptyString(call, "id")
		if err != nil {
			return nil, true, badChatRequest("messages[%d].tool_calls[%d].id: %v", messageIndex, callIndex, err)
		}
		if _, exists := seen[id]; exists {
			return nil, true, badChatRequest("messages[%d].tool_calls[%d].id is duplicated", messageIndex, callIndex)
		}
		seen[id] = struct{}{}
		callType, err := requiredNonEmptyString(call, "type")
		if err != nil {
			return nil, true, badChatRequest("messages[%d].tool_calls[%d].type: %v", messageIndex, callIndex, err)
		}
		if callType != "function" {
			return nil, true, badChatRequest("messages[%d].tool_calls[%d].type must be \"function\"", messageIndex, callIndex)
		}
		function, ok := call["function"].(map[string]any)
		if !ok {
			return nil, true, badChatRequest("messages[%d].tool_calls[%d].function must be an object", messageIndex, callIndex)
		}
		if _, err := requiredNonEmptyString(function, "name"); err != nil {
			return nil, true, badChatRequest("messages[%d].tool_calls[%d].function.name: %v", messageIndex, callIndex, err)
		}
		if err := optionalStringField(function, "arguments"); err != nil {
			return nil, true, badChatRequest("messages[%d].tool_calls[%d].function.arguments: %v", messageIndex, callIndex, err)
		}
		ids = append(ids, id)
	}
	return ids, true, nil
}

func validateFunctionCallField(message map[string]any, messageIndex int) (bool, error) {
	rawFunctionCall, exists := message["function_call"]
	if !exists {
		return false, nil
	}
	if rawFunctionCall == nil {
		delete(message, "function_call")
		return false, nil
	}
	functionCall, ok := rawFunctionCall.(map[string]any)
	if !ok {
		return true, badChatRequest("messages[%d].function_call must be an object", messageIndex)
	}
	if _, err := requiredNonEmptyString(functionCall, "name"); err != nil {
		return true, badChatRequest("messages[%d].function_call.name: %v", messageIndex, err)
	}
	if err := optionalStringField(functionCall, "arguments"); err != nil {
		return true, badChatRequest("messages[%d].function_call.arguments: %v", messageIndex, err)
	}
	return true, nil
}

func validateAssistantContent(message map[string]any, canBeEmpty bool) error {
	content, exists := message["content"]
	if !exists || content == nil {
		if canBeEmpty {
			return nil
		}
		return fmt.Errorf("is required unless tool_calls or function_call is provided")
	}
	return validateNonEmptyContent(content)
}

func validateRequiredContent(message map[string]any) error {
	content, exists := message["content"]
	if !exists || content == nil {
		return fmt.Errorf("is required")
	}
	return validateNonEmptyContent(content)
}

func validateNonEmptyContent(content any) error {
	switch value := content.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("must not be empty")
		}
		return nil
	case []any:
		if len(value) == 0 {
			return fmt.Errorf("must not be empty")
		}
		for i, rawPart := range value {
			part, ok := rawPart.(map[string]any)
			if !ok {
				return fmt.Errorf("[%d] must be an object", i)
			}
			text, err := requiredTextContentPart(part, i)
			if err != nil {
				return err
			}
			if strings.TrimSpace(text) == "" {
				return fmt.Errorf("[%d].text must not be empty", i)
			}
		}
		return nil
	default:
		return fmt.Errorf("must be a string or an array of typed content parts")
	}
}

// We only accept text parts here because the gateway normalizes typed text content into a plain string before forwarding.
func requiredTextContentPart(part map[string]any, partIndex int) (string, error) {
	partType, err := requiredNonEmptyString(part, "type")
	if err != nil {
		return "", fmt.Errorf("[%d].type: %w", partIndex, err)
	}
	if partType != "text" {
		return "", fmt.Errorf("[%d].type has unsupported value %q", partIndex, partType)
	}
	text, err := requiredNonEmptyString(part, "text")
	if err != nil {
		return "", fmt.Errorf("[%d].text: %w", partIndex, err)
	}
	return text, nil
}

func combineTextContentParts(parts []any, messageIndex int) (string, error) {
	texts := make([]string, 0, len(parts))
	for partIndex, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			return "", badChatRequest("messages[%d].content[%d] must be an object", messageIndex, partIndex)
		}
		text, err := requiredTextContentPart(part, partIndex)
		if err != nil {
			return "", badChatRequest("messages[%d].content%v", messageIndex, err)
		}
		texts = append(texts, text)
	}
	if len(texts) == 0 {
		return "", nil
	}
	return strings.Join(texts, "\n"), nil
}

func ensureFieldsAbsent(values map[string]any, fields ...string) error {
	for _, field := range fields {
		if _, exists := values[field]; exists {
			return fmt.Errorf("%s is not allowed for this role", field)
		}
	}
	return nil
}

func requiredNonEmptyString(values map[string]any, field string) (string, error) {
	rawValue, exists := values[field]
	if !exists || rawValue == nil {
		return "", fmt.Errorf("is required")
	}
	value, ok := rawValue.(string)
	if !ok {
		return "", fmt.Errorf("must be a string")
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("must not be empty")
	}
	return value, nil
}

func optionalStringField(values map[string]any, field string) error {
	rawValue, exists := values[field]
	if !exists || rawValue == nil {
		return nil
	}
	if _, ok := rawValue.(string); !ok {
		return fmt.Errorf("must be a string")
	}
	return nil
}
