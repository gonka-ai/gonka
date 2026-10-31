package filters

import (
	"fmt"
	"maps"
	"math"
	"slices"
)

// Bounds on a model's own chat_template_kwargs policy, kept well inside the request bounds on the object.
const (
	templateKwargsKeyMaxLen       = 64
	templateKwargsMaxPolicyKeys   = 16
	templateKwargsForcedStringMax = 256
)

// TemplateKwargsPolicy is what a model's chat template must always or never see in chat_template_kwargs.
type TemplateKwargsPolicy struct {
	Forced map[string]any `json:"forced,omitempty"`
	Denied []string       `json:"denied,omitempty"`
}

func (policy TemplateKwargsPolicy) touches(key string) bool {
	_, forced := policy.Forced[key]
	return forced || slices.Contains(policy.Denied, key)
}

func (policy TemplateKwargsPolicy) clone() TemplateKwargsPolicy {
	return TemplateKwargsPolicy{Forced: maps.Clone(policy.Forced), Denied: slices.Clone(policy.Denied)}
}

func (policy TemplateKwargsPolicy) validate() error {
	if len(policy.Forced) > templateKwargsMaxPolicyKeys || len(policy.Denied) > templateKwargsMaxPolicyKeys {
		return fmt.Errorf("template_kwargs: at most %d forced and %d denied keys", templateKwargsMaxPolicyKeys, templateKwargsMaxPolicyKeys)
	}
	for key, value := range policy.Forced {
		if err := validTemplateKey("template_kwargs.forced", key); err != nil {
			return err
		}
		if err := validForcedValue(key, value); err != nil {
			return err
		}
		if slices.Contains(policy.Denied, key) {
			return fmt.Errorf("template_kwargs: %q is both forced and denied", key)
		}
	}
	for _, key := range policy.Denied {
		if err := validTemplateKey("template_kwargs.denied", key); err != nil {
			return err
		}
	}
	return nil
}

func validForcedValue(key string, value any) error {
	switch typed := value.(type) {
	case bool:
		return nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return fmt.Errorf("template_kwargs.forced.%s must be a finite number", key)
		}
		return nil
	case string:
		if len(typed) > templateKwargsForcedStringMax {
			return fmt.Errorf("template_kwargs.forced.%s is longer than %d bytes", key, templateKwargsForcedStringMax)
		}
		return nil
	default:
		return fmt.Errorf("template_kwargs.forced.%s must be a boolean, a number or a string", key)
	}
}

func applyTemplateKwargsPolicy() RuleFunc {
	return func(ctx RuleContext) error {
		policy := ctx.Profile.templateKwargsPolicy()
		if kwargs, present, isObject := ctx.Document.ObjectField("chat_template_kwargs"); present && isObject {
			for _, key := range policy.Denied {
				delete(kwargs, key)
			}
		}
		if len(policy.Forced) == 0 {
			return nil
		}
		kwargs, err := getOrCreateChatTemplateKwargs(ctx.Document)
		if err != nil {
			return err
		}
		maps.Copy(kwargs, policy.Forced)
		return nil
	}
}
