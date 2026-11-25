package config

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ModelSettings is one model's overrides of the network-wide settings of the same name; a nil field inherits the network value. See README.md, "Per-model settings".
type ModelSettings struct {
	MaxBurnsPerRequest                     *int64                `json:"max_burns_per_request,omitempty"`
	MaxConsecutiveBurns                    *int64                `json:"max_consecutive_burns,omitempty"`
	MatchWaitMS                            *int64                `json:"match_wait_ms,omitempty"`
	EngineReceiptTimeoutMS                 *int64                `json:"engine_receipt_timeout_ms,omitempty"`
	EngineFirstTokenFloorMS                *int64                `json:"engine_first_token_floor_ms,omitempty"`
	EngineFirstTokenCeilingMS              *int64                `json:"engine_first_token_ceiling_ms,omitempty"`
	EngineInterChunkStallMS                *int64                `json:"engine_inter_chunk_stall_ms,omitempty"`
	EngineLoserGraceMS                     *int64                `json:"engine_loser_grace_ms,omitempty"`
	EngineHedgeFirstTokenFloorMS           *int64                `json:"engine_hedge_first_token_floor_ms,omitempty"`
	EngineMaxAttemptsPerRequest            *int64                `json:"engine_max_attempts_per_request,omitempty"`
	EngineMaxStartedAttemptsPerRequest     *int64                `json:"engine_max_started_attempts_per_request,omitempty"`
	DrainTimeoutSeconds                    *int64                `json:"drain_timeout_seconds,omitempty"`
	PerfEWMAHalfLifeSeconds                *int64                `json:"perf_ewma_halflife_seconds,omitempty"`
	PerfConsecutiveFailThreshold           *int64                `json:"perf_consecutive_fail_threshold,omitempty"`
	PerfFailureRateThreshold               *float64              `json:"perf_failure_rate_threshold,omitempty"`
	PerfFailureRateMinVolume               *float64              `json:"perf_failure_rate_min_volume,omitempty"`
	PerfEjectionBaseSeconds                *int64                `json:"perf_ejection_base_seconds,omitempty"`
	PerfEjectionMaxSeconds                 *int64                `json:"perf_ejection_max_seconds,omitempty"`
	PerfMaxEjectionFraction                *float64              `json:"perf_max_ejection_fraction,omitempty"`
	PerfMinAvailableHosts                  *int64                `json:"perf_min_available_hosts,omitempty"`
	HostInputWindowMinRequests             *int64                `json:"host_input_window_min_requests,omitempty"`
	HostInputWindowInitialRequests         *int64                `json:"host_input_window_initial_requests,omitempty"`
	HostOutputWindowMinRequests            *int64                `json:"host_output_window_min_requests,omitempty"`
	HostOutputWindowInitialRequests        *int64                `json:"host_output_window_initial_requests,omitempty"`
	IOAIMDBetaSoft                         *float64              `json:"io_aimd_beta_soft,omitempty"`
	IOAIMDBetaHard                         *float64              `json:"io_aimd_beta_hard,omitempty"`
	IOAIMDBetaSevere                       *float64              `json:"io_aimd_beta_severe,omitempty"`
	IOAIMDBetaCross                        *float64              `json:"io_aimd_beta_cross,omitempty"`
	HostCongestionSlack                    *float64              `json:"host_congestion_slack,omitempty"`
	HostCutoffAfterFailures                *int64                `json:"host_cutoff_after_failures,omitempty"`
	HostCutoffMS                           *int64                `json:"host_cutoff_ms,omitempty"`
	HostCutoffMaxMS                        *int64                `json:"host_cutoff_max_ms,omitempty"`
	MaxConcurrentRequestsPer10000Weight    *float64              `json:"max_concurrent_requests_per_10000_weight,omitempty"`
	PoCMaxConcurrentRequestsPer10000Weight *float64              `json:"poc_max_concurrent_requests_per_10000_weight,omitempty"`
	AdmissionQueuePerSlot                  *int64                `json:"admission_queue_per_slot,omitempty"`
	AdmissionQueueWaitMS                   *int64                `json:"admission_queue_wait_ms,omitempty"`
	LogprobsOptimization                   *bool                 `json:"logprobs_optimization,omitempty"`
	ForceUpstreamStreaming                 *bool                 `json:"force_upstream_streaming,omitempty"`
	MaxConcurrentRequests                  *int64                `json:"max_concurrent_requests,omitempty"`
	MaxInputTokensInFlight                 *int64                `json:"max_input_tokens_in_flight,omitempty"`
	DefaultMaxTokens                       *int64                `json:"default_max_tokens,omitempty"`
	MaxTokensCap                           *int64                `json:"max_tokens_cap,omitempty"`
	MaxModelLen                            *int64                `json:"max_model_len,omitempty"`
	Access                                 *string               `json:"access,omitempty"`
	Participants                           *ParticipantOverrides `json:"participants,omitempty"`
}

// For is the configuration a decision about model runs under: the model's own effective configuration, else the network one.
func (c *Config) For(model string) *Config {
	if effective, named := c.models[model]; named {
		return effective
	}
	return c
}

// ModelNames lists, sorted, the models that carry a block of their own settings.
func (c *Config) ModelNames() []string {
	return slices.Sorted(maps.Keys(c.models))
}

// HasModels reports whether any model carries a block of its own settings.
func (c *Config) HasModels() bool {
	return len(c.models) > 0
}

// Offers reports whether the operator names the model in the models setting. See operations.md, "Who may call what".
func (c *Config) Offers(model string) bool {
	_, named := c.models[model]
	return named
}

// CheckStoredModels refuses, as ErrInvalid, a stored models block for a model rotation_models_json no longer lists; boot tolerates one so the admin API stays reachable to clear it.
func (c *Config) CheckStoredModels() error {
	if len(c.staleModels) == 0 {
		return nil
	}
	return fmt.Errorf("%w: models[%s]: no model of that id in rotation_models_json, clear it with a null", ErrInvalid, strings.Join(c.staleModels, ", "))
}

func applyModelSettings(configuration *Config, settings ModelSettings) {
	overrideIfSet(&configuration.Scheduler.MaxBurnsPerRequest, settings.MaxBurnsPerRequest)
	overrideIfSet(&configuration.Scheduler.MaxConsecutiveBurns, settings.MaxConsecutiveBurns)
	overrideIfSet(&configuration.Scheduler.MatchWaitMS, settings.MatchWaitMS)
	overrideIfSet(&configuration.Engine.ReceiptTimeoutMS, settings.EngineReceiptTimeoutMS)
	overrideIfSet(&configuration.Engine.FirstTokenFloorMS, settings.EngineFirstTokenFloorMS)
	overrideIfSet(&configuration.Engine.FirstTokenCeilingMS, settings.EngineFirstTokenCeilingMS)
	overrideIfSet(&configuration.Engine.InterChunkStallMS, settings.EngineInterChunkStallMS)
	overrideIfSet(&configuration.Engine.LoserGraceMS, settings.EngineLoserGraceMS)
	overrideIfSet(&configuration.Engine.HedgeFirstTokenFloorMS, settings.EngineHedgeFirstTokenFloorMS)
	overrideIfSet(&configuration.Engine.MaxAttemptsPerRequest, settings.EngineMaxAttemptsPerRequest)
	overrideIfSet(&configuration.Engine.MaxStartedAttemptsPerRequest, settings.EngineMaxStartedAttemptsPerRequest)
	overrideIfSet(&configuration.Stream.DrainTimeoutSeconds, settings.DrainTimeoutSeconds)
	overrideIfSet(&configuration.Perf.EWMAHalfLifeSeconds, settings.PerfEWMAHalfLifeSeconds)
	overrideIfSet(&configuration.Perf.ConsecutiveFailThreshold, settings.PerfConsecutiveFailThreshold)
	overrideIfSet(&configuration.Perf.FailureRateThreshold, settings.PerfFailureRateThreshold)
	overrideIfSet(&configuration.Perf.FailureRateMinVolume, settings.PerfFailureRateMinVolume)
	overrideIfSet(&configuration.Perf.EjectionBaseSeconds, settings.PerfEjectionBaseSeconds)
	overrideIfSet(&configuration.Perf.EjectionMaxSeconds, settings.PerfEjectionMaxSeconds)
	overrideIfSet(&configuration.Perf.MaxEjectionFraction, settings.PerfMaxEjectionFraction)
	overrideIfSet(&configuration.Perf.MinAvailableHosts, settings.PerfMinAvailableHosts)
	overrideIfSet(&configuration.Limits.HostWindows.Input.MinRequests, settings.HostInputWindowMinRequests)
	overrideIfSet(&configuration.Limits.HostWindows.Input.InitialRequests, settings.HostInputWindowInitialRequests)
	overrideIfSet(&configuration.Limits.HostWindows.Output.MinRequests, settings.HostOutputWindowMinRequests)
	overrideIfSet(&configuration.Limits.HostWindows.Output.InitialRequests, settings.HostOutputWindowInitialRequests)
	overrideIfSet(&configuration.Limits.Congestion.BetaSoft, settings.IOAIMDBetaSoft)
	overrideIfSet(&configuration.Limits.Congestion.BetaHard, settings.IOAIMDBetaHard)
	overrideIfSet(&configuration.Limits.Congestion.BetaSevere, settings.IOAIMDBetaSevere)
	overrideIfSet(&configuration.Limits.Congestion.BetaCross, settings.IOAIMDBetaCross)
	overrideIfSet(&configuration.Limits.Congestion.Slack, settings.HostCongestionSlack)
	overrideIfSet(&configuration.Limits.HostCutoff.AfterFailures, settings.HostCutoffAfterFailures)
	overrideIfSet(&configuration.Limits.HostCutoff.BaseMS, settings.HostCutoffMS)
	overrideIfSet(&configuration.Limits.HostCutoff.MaxMS, settings.HostCutoffMaxMS)
	overrideIfSet(&configuration.Limits.Concurrency.RequestsPer10000Weight, settings.MaxConcurrentRequestsPer10000Weight)
	overrideIfSet(&configuration.Limits.Concurrency.PoCRequestsPer10000Weight, settings.PoCMaxConcurrentRequestsPer10000Weight)
	overrideIfSet(&configuration.Limits.AdmissionQueuePerSlot, settings.AdmissionQueuePerSlot)
	overrideIfSet(&configuration.Limits.AdmissionQueueWaitMS, settings.AdmissionQueueWaitMS)
	configuration.Limits.LogprobsOptimization = cmp.Or(settings.LogprobsOptimization, configuration.Limits.LogprobsOptimization)
	overrideIfSet(&configuration.Limits.ForceUpstreamStreaming, settings.ForceUpstreamStreaming)
	overrideIfSet(&configuration.Limits.Concurrency.MaxRequests, settings.MaxConcurrentRequests)
	overrideIfSet(&configuration.Limits.MaxInputTokensInFlight, settings.MaxInputTokensInFlight)
	overrideIfSet(&configuration.Limits.DefaultMaxTokens, settings.DefaultMaxTokens)
	overrideIfSet(&configuration.Limits.MaxTokensCap, settings.MaxTokensCap)
	if settings.MaxModelLen != nil {
		pinned := *settings.MaxModelLen
		configuration.Limits.MaxModelLen = &pinned
	}
	overrideIfSet(&configuration.Limits.Access, settings.Access)
	if settings.Participants != nil {
		applyParticipants(&configuration.Scheduler.Participants, *settings.Participants)
	}
}
