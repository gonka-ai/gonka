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
	overrideIfSet(&configuration.Chain.SnapshotMaxAgeSeconds, values.ChainSnapshotMaxAgeSeconds)
	overrideIfSet(&configuration.Chain.VersionsPollIntervalSeconds, values.ChainVersionsPollIntervalSeconds)
	overrideIfSet(&configuration.Tx.FeeAmount, values.TxFeeAmount)
	overrideIfSet(&configuration.Tx.GasLimit, values.TxGasLimit)
	overrideIfSet(&configuration.Tx.FeeDenom, values.TxFeeDenom)
	overrideIfSet(&configuration.Tx.PollIntervalMS, values.TxPollIntervalMS)
	overrideIfSet(&configuration.Tx.PollTimeoutMS, values.TxPollTimeoutMS)

	overrideIfSet(&configuration.Limits.DefaultMaxTokens, values.DefaultMaxTokens)
	overrideIfSet(&configuration.Limits.MaxTokensCap, values.MaxTokensCap)
	overrideIfSet(&configuration.Limits.ForceUpstreamStreaming, values.ForceUpstreamStreaming)
	configuration.Limits.LogprobsOptimization = cmp.Or(values.LogprobsOptimization, configuration.Limits.LogprobsOptimization)
	overrideIfSet(&configuration.Limits.MaxBufferedResponseBytes, values.MaxBufferedResponseBytes)
	overrideIfSet(&configuration.Limits.Concurrency.MaxRequests, values.MaxConcurrentRequests)
	overrideIfSet(&configuration.Limits.Concurrency.RequestsPer10000Weight, values.MaxConcurrentRequestsPer10000Weight)
	overrideIfSet(&configuration.Limits.Concurrency.PoCRequestsPer10000Weight, pocWeightLimit(values, Defaults().Limits.Concurrency.RequestsPer10000Weight))
	overrideIfSet(&configuration.Limits.MaxInputTokensInFlight, values.MaxInputTokensInFlight)
	overrideIfSet(&configuration.Limits.AdmissionQueueWaitMS, values.AdmissionQueueWaitMS)
	overrideIfSet(&configuration.Limits.AdmissionQueuePerSlot, values.AdmissionQueuePerSlot)
	overrideIfSet(&configuration.Scheduler.MatchWaitMS, values.MatchWaitMS)
	overrideIfSet(&configuration.Scheduler.MaxConsecutiveBurns, values.MaxConsecutiveBurns)
	overrideIfSet(&configuration.Scheduler.DispatchQueueDepth, values.DispatchQueueDepth)
	overrideIfSet(&configuration.Scheduler.WarmNewEscrows, values.WarmNewEscrows)

	overrideIfSet(&configuration.Modes.PoCMode, values.PoCMode)
	overrideIfSet(&configuration.Modes.Disabled, values.Disabled)
	overrideIfSet(&configuration.Modes.DisabledMessage, values.DisabledMessage)
	overrideIfSet(&configuration.Modes.DisabledRedirectURL, values.DisabledRedirectURL)

	overrideIfSet(&configuration.Rotation.Enabled, values.RotationEnabled)
	overrideIfSet(&configuration.Rotation.PrePoCBlocks, values.RotationPrePoCBlocks)
	overrideIfSet(&configuration.Rotation.SettlementEnabled, values.RotationSettlementEnabled)
	overrideIfSet(&configuration.Rotation.PreviousEpochSettlementEnabled, values.RotationPreviousEpochSettlementEnabled)
	overrideIfSet(&configuration.Rotation.ModelsJSON, values.RotationModelsJSON)
	overrideIfSet(&configuration.Rotation.SettleMarginBlocks, values.RotationSettleMarginBlocks)

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
	overrideIfSet(&configuration.Perf.EWMAHalfLifeSeconds, values.PerfEWMAHalfLifeSeconds)
	overrideIfSet(&configuration.Perf.ConsecutiveFailThreshold, values.PerfConsecutiveFailThreshold)
	overrideIfSet(&configuration.Perf.FailureRateThreshold, values.PerfFailureRateThreshold)
	overrideIfSet(&configuration.Perf.FailureRateMinVolume, values.PerfFailureRateMinVolume)
	overrideIfSet(&configuration.Perf.EjectionBaseSeconds, values.PerfEjectionBaseSeconds)
	overrideIfSet(&configuration.Perf.EjectionMaxSeconds, values.PerfEjectionMaxSeconds)
	overrideIfSet(&configuration.Perf.MaxEjectionFraction, values.PerfMaxEjectionFraction)
	overrideIfSet(&configuration.Perf.MinAvailableHosts, values.PerfMinAvailableHosts)
	overrideIfSet(&configuration.Perf.HostStalenessSeconds, values.PerfHostStalenessSeconds)

	overrideIfSet(&configuration.Engine.ReceiptTimeoutMS, values.EngineReceiptTimeoutMS)
	overrideIfSet(&configuration.Engine.FirstTokenFloorMS, values.EngineFirstTokenFloorMS)
	overrideIfSet(&configuration.Engine.FirstTokenCeilingMS, values.EngineFirstTokenCeilingMS)
	overrideIfSet(&configuration.Engine.InterChunkStallMS, values.EngineInterChunkStallMS)
	overrideIfSet(&configuration.Engine.LoserGraceMS, values.EngineLoserGraceMS)
	overrideIfSet(&configuration.Engine.MaxConcurrentTimeoutVotes, values.EngineMaxConcurrentTimeoutVotes)
	overrideIfSet(&configuration.Engine.HedgeFirstTokenFloorMS, values.EngineHedgeFirstTokenFloorMS)

	// Admin-override layer (wins over env).
	overrideIfSet(&configuration.Limits.DefaultMaxTokens, overrides.DefaultMaxTokens)
	overrideIfSet(&configuration.Limits.MaxTokensCap, overrides.MaxTokensCap)
	overrideIfSet(&configuration.Limits.Concurrency.MaxRequests, overrides.MaxConcurrentRequests)
	overrideIfSet(&configuration.Limits.Concurrency.RequestsPer10000Weight, overrides.MaxConcurrentRequestsPer10000Weight)
	overrideIfSet(&configuration.Limits.Concurrency.PoCRequestsPer10000Weight, overrides.PoCMaxConcurrentRequestsPer10000Weight)
	overrideIfSet(&configuration.Limits.MaxInputTokensInFlight, overrides.MaxInputTokensInFlight)
	overrideIfSet(&configuration.Limits.AdmissionQueueWaitMS, overrides.AdmissionQueueWaitMS)
	overrideIfSet(&configuration.Limits.AdmissionQueuePerSlot, overrides.AdmissionQueuePerSlot)
	overrideIfSet(&configuration.Scheduler.MatchWaitMS, overrides.MatchWaitMS)
	overrideIfSet(&configuration.Scheduler.MaxConsecutiveBurns, overrides.MaxConsecutiveBurns)
	overrideIfSet(&configuration.Limits.ForceUpstreamStreaming, overrides.ForceUpstreamStreaming)
	configuration.Limits.LogprobsOptimization = cmp.Or(overrides.LogprobsOptimization, configuration.Limits.LogprobsOptimization)
	overrideIfSet(&configuration.Limits.MaxBufferedResponseBytes, overrides.MaxBufferedResponseBytes)
	overrideIfSet(&configuration.Scheduler.WarmNewEscrows, overrides.WarmNewEscrows)
	overrideIfSet(&configuration.Chain.SnapshotMaxAgeSeconds, overrides.ChainSnapshotMaxAgeSeconds)
	overrideIfSet(&configuration.Engine.ReceiptTimeoutMS, overrides.EngineReceiptTimeoutMS)
	overrideIfSet(&configuration.Engine.FirstTokenFloorMS, overrides.EngineFirstTokenFloorMS)
	overrideIfSet(&configuration.Engine.FirstTokenCeilingMS, overrides.EngineFirstTokenCeilingMS)
	overrideIfSet(&configuration.Engine.InterChunkStallMS, overrides.EngineInterChunkStallMS)
	overrideIfSet(&configuration.Engine.LoserGraceMS, overrides.EngineLoserGraceMS)
	overrideIfSet(&configuration.Engine.MaxConcurrentTimeoutVotes, overrides.EngineMaxConcurrentTimeoutVotes)
	overrideIfSet(&configuration.Engine.HedgeFirstTokenFloorMS, overrides.EngineHedgeFirstTokenFloorMS)
	overrideIfSet(&configuration.Perf.EWMAHalfLifeSeconds, overrides.PerfEWMAHalfLifeSeconds)
	overrideIfSet(&configuration.Perf.ConsecutiveFailThreshold, overrides.PerfConsecutiveFailThreshold)
	overrideIfSet(&configuration.Perf.FailureRateThreshold, overrides.PerfFailureRateThreshold)
	overrideIfSet(&configuration.Perf.FailureRateMinVolume, overrides.PerfFailureRateMinVolume)
	overrideIfSet(&configuration.Perf.EjectionBaseSeconds, overrides.PerfEjectionBaseSeconds)
	overrideIfSet(&configuration.Perf.EjectionMaxSeconds, overrides.PerfEjectionMaxSeconds)
	overrideIfSet(&configuration.Perf.MaxEjectionFraction, overrides.PerfMaxEjectionFraction)
	overrideIfSet(&configuration.Perf.MinAvailableHosts, overrides.PerfMinAvailableHosts)
	overrideIfSet(&configuration.Perf.HostStalenessSeconds, overrides.PerfHostStalenessSeconds)
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
	overrideIfSet(&configuration.Modes.Disabled, overrides.Disabled)
	overrideIfSet(&configuration.Modes.DisabledMessage, overrides.DisabledMessage)
	overrideIfSet(&configuration.Modes.DisabledRedirectURL, overrides.DisabledRedirectURL)
	overrideIfSet(&configuration.Rotation.Enabled, overrides.RotationEnabled)
	overrideIfSet(&configuration.Rotation.SettlementEnabled, overrides.RotationSettlementEnabled)
	overrideIfSet(&configuration.Rotation.PreviousEpochSettlementEnabled, overrides.RotationPreviousEpochSettlementEnabled)
	overrideIfSet(&configuration.Rotation.PrePoCBlocks, overrides.RotationPrePoCBlocks)
	overrideIfSet(&configuration.Rotation.ModelsJSON, overrides.RotationModelsJSON)
	overrideIfSet(&configuration.Rotation.SettleMarginBlocks, overrides.RotationSettleMarginBlocks)

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

func pocWeightLimit(values env.Values, plainDefault float64) *float64 {
	if values.PoCMaxConcurrentRequestsPer10000Weight != nil || values.MaxConcurrentRequestsPer10000Weight == nil ||
		*values.MaxConcurrentRequestsPer10000Weight == plainDefault {
		return values.PoCMaxConcurrentRequestsPer10000Weight
	}
	return values.MaxConcurrentRequestsPer10000Weight
}
