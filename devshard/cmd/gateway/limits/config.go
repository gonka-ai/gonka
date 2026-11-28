package limits

import (
	"time"

	"devshard/cmd/gateway/config"
)

// GatewayConfigFromConfig is the network admission caps and, for every model that carries a block of its own settings, its resolved caps.
func GatewayConfigFromConfig(configuration *config.Config) GatewayConfig {
	network := GatewayConfigFromLimits(configuration.Limits)
	network.ByModel = make(map[string]GatewayConfig, len(configuration.ModelNames()))
	for _, model := range configuration.ModelNames() {
		network.ByModel[model] = GatewayConfigFromLimits(configuration.For(model).Limits)
	}
	return network
}

func GatewayConfigFromLimits(l config.Limits) GatewayConfig {
	return GatewayConfig{
		MaxConcurrent:         l.Concurrency.MaxRequests,
		MaxInputTokens:        l.MaxInputTokensInFlight,
		AdmissionQueuePerSlot: l.AdmissionQueuePerSlot,
		MaxQueuedBytes:        l.MaxQueuedRequestBytes,
		AcquireWait:           time.Duration(l.AdmissionQueueWaitMS) * time.Millisecond,
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

func windowPricingOf(limitsConfig config.Limits) WindowPricing {
	return WindowPricing{
		Input:                     requestBoundsOf(limitsConfig.HostWindows.Input),
		Output:                    requestBoundsOf(limitsConfig.HostWindows.Output),
		ConcurrencyPer10000Weight: limitsConfig.Concurrency.RequestsPer10000Weight,
		FallbackContextTokens:     limitsConfig.FallbackMaxModelLen,
		FallbackOutputTokens:      limitsConfig.MaxTokensCap,
	}
}

func requestBoundsOf(window config.RequestWindow) RequestBounds {
	return RequestBounds{Min: window.MinRequests, Initial: window.InitialRequests}
}

// ParticipantConfigFromConfig takes the idle window from perf_host_staleness_seconds and gives every named model the configuration its own settings resolve to. See capacity.md, "Nothing here is persisted".
func ParticipantConfigFromConfig(configuration *config.Config) ParticipantConfig {
	settings := participantConfigOf(configuration)
	settings.ByModel = make(map[string]ParticipantConfig, len(configuration.ModelNames()))
	for _, model := range configuration.ModelNames() {
		effective := participantConfigOf(configuration.For(model))
		if pinned := configuration.For(model).Limits.MaxModelLen; pinned != nil && *pinned > 0 {
			effective.Pricing.ContextTokensByModel = map[string]int64{model: *pinned}
		}
		settings.ByModel[model] = effective
	}
	return settings
}

func participantConfigOf(configuration *config.Config) ParticipantConfig {
	settings := ParticipantConfigFromLimits(configuration.Limits)
	settings.IdleEviction = time.Duration(configuration.Perf.HostStalenessSeconds) * time.Second
	return settings
}
