package config

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strings"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/filters"
)

func notFinite(value float64) bool { return math.IsNaN(value) || math.IsInf(value, 0) }

func outsideWeightRateRange(requestsPer10000Weight float64) bool {
	return notFinite(requestsPer10000Weight) || requestsPer10000Weight < 0 || requestsPer10000Weight > maxConcurrentRequests
}

// MaxContextTokens is the longest context a model may be priced at. See capacity.md, "The participant limiter: IOCW".
const (
	MaxContextTokens = 100_000_000

	maxWindowRequests        = 1_000_000
	maxConcurrentRequests    = 10_000_000
	maxAdmissionQueuePerSlot = 10_000

	maxConsecutiveBurns   = 1_000
	maxDispatchQueueDepth = 65_536

	maxSnapshotAgeSeconds          = 86_400
	maxVersionsPollIntervalSeconds = 86_400
	maxEngineTimingMS              = 86_400_000
	snapshotAgePollMultiple        = 7

	minAddressDataLength = 4

	maxTCPPort = 65535
)

// ErrInvalid marks a configuration the operator got wrong, so the surface answers 400 rather than 502.
var ErrInvalid = errors.New("invalid configuration")

// Validate reports every problem at once, naming fields in the admin-API snake_case spelling.
func (c *Config) Validate() error {
	var problems []error
	complain := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf(format, args...))
	}

	if c.Server.Port < 1 || c.Server.Port > 65535 {
		complain("port: %d out of range 1..65535", c.Server.Port)
	}
	if c.Server.MaxConcurrentRuntimeBuilds < 1 {
		complain("max_concurrent_runtime_builds: %d must be >= 1", c.Server.MaxConcurrentRuntimeBuilds)
	}
	if c.Server.MaxConcurrentPromptCounts < 1 {
		complain("max_concurrent_prompt_counts: %d must be >= 1", c.Server.MaxConcurrentPromptCounts)
	}
	if c.Server.SessionRetentionEpochs < 0 {
		complain("session_retention_epochs: %d must be >= 0", c.Server.SessionRetentionEpochs)
	}
	if c.HeightSync.QuietSettleDelayMS < 1 {
		complain("quiet_settle_delay_ms: %d must be >= 1", c.HeightSync.QuietSettleDelayMS)
	}
	checkBaseURL := func(name, value string) {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			complain("%s: %q is not an absolute URL", name, value)
		}
	}
	checkBaseURL("public_api", c.Chain.PublicAPIBaseURL)
	// A gRPC target is host:port with no scheme, so the URL check above would pass anything.
	if _, _, err := net.SplitHostPort(c.Chain.GRPCEndpoint); err != nil {
		complain("chain_grpc: %q is not host:port", c.Chain.GRPCEndpoint)
	}
	if c.Chain.SnapshotMaxAgeSeconds < 0 || c.Chain.SnapshotMaxAgeSeconds > maxSnapshotAgeSeconds {
		complain("chain_snapshot_max_age_seconds: %d must be between 0 (disabled) and %d", c.Chain.SnapshotMaxAgeSeconds, maxSnapshotAgeSeconds)
	}
	if minimum := int64(chain.DefaultObserverPollInterval.Seconds()) * snapshotAgePollMultiple; c.Chain.SnapshotMaxAgeSeconds > 0 && c.Chain.SnapshotMaxAgeSeconds < minimum {
		complain("chain_snapshot_max_age_seconds: %d must be 0 or at least %d, the observer's own refresh cadence", c.Chain.SnapshotMaxAgeSeconds, minimum)
	}
	if c.Chain.VersionsPollIntervalSeconds < 1 || c.Chain.VersionsPollIntervalSeconds > maxVersionsPollIntervalSeconds {
		complain("chain_versions_poll_interval_seconds: %d must be between 1 and %d", c.Chain.VersionsPollIntervalSeconds, maxVersionsPollIntervalSeconds)
	}
	if c.Tx.FeeAmount < 0 {
		complain("tx_fee_amount: %d must be >= 0", c.Tx.FeeAmount)
	}
	if c.Tx.GasLimit < 1 {
		complain("tx_gas_limit: %d must be >= 1", c.Tx.GasLimit)
	}
	if c.Tx.PollIntervalMS < 1 {
		complain("tx_poll_interval_ms: %d must be >= 1", c.Tx.PollIntervalMS)
	}
	if c.Tx.PollTimeoutMS < c.Tx.PollIntervalMS {
		complain("tx_poll_timeout_ms: %d must be >= tx_poll_interval_ms %d", c.Tx.PollTimeoutMS, c.Tx.PollIntervalMS)
	}

	if c.Limits.DefaultMaxTokens < 1 || c.Limits.DefaultMaxTokens > int64(filters.MaxOutputTokens) {
		complain("default_max_tokens: %d must be between 1 and %d", c.Limits.DefaultMaxTokens, filters.MaxOutputTokens)
	}
	if c.Limits.MaxTokensCap < 1 || c.Limits.MaxTokensCap > int64(filters.MaxOutputTokens) {
		complain("max_tokens_cap: %d must be between 1 and %d", c.Limits.MaxTokensCap, filters.MaxOutputTokens)
	}
	if c.Limits.Concurrency.MaxRequests < 0 || c.Limits.Concurrency.MaxRequests > maxConcurrentRequests {
		complain("max_concurrent_requests: %d must be between 0 and %d", c.Limits.Concurrency.MaxRequests, maxConcurrentRequests)
	}
	if c.Limits.AdmissionQueuePerSlot < 0 || c.Limits.AdmissionQueuePerSlot > maxAdmissionQueuePerSlot {
		complain("admission_queue_per_slot: %d must be between 0 and %d", c.Limits.AdmissionQueuePerSlot, maxAdmissionQueuePerSlot)
	}
	if outsideWeightRateRange(c.Limits.Concurrency.RequestsPer10000Weight) {
		complain("max_concurrent_requests_per_10000_weight: %v must be between 0 and %d", c.Limits.Concurrency.RequestsPer10000Weight, maxConcurrentRequests)
	}
	if outsideWeightRateRange(c.Limits.Concurrency.PoCRequestsPer10000Weight) {
		complain("poc_max_concurrent_requests_per_10000_weight: %v must be between 0 and %d", c.Limits.Concurrency.PoCRequestsPer10000Weight, maxConcurrentRequests)
	}
	if c.Limits.MaxInputTokensInFlight < 0 {
		complain("max_input_tokens_in_flight: %d must be >= 0", c.Limits.MaxInputTokensInFlight)
	}
	if c.Limits.AdmissionQueueWaitMS < 0 {
		complain("admission_queue_wait_ms: %d must be >= 0", c.Limits.AdmissionQueueWaitMS)
	}
	validateRequestWindow("host_input_window", c.Limits.HostWindows.Input, complain)
	validateRequestWindow("host_output_window", c.Limits.HostWindows.Output, complain)
	if c.Limits.FallbackMaxModelLen < 1 || c.Limits.FallbackMaxModelLen > MaxContextTokens {
		complain("fallback_max_model_len: %d must be between 1 and %d", c.Limits.FallbackMaxModelLen, MaxContextTokens)
	}
	if notFinite(c.Limits.Congestion.BetaSoft) || c.Limits.Congestion.BetaSoft <= 0 || c.Limits.Congestion.BetaSoft >= 1 {
		complain("io_aimd_beta_soft: %v must be > 0 and < 1", c.Limits.Congestion.BetaSoft)
	}
	if notFinite(c.Limits.Congestion.BetaHard) || c.Limits.Congestion.BetaHard <= 0 || c.Limits.Congestion.BetaHard >= 1 {
		complain("io_aimd_beta_hard: %v must be > 0 and < 1", c.Limits.Congestion.BetaHard)
	}
	if notFinite(c.Limits.Congestion.BetaSevere) || c.Limits.Congestion.BetaSevere <= 0 || c.Limits.Congestion.BetaSevere >= 1 {
		complain("io_aimd_beta_severe: %v must be > 0 and < 1", c.Limits.Congestion.BetaSevere)
	}
	if notFinite(c.Limits.Congestion.BetaCross) || c.Limits.Congestion.BetaCross <= 0 || c.Limits.Congestion.BetaCross > 1 {
		complain("io_aimd_beta_cross: %v must be > 0 and <= 1", c.Limits.Congestion.BetaCross)
	}
	if notFinite(c.Limits.Congestion.Slack) || c.Limits.Congestion.Slack < 0 {
		complain("host_congestion_slack: %v must be >= 0", c.Limits.Congestion.Slack)
	}
	if c.Limits.HostCutoff.AfterFailures < 1 {
		complain("host_cutoff_after_failures: %d must be >= 1", c.Limits.HostCutoff.AfterFailures)
	}
	if c.Limits.HostCutoff.BaseMS < 1 {
		complain("host_cutoff_ms: %d must be >= 1", c.Limits.HostCutoff.BaseMS)
	}
	if c.Limits.HostCutoff.MaxMS < c.Limits.HostCutoff.BaseMS {
		complain("host_cutoff_max_ms: %d must be >= host_cutoff_ms %d", c.Limits.HostCutoff.MaxMS, c.Limits.HostCutoff.BaseMS)
	}
	if perfEjectionMaxMS := c.Perf.EjectionMaxSeconds * time.Second.Milliseconds(); c.Limits.HostCutoff.MaxMS > perfEjectionMaxMS {
		complain("host_cutoff_max_ms: %d must be <= perf_ejection_max_seconds %d (ms) so perf stays the dominant ejection authority", c.Limits.HostCutoff.MaxMS, perfEjectionMaxMS)
	}
	// A guessable key is worse than none: "none" disables admin entirely, a weak one authenticates.
	if c.Server.AdminAPIKey != "" && len(c.Server.AdminAPIKey) < minAdminAPIKeyLength {
		complain("admin_api_key: must be at least %d characters when set (empty disables admin routes)", minAdminAPIKeyLength)
	}
	requiresAPIKey := false
	for model, access := range c.Limits.ModelAccess {
		if access != ModelAccessOpen && access != ModelAccessAPIKey && access != ModelAccessAdminOnly {
			complain("model_access[%s]: %q is not open, api_key or admin_only", model, access)
		}
		requiresAPIKey = requiresAPIKey || access == ModelAccessAPIKey
	}
	if requiresAPIKey && len(c.Server.APIKeys) == 0 {
		complain("api_keys: model_access marks a model api_key but no api keys are configured, so it is unreachable")
	}
	for model, limit := range c.Limits.ModelLimits {
		if limit.DefaultMaxTokens < 1 || limit.MaxTokensCap < 1 {
			complain("model_limits[%s]: default %d / cap %d invalid", model, limit.DefaultMaxTokens, limit.MaxTokensCap)
		}
		if limit.MaxTokensCap > int64(filters.MaxOutputTokens) || limit.DefaultMaxTokens > int64(filters.MaxOutputTokens) {
			complain("model_limits[%s]: default %d / cap %d must be <= %d", model, limit.DefaultMaxTokens, limit.MaxTokensCap, filters.MaxOutputTokens)
		}
		if limit.MaxModelLen != nil && (*limit.MaxModelLen < 1 || *limit.MaxModelLen > MaxContextTokens) {
			complain("model_limits[%s]: max_model_len %d must be between 1 and %d", model, *limit.MaxModelLen, MaxContextTokens)
		}
		if limit.MaxConcurrentRequests != nil && *limit.MaxConcurrentRequests < 1 {
			complain("model_limits[%s]: max_concurrent_requests %d must be >= 1", model, *limit.MaxConcurrentRequests)
		}
		if limit.MaxInputTokensInFlight != nil && *limit.MaxInputTokensInFlight < 0 {
			complain("model_limits[%s]: max_input_tokens_in_flight %d must be >= 0", model, *limit.MaxInputTokensInFlight)
		}
	}

	if c.Modes.PoCMode != PoCModeOff && c.Modes.PoCMode != PoCModeRelaxed {
		complain("poc_mode: %q is not %q or %q", c.Modes.PoCMode, PoCModeOff, PoCModeRelaxed)
	}
	if c.Rotation.PrePoCBlocks < 0 {
		complain("rotation_pre_poc_blocks: %d must be >= 0", c.Rotation.PrePoCBlocks)
	}
	if c.Rotation.SettleMarginBlocks < 1 {
		complain("rotation_settle_margin_blocks: %d must be >= 1", c.Rotation.SettleMarginBlocks)
	}
	if c.Cache.ChatCacheMaxBytes < 0 {
		complain("chat_cache_max_bytes: %d must be >= 0", c.Cache.ChatCacheMaxBytes)
	}
	if c.Accounting.RetentionHours < 1 {
		complain("accounting_retention_hours: %d must be >= 1", c.Accounting.RetentionHours)
	}
	if c.Accounting.RetentionMaxRows < 1 {
		complain("accounting_retention_max_rows: %d must be >= 1", c.Accounting.RetentionMaxRows)
	}
	if c.TimeoutSweep.BudgetPerTick < 0 {
		complain("timeout_sweep_budget_per_tick: %d must be >= 0", c.TimeoutSweep.BudgetPerTick)
	}

	if c.TimeoutSweep.GraceSeconds < 0 {
		complain("timeout_sweep_grace_seconds: %d must be >= 0", c.TimeoutSweep.GraceSeconds)
	}

	if c.NonceAccounting.RetentionEpochs < 0 {
		complain("nonce_accounting_retention_epochs: %d must be >= 0", c.NonceAccounting.RetentionEpochs)
	}
	if c.NonceAccounting.SnapshotSeconds < 1 {
		complain("nonce_accounting_snapshot_seconds: %d must be >= 1", c.NonceAccounting.SnapshotSeconds)
	}
	if portOutOfRange := c.NonceAccounting.Port < 1 || c.NonceAccounting.Port > maxTCPPort; c.NonceAccounting.Enabled && portOutOfRange {
		complain("nonce_accounting_port: %d out of range 1..%d while nonce accounting is enabled", c.NonceAccounting.Port, maxTCPPort)
	}
	if notFinite(c.Capture.SampleRate) || c.Capture.SampleRate < 0 || c.Capture.SampleRate > 1 {
		complain("capture_sample_rate: %v must be in [0, 1]", c.Capture.SampleRate)
	}
	if c.Capture.MaxBytes < 1 {
		complain("capture_max_bytes: %d must be >= 1", c.Capture.MaxBytes)
	}
	if c.Stream.DrainTimeoutSeconds < 1 {
		complain("drain_timeout_seconds: %d must be >= 1", c.Stream.DrainTimeoutSeconds)
	}
	if c.Stream.ClassifyMaxAttemptBytes < 1 {
		complain("classify_max_attempt_bytes: %d must be >= 1", c.Stream.ClassifyMaxAttemptBytes)
	}
	if c.Stream.ClassifyMaxParticipantBytes < c.Stream.ClassifyMaxAttemptBytes {
		complain("classify_max_participant_bytes: %d must be >= classify_max_attempt_bytes %d", c.Stream.ClassifyMaxParticipantBytes, c.Stream.ClassifyMaxAttemptBytes)
	}
	if c.Stream.ClassifyMaxGlobalBytes < c.Stream.ClassifyMaxParticipantBytes {
		complain("classify_max_global_bytes: %d must be >= classify_max_participant_bytes %d", c.Stream.ClassifyMaxGlobalBytes, c.Stream.ClassifyMaxParticipantBytes)
	}

	if c.Perf.EWMAHalfLifeSeconds < 1 {
		complain("perf_ewma_half_life_seconds: %d must be >= 1", c.Perf.EWMAHalfLifeSeconds)
	}
	if c.Perf.ConsecutiveFailThreshold < 1 {
		complain("perf_consecutive_fail_threshold: %d must be >= 1", c.Perf.ConsecutiveFailThreshold)
	}
	if notFinite(c.Perf.FailureRateThreshold) || c.Perf.FailureRateThreshold <= 0 || c.Perf.FailureRateThreshold > 1 {
		complain("perf_failure_rate_threshold: %v must be in (0, 1]", c.Perf.FailureRateThreshold)
	}
	if notFinite(c.Perf.FailureRateMinVolume) || c.Perf.FailureRateMinVolume < 1 {
		complain("perf_failure_rate_min_volume: %v must be >= 1", c.Perf.FailureRateMinVolume)
	}
	if c.Perf.EjectionBaseSeconds < 1 {
		complain("perf_ejection_base_seconds: %d must be >= 1", c.Perf.EjectionBaseSeconds)
	}
	if c.Perf.EjectionMaxSeconds < c.Perf.EjectionBaseSeconds {
		complain("perf_ejection_max_seconds: %d must be >= perf_ejection_base_seconds %d", c.Perf.EjectionMaxSeconds, c.Perf.EjectionBaseSeconds)
	}
	if notFinite(c.Perf.MaxEjectionFraction) || c.Perf.MaxEjectionFraction <= 0 || c.Perf.MaxEjectionFraction > 1 {
		complain("perf_max_ejection_fraction: %v must be in (0, 1]", c.Perf.MaxEjectionFraction)
	}
	if c.Perf.MinAvailableHosts < 0 {
		complain("perf_min_available_hosts: %d must be >= 0", c.Perf.MinAvailableHosts)
	}
	if c.Perf.HostStalenessSeconds < 1 {
		complain("perf_host_staleness_seconds: %d must be >= 1", c.Perf.HostStalenessSeconds)
	}

	if c.Engine.ReceiptTimeoutMS < 1 {
		complain("engine_receipt_timeout_ms: %d must be >= 1", c.Engine.ReceiptTimeoutMS)
	}
	if c.Engine.ReceiptTimeoutMS > maxEngineTimingMS {
		complain("engine_receipt_timeout_ms: %d must be <= %d", c.Engine.ReceiptTimeoutMS, maxEngineTimingMS)
	}
	if c.Engine.FirstTokenFloorMS < 1 {
		complain("engine_first_token_floor_ms: %d must be >= 1", c.Engine.FirstTokenFloorMS)
	}
	if c.Engine.FirstTokenFloorMS > maxEngineTimingMS {
		complain("engine_first_token_floor_ms: %d must be <= %d", c.Engine.FirstTokenFloorMS, maxEngineTimingMS)
	}
	if c.Engine.InterChunkStallMS < 1 {
		complain("engine_inter_chunk_stall_ms: %d must be >= 1", c.Engine.InterChunkStallMS)
	}
	if c.Engine.InterChunkStallMS > maxEngineTimingMS {
		complain("engine_inter_chunk_stall_ms: %d must be <= %d", c.Engine.InterChunkStallMS, maxEngineTimingMS)
	}
	// A grace under the stall window kills losers that are merely between chunks.
	if c.Engine.LoserGraceMS < c.Engine.InterChunkStallMS {
		complain("engine_loser_grace_ms: %d must be >= engine_inter_chunk_stall_ms %d", c.Engine.LoserGraceMS, c.Engine.InterChunkStallMS)
	}
	if c.Engine.LoserGraceMS > maxEngineTimingMS {
		complain("engine_loser_grace_ms: %d must be <= %d", c.Engine.LoserGraceMS, maxEngineTimingMS)
	}
	if c.Engine.FirstTokenCeilingMS > maxEngineTimingMS {
		complain("engine_first_token_ceiling_ms: %d must be <= %d", c.Engine.FirstTokenCeilingMS, maxEngineTimingMS)
	}
	if c.Engine.FirstTokenCeilingMS < c.Engine.FirstTokenFloorMS {
		complain("engine_first_token_ceiling_ms: %d must be >= engine_first_token_floor_ms %d",
			c.Engine.FirstTokenCeilingMS, c.Engine.FirstTokenFloorMS)
	}
	if c.Engine.HedgeFirstTokenFloorMS < 0 {
		complain("engine_hedge_first_token_floor_ms: %d must be >= 0", c.Engine.HedgeFirstTokenFloorMS)
	}
	if c.Engine.HedgeFirstTokenFloorMS > maxEngineTimingMS {
		complain("engine_hedge_first_token_floor_ms: %d must be <= %d", c.Engine.HedgeFirstTokenFloorMS, maxEngineTimingMS)
	}
	if c.Engine.MaxConcurrentTimeoutVotes < 0 {
		complain("engine_max_concurrent_timeout_votes: %d must be >= 0 (0 = as many as races owe votes)", c.Engine.MaxConcurrentTimeoutVotes)
	}
	if c.Engine.MaxAttemptsPerRequest < 0 {
		complain("engine_max_attempts_per_request: %d must be >= 0 (0 = bounded only by the host group)", c.Engine.MaxAttemptsPerRequest)
	}

	// The ceiling is a budget guard: a long grace parks a committed-cost nonce on the chance of a co-arrival.
	if c.Scheduler.MatchWaitMS < 0 || c.Scheduler.MatchWaitMS > 5_000 {
		complain("scheduler_match_wait_ms: %d must be in [0, 5000]", c.Scheduler.MatchWaitMS)
	}
	if c.Scheduler.MaxConsecutiveBurns < 0 || c.Scheduler.MaxConsecutiveBurns > maxConsecutiveBurns {
		complain("scheduler_max_consecutive_burns: %d must be in [0, %d]", c.Scheduler.MaxConsecutiveBurns, maxConsecutiveBurns)
	}
	if c.Scheduler.DispatchQueueDepth < 1 || c.Scheduler.DispatchQueueDepth > maxDispatchQueueDepth {
		complain("scheduler_dispatch_queue_depth: %d must be in [1, %d]", c.Scheduler.DispatchQueueDepth, maxDispatchQueueDepth)
	}
	for index, participant := range c.Scheduler.ParticipantAllowlist {
		if problem := participantEntryProblem(participant); problem != "" {
			complain("scheduler_participant_allowlist[%d]: %s", index, problem)
		}
	}
	for index, participant := range c.Scheduler.UnthrottledParticipants {
		if problem := participantEntryProblem(participant); problem != "" {
			complain("scheduler_unthrottled_participants[%d]: %s", index, problem)
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w: %w", ErrInvalid, errors.Join(problems...))
	}
	return nil
}

func validateRequestWindow(name string, window RequestWindow, complain func(string, ...any)) {
	if window.MinRequests < 1 {
		complain("%s_min_requests: %d must be >= 1", name, window.MinRequests)
	}
	if window.MinRequests > maxWindowRequests {
		complain("%s_min_requests: %d must be <= %d", name, window.MinRequests, maxWindowRequests)
	}
	if window.InitialRequests > maxWindowRequests {
		complain("%s_initial_requests: %d must be <= %d", name, window.InitialRequests, maxWindowRequests)
	}
	if window.InitialRequests < window.MinRequests {
		complain("%s_initial_requests: %d must be >= %s_min_requests %d", name, window.InitialRequests, name, window.MinRequests)
	}
}

// participantEntryProblem refuses a short host label, which matches no participant. See ../docs/operations.md.
func participantEntryProblem(participant string) string {
	entry := strings.TrimSpace(participant)
	if entry == "" {
		return "must not be blank"
	}
	separator := strings.IndexByte(entry, '1')
	if separator <= 0 || len(entry)-separator-1 < minAddressDataLength {
		return fmt.Sprintf("%q is a host label, not a participant address", entry)
	}
	return ""
}
