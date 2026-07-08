package messagevalidators

import (
	"fmt"
	"slices"
)

// Wire-format role values; kept as local literals so the package stays free of
// main-package imports. These match what is on the JSON wire and never change.
const (
	roleAssistant = "assistant"
	roleTool      = "tool"
)

// OrphanToolMessageDropper drops role:"tool" entries whose tool_call_id has no
// matching prior assistant.tool_call. Mirrors ValidateDocument's pending-set
// accounting so survivors pass the strict check downstream.
type OrphanToolMessageDropper struct{}

func (OrphanToolMessageDropper) Apply(messages []any) ([]any, bool, error) {
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
		case roleAssistant:
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
		case roleTool:
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
	return filtered, dropped, nil
}

// MinimaxOrphanToolMessageDropper drops role:"tool" messages on routes whose
// tool messages carry no tool_call_id (e.g. MiniMax-M2.7). Orphan = tool
// message appearing before any assistant.tool_calls[] block. The block stays
// open across consecutive tool turns and resets on the next non-tool message.
type MinimaxOrphanToolMessageDropper struct{}

func (MinimaxOrphanToolMessageDropper) Apply(messages []any) ([]any, bool, error) {
	inToolBlock := false
	filtered := make([]any, 0, len(messages))
	dropped := false
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			filtered = append(filtered, raw)
			inToolBlock = false
			continue
		}
		role, _ := msg["role"].(string)
		switch role {
		case roleAssistant:
			inToolBlock = false
			if calls, ok := msg["tool_calls"].([]any); ok && len(calls) > 0 {
				inToolBlock = true
			}
		case roleTool:
			if !inToolBlock {
				dropped = true
				continue
			}
		default:
			inToolBlock = false
		}
		filtered = append(filtered, raw)
	}
	return filtered, dropped, nil
}

// EmptyAssistantTurnDropper drops role:"assistant" messages with no content
// and no tool_calls / function_call — informationless placeholders left by
// session-resume serializers.
type EmptyAssistantTurnDropper struct{}

func (EmptyAssistantTurnDropper) Apply(messages []any) ([]any, bool, error) {
	filtered := make([]any, 0, len(messages))
	dropped := false
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			filtered = append(filtered, raw)
			continue
		}
		if role, _ := msg["role"].(string); role == roleAssistant && IsAssistantTurnEmpty(msg) {
			dropped = true
			continue
		}
		filtered = append(filtered, raw)
	}
	return filtered, dropped, nil
}

// EmptyContentNormalizer fills/normalizes content for special-case roles:
//   - role:"tool" with missing, null, or empty content → ToolSentinel
//   - role:"assistant" with empty content AND a tool_calls/function_call payload → null
//
// SkipRoles bypasses messages whose role is listed (e.g. MiniMax tool messages
// carry array content with a non-OpenAI shape and must not be touched here).
type EmptyContentNormalizer struct {
	ToolSentinel string
	SkipRoles    []string
}

func (n EmptyContentNormalizer) Apply(messages []any) ([]any, bool, error) {
	changed := false
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if slices.Contains(n.SkipRoles, role) {
			continue
		}
		content, exists := msg["content"]
		switch {
		case !exists, content == nil:
			if role == roleTool {
				msg["content"] = n.ToolSentinel
				changed = true
			}
		case IsEmptyContent(content):
			switch role {
			case roleAssistant:
				_, hasToolCalls := msg["tool_calls"]
				_, hasFunctionCall := msg["function_call"]
				if hasToolCalls || hasFunctionCall {
					msg["content"] = nil
					changed = true
				}
			case roleTool:
				msg["content"] = n.ToolSentinel
				changed = true
			}
		}
	}
	return messages, changed, nil
}

// LegacyToolNameStripper strips the legacy `name` field from role:"tool"
// messages — an artifact of the old role:"function" API. Universal.
type LegacyToolNameStripper struct{}

func (LegacyToolNameStripper) Apply(messages []any) ([]any, bool, error) {
	changed := false
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != roleTool {
			continue
		}
		if _, exists := msg["name"]; !exists {
			continue
		}
		delete(msg, "name")
		changed = true
	}
	return messages, changed, nil
}

// MinimaxToolCallIDStripper silently strips tool_call_id from role:"tool"
// messages. Clients dual-emitting tool_call_id for cross-route portability
// keep working — MiniMax itself correlates tool results positionally.
type MinimaxToolCallIDStripper struct{}

func (MinimaxToolCallIDStripper) Apply(messages []any) ([]any, bool, error) {
	changed := false
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != roleTool {
			continue
		}
		if _, exists := msg["tool_call_id"]; exists {
			delete(msg, "tool_call_id")
			changed = true
		}
	}
	return messages, changed, nil
}

// TextPartsFlattener combines a content array of {type:"text",text} parts into
// a single newline-joined string. SkipRoles bypasses messages whose role is
// listed (e.g. MiniMax tool messages keep their {name,type,text}[] shape).
type TextPartsFlattener struct {
	SkipRoles []string
}

func (n TextPartsFlattener) Apply(messages []any) ([]any, bool, error) {
	changed := false
	for index, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if slices.Contains(n.SkipRoles, role) {
			continue
		}
		content, exists := msg["content"]
		if !exists || content == nil {
			continue
		}
		parts, ok := content.([]any)
		if !ok {
			continue
		}
		combined, err := CombineTextContentParts(parts)
		if err != nil {
			return nil, false, fmt.Errorf("messages[%d].content%w", index, err)
		}
		if combined != "" {
			msg["content"] = combined
			changed = true
		}
	}
	return messages, changed, nil
}
