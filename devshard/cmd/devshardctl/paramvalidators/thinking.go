package paramvalidators

import (
	"errors"
	"fmt"

	"devshard/cmd/devshardctl/filtercore"
)

// ErrThinkingShape covers the wrapper-level rejection. ErrThinkingType covers the inner
// type field (missing / not a string / not in the accepted enum).
var (
	ErrThinkingShape = errors.New("thinking: invalid wrapper shape")
	ErrThinkingType  = errors.New("thinking.type: must be \"enabled\", \"disabled\", \"adaptive\", or \"auto\"")
)

type ThinkingValidator struct {
	MirrorToTemplateKwargsForModels []string
}

func (v ThinkingValidator) Validate(vctx ValidatorContext) error {
	raw, exists := vctx.Document["thinking"]
	if !exists {
		return nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: must be an object", ErrThinkingShape)
	}
	typeRaw, hasType := obj["type"]
	if !hasType {
		return fmt.Errorf("%w: type is required", ErrThinkingType)
	}
	typeStr, ok := typeRaw.(string)
	if !ok {
		return fmt.Errorf("%w: type must be a string", ErrThinkingType)
	}
	enabled, ok := resolveThinkingType(typeStr)
	if !ok {
		return fmt.Errorf("%w: got %q", ErrThinkingType, typeStr)
	}
	if v.shouldMirror(vctx.RoutedModel) {
		// Mirror path (Kimi): chat template only reads from chat_template_kwargs.thinking.
		// Top-level thinking is dead weight on the wire — drop it together with siblings.
		if err := mirrorThinkingToTemplateKwargs(vctx.Document, enabled); err != nil {
			return err
		}
		delete(vctx.Document, "thinking")
		return nil
	}
	// No-mirror path: normalize client extensions to the canonical enum so vLLM never sees adaptive/auto.
	if enabled {
		obj["type"] = "enabled"
	} else {
		obj["type"] = "disabled"
	}
	// `display` is a Claude Code CLI UI hint with no vLLM semantics.
	delete(obj, "display")
	return nil
}

// resolveThinkingType maps client-side extensions adaptive/auto to enabled — both signal opt-in thinking with an SDK-chosen budget.
func resolveThinkingType(typeStr string) (bool, bool) {
	switch typeStr {
	case "enabled", "adaptive", "auto":
		return true, true
	case "disabled":
		return false, true
	default:
		return false, false
	}
}

func (v ThinkingValidator) shouldMirror(routedModel string) bool {
	return filtercore.MatchesModel(routedModel, v.MirrorToTemplateKwargsForModels)
}

func mirrorThinkingToTemplateKwargs(document map[string]any, enabled bool) error {
	chatTemplateKwargs, err := getOrCreateChatTemplateKwargs(document)
	if err != nil {
		return err
	}
	if _, exists := chatTemplateKwargs["thinking"]; exists {
		return nil
	}
	chatTemplateKwargs["thinking"] = enabled
	return nil
}
