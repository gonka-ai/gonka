package main

import (
	"devshard/cmd/devshardctl/messagevalidators"
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

// MessageRolePolicy bundles validate-side rules for a (route, role) pair.
// Per-model overrides plug into ChatMessageProcessor.modelRoles and replace
// the default entry verbatim for that role on that route.
type MessageRolePolicy struct {
	Role              MessageRole
	DisallowedFields  []string
	RequireName       bool
	RequireToolCallID bool
	ContentRule       MessageContentRule
	// ContentValidator, when non-nil, replaces the default role-specific
	// content shape check. Used by MiniMax-M2.7 tool messages whose content
	// is a {name,type,text}[] array instead of a string.
	ContentValidator messagevalidators.ContentValidator
}

// MessageNormalizer rewrites the messages array before ValidateDocument runs.
// Returns the (possibly filtered/rewritten) messages, whether anything
// changed, and an unrecoverable error (which the caller wraps as HTTP 400).
type MessageNormalizer interface {
	Apply(messages []any) ([]any, bool, error)
}

// ChatMessageProcessor coordinates per-model normalization and validation.
// Mirrors defaultVLLMParameterCatalog's per-model dispatch pattern for the
// message side of the pipeline.
type ChatMessageProcessor struct {
	defaultRoles       map[string]MessageRolePolicy
	modelRoles         map[string]map[string]MessageRolePolicy
	defaultNormalizers []MessageNormalizer
	modelNormalizers   map[string][]MessageNormalizer
}

var defaultMessageProcessor = defaultChatMessageProcessor()

func defaultChatMessageProcessor() ChatMessageProcessor {
	return ChatMessageProcessor{
		defaultRoles: openAIRolePolicies(),
		// Per-model role-policy overrides. Adding a model = add one row.
		modelRoles: map[string]map[string]MessageRolePolicy{
			// MiniMax-M2.7 tool messages omit tool_call_id and carry results as a
			// {name,type,text}[] array — see docs/chat-api/minimax-m2.7.md.
			miniMaxM27ModelID: {
				string(MessageRoleTool): {
					Role:              MessageRoleTool,
					DisallowedFields:  []string{"tool_calls", "function_call"},
					RequireToolCallID: false,
					ContentRule:       MessageContentRequired,
					ContentValidator: messagevalidators.MinimaxToolMessage{
						MaxEntries:  MinimaxToolMessageMaxEntries,
						NameMaxLen:  MinimaxToolMessageNameMaxLen,
						TextMaxSize: MinimaxToolMessageTextMaxSize,
					},
				},
			},
		},
		defaultNormalizers: []MessageNormalizer{
			messagevalidators.OrphanToolMessageDropper{},
			messagevalidators.EmptyAssistantTurnDropper{},
			messagevalidators.EmptyContentNormalizer{ToolSentinel: emptyToolResultContent},
			messagevalidators.LegacyToolNameStripper{},
			messagevalidators.TextPartsFlattener{},
		},
		// Per-model normalizer chain overrides. Adding a model = add one row.
		modelNormalizers: map[string][]MessageNormalizer{
			miniMaxM27ModelID: {
				messagevalidators.MinimaxOrphanToolMessageDropper{},
				messagevalidators.EmptyAssistantTurnDropper{},
				messagevalidators.EmptyContentNormalizer{
					ToolSentinel: emptyToolResultContent,
					SkipRoles:    []string{string(MessageRoleTool)},
				},
				messagevalidators.LegacyToolNameStripper{},
				messagevalidators.MinimaxToolCallIDStripper{},
				messagevalidators.TextPartsFlattener{
					SkipRoles: []string{string(MessageRoleTool)},
				},
			},
		},
	}
}

func openAIRolePolicies() map[string]MessageRolePolicy {
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
	return byRole
}

func (p ChatMessageProcessor) resolveRoles(routedModel string) map[string]MessageRolePolicy {
	overrides := p.modelRoles[routedModel]
	if len(overrides) == 0 {
		return p.defaultRoles
	}
	merged := make(map[string]MessageRolePolicy, len(p.defaultRoles))
	for k, v := range p.defaultRoles {
		merged[k] = v
	}
	for k, v := range overrides {
		merged[k] = v
	}
	return merged
}

func (p ChatMessageProcessor) resolveNormalizers(routedModel string) []MessageNormalizer {
	if list, ok := p.modelNormalizers[routedModel]; ok {
		return list
	}
	return p.defaultNormalizers
}

// NormalizeDocument fixes message shapes the gateway intentionally accepts
// (orphan tool drops, empty tool content sentinels, content-array flattening).
// The per-route normalizer chain decides what runs.
func (p ChatMessageProcessor) NormalizeDocument(document *ChatRequestDocument, routedModel string) error {
	messages, ok := document.Array("messages")
	if !ok {
		return nil
	}
	changed := false
	for _, n := range p.resolveNormalizers(routedModel) {
		rewritten, ch, err := n.Apply(messages)
		if err != nil {
			return wrapBadChatRequest(err)
		}
		if ch {
			messages = rewritten
			changed = true
		}
	}
	if changed {
		document.Set("messages", messages)
	}
	return nil
}

// ValidateDocument enforces the per-role policies resolved against the routed
// model. RequireToolCallID and ContentValidator are honored as policy fields
// so per-model overrides reuse the same validation skeleton.
func (p ChatMessageProcessor) ValidateDocument(document *ChatRequestDocument, routedModel string) error {
	rawMessages, exists := document.Array("messages")
	if !exists {
		return badChatRequest("messages is required")
	}
	if len(rawMessages) == 0 {
		return badChatRequest("messages must not be empty")
	}
	roles := p.resolveRoles(routedModel)

	pendingToolCalls := map[string]struct{}{}
	for i, rawMessage := range rawMessages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			return badChatRequest("messages[%d] must be an object", i)
		}
		role, err := messagevalidators.RequiredNonEmptyString(message, "role")
		if err != nil {
			return badChatRequest("messages[%d].role: %v", i, err)
		}
		policy, ok := roles[role]
		if !ok {
			return badChatRequest("messages[%d].role has unsupported value %q", i, role)
		}
		if err := messagevalidators.EnsureFieldsAbsent(message, policy.DisallowedFields...); err != nil {
			return badChatRequest("messages[%d]: %v", i, err)
		}
		if err := p.validateRoleSpecific(message, i, policy, pendingToolCalls); err != nil {
			return err
		}
	}
	return nil
}

// validateRoleSpecific keeps ValidateDocument's iteration loop legible: each
// case reads as a single responsibility.
func (p ChatMessageProcessor) validateRoleSpecific(message map[string]any, i int, policy MessageRolePolicy, pendingToolCalls map[string]struct{}) error {
	switch policy.Role {
	case MessageRoleDeveloper, MessageRoleSystem, MessageRoleUser:
		if err := messagevalidators.ValidateRequiredContentField(message); err != nil {
			return badChatRequest("messages[%d].content: %v", i, err)
		}
	case MessageRoleAssistant:
		toolCallIDs, hasToolCalls, err := messagevalidators.ValidateToolCallsField(message)
		if err != nil {
			return badChatRequest("messages[%d].%v", i, err)
		}
		hasFunctionCall, err := messagevalidators.ValidateFunctionCallField(message)
		if err != nil {
			return badChatRequest("messages[%d].%v", i, err)
		}
		if err := messagevalidators.ValidateAssistantContentField(message, hasToolCalls || hasFunctionCall); err != nil {
			return badChatRequest("messages[%d].content: %v", i, err)
		}
		for _, id := range toolCallIDs {
			pendingToolCalls[id] = struct{}{}
		}
	case MessageRoleTool:
		if policy.RequireToolCallID {
			toolCallID, err := messagevalidators.RequiredNonEmptyString(message, "tool_call_id")
			if err != nil {
				return badChatRequest("messages[%d].tool_call_id: %v", i, err)
			}
			if _, ok := pendingToolCalls[toolCallID]; !ok {
				return badChatRequest("messages[%d].tool_call_id does not match any previous assistant tool_calls", i)
			}
			delete(pendingToolCalls, toolCallID)
		}
		if policy.ContentValidator != nil {
			content, exists := message["content"]
			if !exists {
				return badChatRequest("messages[%d].content: is required", i)
			}
			if err := policy.ContentValidator.Validate(content); err != nil {
				return badChatRequest("messages[%d].%v", i, err)
			}
		} else if err := messagevalidators.ValidateRequiredContentField(message); err != nil {
			return badChatRequest("messages[%d].content: %v", i, err)
		}
	case MessageRoleFunction:
		if policy.RequireName {
			if _, err := messagevalidators.RequiredNonEmptyString(message, "name"); err != nil {
				return badChatRequest("messages[%d].name: %v", i, err)
			}
		}
		if err := messagevalidators.ValidateRequiredContentField(message); err != nil {
			return badChatRequest("messages[%d].content: %v", i, err)
		}
	}
	return nil
}

func validateOpenAICompatChatMessages(request map[string]any) error {
	return defaultMessageProcessor.ValidateDocument(&ChatRequestDocument{raw: request}, "")
}
