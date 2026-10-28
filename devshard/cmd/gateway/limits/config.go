package limits

import (
	"time"

	"devshard/cmd/gateway/config"
)

func GatewayConfigFromLimits(l config.Limits) GatewayConfig {
	modelLimits := make(map[string]ModelOverride, len(l.ModelLimits))
	for model, modelLimit := range l.ModelLimits {
		modelLimits[model] = ModelOverride{
			MaxConcurrent:  modelLimit.MaxConcurrentRequests,
			MaxInputTokens: modelLimit.MaxInputTokensInFlight,
		}
	}
	return GatewayConfig{
		MaxConcurrent:         l.Concurrency.MaxRequests,
		MaxInputTokens:        l.MaxInputTokensInFlight,
		AdmissionQueuePerSlot: l.AdmissionQueuePerSlot,
		AcquireWait:           time.Duration(l.AdmissionQueueWaitMS) * time.Millisecond,
		ModelLimits:           modelLimits,
	}
}

func ParticipantConfigFromLimits(l config.Limits) ParticipantConfig {
	return ParticipantConfig{
		Pricing: windowPricingOf(l),
		Factors: CongestionFactors{
			Soft:   l.Congestion.BetaSoft,
			Hard:   l.Congestion.BetaHard,
			Severe: l.Congestion.BetaSevere,
			Cross:  l.Congestion.BetaCross,
		},
		Slack:         l.Congestion.Slack,
		AfterFailures: l.HostCutoff.AfterFailures,
		BaseOpen:      time.Duration(l.HostCutoff.BaseMS) * time.Millisecond,
		MaxOpen:       time.Duration(l.HostCutoff.MaxMS) * time.Millisecond,
	}
}

// windowPricingOf prices one gateway's models. See capacity.md, "The participant limiter: IOCW".
func windowPricingOf(limitsConfig config.Limits) WindowPricing {
	contextTokens := make(map[string]int64, len(limitsConfig.ModelLimits))
	outputTokens := make(map[string]int64, len(limitsConfig.ModelLimits))
	for model, perModel := range limitsConfig.ModelLimits {
		if perModel.MaxModelLen != nil && *perModel.MaxModelLen > 0 {
			contextTokens[model] = *perModel.MaxModelLen
		}
		if perModel.MaxTokensCap > 0 {
			outputTokens[model] = perModel.MaxTokensCap
		}
	}
	return WindowPricing{
		Input:                     requestBoundsOf(limitsConfig.HostWindows.Input),
		Output:                    requestBoundsOf(limitsConfig.HostWindows.Output),
		ConcurrencyPer10000Weight: limitsConfig.Concurrency.RequestsPer10000Weight,
		FallbackContextTokens:     limitsConfig.FallbackMaxModelLen,
		FallbackOutputTokens:      limitsConfig.MaxTokensCap,
		ContextTokensByModel:      contextTokens,
		OutputTokensByModel:       outputTokens,
	}
}

func requestBoundsOf(window config.RequestWindow) RequestBounds {
	return RequestBounds{Min: window.MinRequests, Initial: window.InitialRequests}
}

// ParticipantConfigFromConfig takes the idle window from perf_host_staleness_seconds. See capacity.md, "Nothing here is persisted".
func ParticipantConfigFromConfig(configuration *config.Config) ParticipantConfig {
	settings := ParticipantConfigFromLimits(configuration.Limits)
	settings.IdleEviction = time.Duration(configuration.Perf.HostStalenessSeconds) * time.Second
	return settings
}
