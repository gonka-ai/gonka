package config

import (
	"fmt"

	"devshard/cmd/gateway/internal/logkey"
	"devshard/logging"
)

// LegacyModelLimits is one entry of the settings key model_limits; its two token fields are required as a pair.
//
// Deprecated: set the same fields under models.<id>; removal is planned for v7 or v8.
type LegacyModelLimits struct {
	DefaultMaxTokens       int64  `json:"default_max_tokens"`
	MaxTokensCap           int64  `json:"max_tokens_cap"`
	MaxModelLen            *int64 `json:"max_model_len,omitempty"`
	MaxConcurrentRequests  *int64 `json:"max_concurrent_requests,omitempty"`
	MaxInputTokensInFlight *int64 `json:"max_input_tokens_in_flight,omitempty"`
}

// LegacyModelAccess is the settings key model_access, a tier per model id.
//
// Deprecated: set access under models.<id>; removal is planned for v7 or v8.
type LegacyModelAccess map[string]string

const (
	legacyModelLimitsKey = "model_limits"
	legacyModelAccessKey = "model_access"
)

func (o Overrides) legacyModelSettings() map[string]ModelSettings {
	translated := make(map[string]ModelSettings, len(o.ModelLimits)+len(o.ModelAccess))
	for model, limits := range o.ModelLimits {
		translated[model] = ModelSettings{
			DefaultMaxTokens:       &limits.DefaultMaxTokens,
			MaxTokensCap:           &limits.MaxTokensCap,
			MaxModelLen:            limits.MaxModelLen,
			MaxConcurrentRequests:  limits.MaxConcurrentRequests,
			MaxInputTokensInFlight: limits.MaxInputTokensInFlight,
		}
	}
	for model, access := range o.ModelAccess {
		settings := translated[model]
		settings.Access = &access
		translated[model] = settings
	}
	return translated
}

// WarnLegacyKeys logs one warning for each deprecated settings key the document holds.
func (o Overrides) WarnLegacyKeys() {
	if len(o.ModelLimits) > 0 {
		warnLegacyKey(legacyModelLimitsKey, "the same fields under models.<id>")
	}
	if len(o.ModelAccess) > 0 {
		warnLegacyKey(legacyModelAccessKey, "models.<id>.access")
	}
}

func warnLegacyKey(key, replacement string) {
	logging.Warn(fmt.Sprintf("settings key %s is deprecated: set %s instead; removal is planned for v7 or v8", key, replacement),
		logkey.Subsystem, "config")
}
