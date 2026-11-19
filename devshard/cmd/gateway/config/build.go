package config

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"devshard/cmd/gateway/env"
)

// Build produces the validated snapshot: Defaults() ← env ← admin overrides, cloning every input map and slice.
func Build(values env.Values, overrides Overrides) (*Config, error) {
	configuration := Defaults()

	// Environment layer.
	applyTunables(&configuration, values.Tunables)
	overrideIfSet(&configuration.Server.Port, values.Port)
	overrideIfSet(&configuration.Server.StorageDir, values.StorageDir)
	if values.APIKeys != nil {
		configuration.Server.APIKeys = splitCommaSeparated(*values.APIKeys)
	}
	overrideIfSet(&configuration.Server.AdminAPIKey, values.AdminAPIKey)
	overrideIfSet(&configuration.Server.DevshardsJSON, values.DevshardsJSON)
	overrideIfSet(&configuration.Server.MaxConcurrentRuntimeBuilds, values.MaxConcurrentRuntimeBuilds)
	overrideIfSet(&configuration.Server.MaxConcurrentPromptCounts, values.MaxConcurrentPromptCounts)
	overrideIfSet(&configuration.Server.StatsTokenizerEnabled, values.StatsTokenizerEnabled)
	overrideIfSet(&configuration.Server.SessionRetentionEpochs, values.SessionRetentionEpochs)

	overrideIfSet(&configuration.Chain.GRPCEndpoint, values.ChainGRPC)
	overrideIfSet(&configuration.Chain.PublicAPIBaseURL, values.PublicAPI)
	overrideIfSet(&configuration.Chain.ChainID, values.ChainID)
	overrideIfSet(&configuration.Chain.RPCEndpoint, values.ChainRPC)
	overrideIfSet(&configuration.Chain.VersionsPollIntervalSeconds, values.ChainVersionsPollIntervalSeconds)
	overrideIfSet(&configuration.Tx.FeeAmount, values.TxFeeAmount)
	overrideIfSet(&configuration.Tx.GasLimit, values.TxGasLimit)
	overrideIfSet(&configuration.Tx.FeeDenom, values.TxFeeDenom)
	overrideIfSet(&configuration.Tx.PollIntervalMS, values.TxPollIntervalMS)
	overrideIfSet(&configuration.Tx.PollTimeoutMS, values.TxPollTimeoutMS)
	overrideIfSet(&configuration.Tx.CallTimeoutMS, values.TxCallTimeoutMS)

	overrideIfSet(&configuration.Limits.Concurrency.PoCRequestsPer10000Weight, pocWeightLimit(values, Defaults().Limits.Concurrency.RequestsPer10000Weight))
	overrideIfSet(&configuration.Scheduler.DispatchQueueDepth, values.DispatchQueueDepth)

	overrideIfSet(&configuration.Modes.PoCMode, values.PoCMode)

	overrideIfSet(&configuration.Cache.ChatCacheMaxBytes, values.ChatCacheMaxBytes)

	overrideIfSet(&configuration.Accounting.RetentionHours, values.AccountingRetentionHours)
	overrideIfSet(&configuration.Accounting.RetentionMaxRows, values.AccountingRetentionMaxRows)

	overrideIfSet(&configuration.NonceAccounting.Enabled, values.NonceAccountingEnabled)
	overrideIfSet(&configuration.NonceAccounting.Port, values.NonceAccountingPort)
	overrideIfSet(&configuration.NonceAccounting.RetentionEpochs, values.NonceAccountingRetentionEpochs)
	overrideIfSet(&configuration.NonceAccounting.SnapshotSeconds, values.NonceAccountingSnapshotSeconds)
	overrideIfSet(&configuration.TimeoutSweep.BudgetPerTick, values.TimeoutSweepBudgetPerTick)
	overrideIfSet(&configuration.TimeoutSweep.GraceSeconds, values.TimeoutSweepGraceSeconds)

	overrideIfSet(&configuration.Capture.Enabled, values.CaptureEnabled)
	overrideIfSet(&configuration.Capture.Dir, values.CaptureDir)
	overrideIfSet(&configuration.Capture.SampleRate, values.CaptureSampleRate)
	overrideIfSet(&configuration.Capture.MaxBytes, values.CaptureMaxBytes)

	overrideIfSet(&configuration.HeightSync.Enabled, values.HeightSyncEnabled)
	overrideIfSet(&configuration.HeightSync.RequireSeed, values.HeightSyncRequireSeed)
	overrideIfSet(&configuration.HeightSync.ChainOracle, values.HeightSyncChainOracle)
	overrideIfSet(&configuration.HeightSync.Heartbeat, values.HeightSyncHeartbeat)
	overrideIfSet(&configuration.HeightSync.AnchorK, values.HeightSyncAnchorK)
	overrideIfSet(&configuration.HeightSync.AnchorSlots, values.HeightSyncAnchorSlots)
	overrideIfSet(&configuration.HeightSync.QuietSettleDelayMS, values.HeightSyncQuietSettleDelayMS)
	overrideIfSet(&configuration.HostPing.Disabled, values.HostPingDisabled)
	overrideIfSet(&configuration.HostPing.IntervalMS, values.HostPingIntervalMS)
	overrideIfSet(&configuration.HostPing.TimeoutMS, values.HostPingTimeoutMS)
	overrideIfSet(&configuration.HostPing.Concurrency, values.HostPingConcurrency)

	// Admin-override layer (wins over env).
	applyTunables(&configuration, overrides.Tunables)
	if overrides.ParticipantAllowlist != nil {
		configuration.Scheduler.ParticipantAllowlist = slices.Clone(*overrides.ParticipantAllowlist)
	}
	if overrides.UnthrottledParticipants != nil {
		configuration.Scheduler.UnthrottledParticipants = slices.Clone(*overrides.UnthrottledParticipants)
	}
	overrideIfSet(&configuration.Limits.FallbackMaxModelLen, overrides.FallbackMaxModelLen)
	overrideIfSet(&configuration.Limits.HostWindows.Input.MinRequests, overrides.HostInputWindowMinRequests)
	overrideIfSet(&configuration.Limits.HostWindows.Input.InitialRequests, overrides.HostInputWindowInitialRequests)
	overrideIfSet(&configuration.Limits.HostWindows.Output.MinRequests, overrides.HostOutputWindowMinRequests)
	overrideIfSet(&configuration.Limits.HostWindows.Output.InitialRequests, overrides.HostOutputWindowInitialRequests)
	overrideIfSet(&configuration.Limits.Congestion.BetaSoft, overrides.IOAIMDBetaSoft)
	overrideIfSet(&configuration.Limits.Congestion.BetaHard, overrides.IOAIMDBetaHard)
	overrideIfSet(&configuration.Limits.Congestion.BetaSevere, overrides.IOAIMDBetaSevere)
	overrideIfSet(&configuration.Limits.Congestion.BetaCross, overrides.IOAIMDBetaCross)
	overrideIfSet(&configuration.Limits.Congestion.Slack, overrides.HostCongestionSlack)
	overrideIfSet(&configuration.Limits.HostCutoff.AfterFailures, overrides.HostCutoffAfterFailures)
	overrideIfSet(&configuration.Limits.HostCutoff.BaseMS, overrides.HostCutoffMS)
	overrideIfSet(&configuration.Limits.HostCutoff.MaxMS, overrides.HostCutoffMaxMS)
	if overrides.ModelLimits != nil {
		configuration.Limits.ModelLimits = maps.Clone(overrides.ModelLimits)
	}
	if overrides.ModelAccess != nil {
		configuration.Limits.ModelAccess = maps.Clone(overrides.ModelAccess)
	}

	if err := configuration.Validate(); err != nil {
		return nil, err
	}
	return &configuration, nil
}

// overrideIfSet: a nil source leaves the lower layer's value in place.
func overrideIfSet[T any](target, source *T) {
	if source != nil {
		*target = *source
	}
}

// splitCommaSeparated trims, drops empties, and always returns a fresh slice (no clone needed).
func splitCommaSeparated(raw string) []string {
	parts := strings.Split(raw, ",")
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return cleaned
}

func applyTunables(configuration *Config, tunables env.Tunables) {
	overrideIfSet(&configuration.Limits.DefaultMaxTokens, tunables.DefaultMaxTokens)
	overrideIfSet(&configuration.Limits.MaxTokensCap, tunables.MaxTokensCap)
	overrideIfSet(&configuration.Limits.Concurrency.MaxRequests, tunables.MaxConcurrentRequests)
	overrideIfSet(&configuration.Limits.Concurrency.RequestsPer10000Weight, tunables.MaxConcurrentRequestsPer10000Weight)
	overrideIfSet(&configuration.Limits.Concurrency.PoCRequestsPer10000Weight, tunables.PoCMaxConcurrentRequestsPer10000Weight)
	overrideIfSet(&configuration.Limits.MaxInputTokensInFlight, tunables.MaxInputTokensInFlight)
	overrideIfSet(&configuration.Limits.AdmissionQueueWaitMS, tunables.AdmissionQueueWaitMS)
	overrideIfSet(&configuration.Limits.AdmissionQueuePerSlot, tunables.AdmissionQueuePerSlot)
	overrideIfSet(&configuration.Scheduler.MatchWaitMS, tunables.MatchWaitMS)
	overrideIfSet(&configuration.Scheduler.MaxConsecutiveBurns, tunables.MaxConsecutiveBurns)
	overrideIfSet(&configuration.Limits.ForceUpstreamStreaming, tunables.ForceUpstreamStreaming)
	configuration.Limits.LogprobsOptimization = cmp.Or(tunables.LogprobsOptimization, configuration.Limits.LogprobsOptimization)
	overrideIfSet(&configuration.Limits.MaxBufferedResponseBytes, tunables.MaxBufferedResponseBytes)
	overrideIfSet(&configuration.Limits.MaxBufferedRequestBytes, tunables.MaxBufferedRequestBytes)
	overrideIfSet(&configuration.Limits.MinClientReadBytesPerSecond, tunables.MinClientReadBytesPerSecond)
	overrideIfSet(&configuration.Scheduler.WarmNewEscrows, tunables.WarmNewEscrows)
	overrideIfSet(&configuration.Chain.SnapshotMaxAgeSeconds, tunables.ChainSnapshotMaxAgeSeconds)
	overrideIfSet(&configuration.Engine.ReceiptTimeoutMS, tunables.EngineReceiptTimeoutMS)
	overrideIfSet(&configuration.Engine.FirstTokenFloorMS, tunables.EngineFirstTokenFloorMS)
	overrideIfSet(&configuration.Engine.FirstTokenCeilingMS, tunables.EngineFirstTokenCeilingMS)
	overrideIfSet(&configuration.Engine.InterChunkStallMS, tunables.EngineInterChunkStallMS)
	overrideIfSet(&configuration.Engine.LoserGraceMS, tunables.EngineLoserGraceMS)
	overrideIfSet(&configuration.Engine.MaxConcurrentTimeoutVotes, tunables.EngineMaxConcurrentTimeoutVotes)
	overrideIfSet(&configuration.Engine.HedgeFirstTokenFloorMS, tunables.EngineHedgeFirstTokenFloorMS)
	overrideIfSet(&configuration.Perf.EWMAHalfLifeSeconds, tunables.PerfEWMAHalfLifeSeconds)
	overrideIfSet(&configuration.Perf.ConsecutiveFailThreshold, tunables.PerfConsecutiveFailThreshold)
	overrideIfSet(&configuration.Perf.FailureRateThreshold, tunables.PerfFailureRateThreshold)
	overrideIfSet(&configuration.Perf.FailureRateMinVolume, tunables.PerfFailureRateMinVolume)
	overrideIfSet(&configuration.Perf.EjectionBaseSeconds, tunables.PerfEjectionBaseSeconds)
	overrideIfSet(&configuration.Perf.EjectionMaxSeconds, tunables.PerfEjectionMaxSeconds)
	overrideIfSet(&configuration.Perf.MaxEjectionFraction, tunables.PerfMaxEjectionFraction)
	overrideIfSet(&configuration.Perf.MinAvailableHosts, tunables.PerfMinAvailableHosts)
	overrideIfSet(&configuration.Perf.HostStalenessSeconds, tunables.PerfHostStalenessSeconds)
	overrideIfSet(&configuration.Modes.Disabled, tunables.Disabled)
	overrideIfSet(&configuration.Modes.DisabledMessage, tunables.DisabledMessage)
	overrideIfSet(&configuration.Modes.DisabledRedirectURL, tunables.DisabledRedirectURL)
	overrideIfSet(&configuration.Rotation.Enabled, tunables.RotationEnabled)
	overrideIfSet(&configuration.Rotation.SettlementEnabled, tunables.RotationSettlementEnabled)
	overrideIfSet(&configuration.Rotation.PreviousEpochSettlementEnabled, tunables.RotationPreviousEpochSettlementEnabled)
	overrideIfSet(&configuration.Rotation.PrePoCBlocks, tunables.RotationPrePoCBlocks)
	overrideIfSet(&configuration.Rotation.ModelsJSON, tunables.RotationModelsJSON)
	overrideIfSet(&configuration.Rotation.SettleMarginBlocks, tunables.RotationSettleMarginBlocks)
}

func pocWeightLimit(values env.Values, plainDefault float64) *float64 {
	if values.PoCMaxConcurrentRequestsPer10000Weight != nil || values.MaxConcurrentRequestsPer10000Weight == nil ||
		*values.MaxConcurrentRequestsPer10000Weight == plainDefault {
		return values.PoCMaxConcurrentRequestsPer10000Weight
	}
	return values.MaxConcurrentRequestsPer10000Weight
}
