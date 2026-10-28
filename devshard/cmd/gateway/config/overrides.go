package config

import (
	"bytes"
	"fmt"

	json "github.com/goccy/go-json"
)

// Overrides is the admin-tunable subset of Config, merged over env by Build; a nil field is not overridden.
type Overrides struct {
	DefaultMaxTokens                       *int64                 `json:"default_max_tokens,omitempty"`
	MaxTokensCap                           *int64                 `json:"max_tokens_cap,omitempty"`
	MaxConcurrentRequests                  *int64                 `json:"max_concurrent_requests,omitempty"`
	MaxConcurrentRequestsPer10000Weight    *float64               `json:"max_concurrent_requests_per_10000_weight,omitempty"`
	PoCMaxConcurrentRequestsPer10000Weight *float64               `json:"poc_max_concurrent_requests_per_10000_weight,omitempty"`
	MaxInputTokensInFlight                 *int64                 `json:"max_input_tokens_in_flight,omitempty"`
	AdmissionQueueWaitMS                   *int64                 `json:"admission_queue_wait_ms,omitempty"`
	AdmissionQueuePerSlot                  *int64                 `json:"admission_queue_per_slot,omitempty"`
	MatchWaitMS                            *int64                 `json:"match_wait_ms,omitempty"`
	MaxConsecutiveBurns                    *int64                 `json:"max_consecutive_burns,omitempty"`
	ForceUpstreamStreaming                 *bool                  `json:"force_upstream_streaming,omitempty"`
	LogprobsOptimization                   *bool                  `json:"logprobs_optimization,omitempty"`
	MaxBufferedResponseBytes               *int64                 `json:"max_buffered_response_bytes,omitempty"`
	WarmNewEscrows                         *bool                  `json:"warm_new_escrows,omitempty"`
	ParticipantAllowlist                   *[]string              `json:"participant_allowlist,omitempty"`
	UnthrottledParticipants                *[]string              `json:"unthrottled_participants,omitempty"`
	FallbackMaxModelLen                    *int64                 `json:"fallback_max_model_len,omitempty"`
	HostInputWindowMinRequests             *int64                 `json:"host_input_window_min_requests,omitempty"`
	HostInputWindowInitialRequests         *int64                 `json:"host_input_window_initial_requests,omitempty"`
	HostOutputWindowMinRequests            *int64                 `json:"host_output_window_min_requests,omitempty"`
	HostOutputWindowInitialRequests        *int64                 `json:"host_output_window_initial_requests,omitempty"`
	IOAIMDBetaSoft                         *float64               `json:"io_aimd_beta_soft,omitempty"`
	IOAIMDBetaHard                         *float64               `json:"io_aimd_beta_hard,omitempty"`
	IOAIMDBetaSevere                       *float64               `json:"io_aimd_beta_severe,omitempty"`
	IOAIMDBetaCross                        *float64               `json:"io_aimd_beta_cross,omitempty"`
	HostCongestionSlack                    *float64               `json:"host_congestion_slack,omitempty"`
	HostCutoffAfterFailures                *int64                 `json:"host_cutoff_after_failures,omitempty"`
	HostCutoffMS                           *int64                 `json:"host_cutoff_ms,omitempty"`
	HostCutoffMaxMS                        *int64                 `json:"host_cutoff_max_ms,omitempty"`
	ModelLimits                            map[string]ModelLimits `json:"model_limits,omitempty"`
	ModelAccess                            map[string]string      `json:"model_access,omitempty"`
	Disabled                               *bool                  `json:"disabled,omitempty"`
	DisabledMessage                        *string                `json:"disabled_message,omitempty"`
	DisabledRedirectURL                    *string                `json:"disabled_redirect_url,omitempty"`
	RotationEnabled                        *bool                  `json:"rotation_enabled,omitempty"`
	RotationSettlementEnabled              *bool                  `json:"rotation_settlement_enabled,omitempty"`
	RotationPreviousEpochSettlementEnabled *bool                  `json:"rotation_previous_epoch_settlement_enabled,omitempty"`
	RotationPrePoCBlocks                   *int64                 `json:"rotation_pre_poc_blocks,omitempty"`
	RotationModelsJSON                     *string                `json:"rotation_models_json,omitempty"`
	RotationSettleMarginBlocks             *int64                 `json:"rotation_settle_margin_blocks,omitempty"`
	ChainSnapshotMaxAgeSeconds             *int64                 `json:"chain_snapshot_max_age_seconds,omitempty"`
	EngineReceiptTimeoutMS                 *int64                 `json:"engine_receipt_timeout_ms,omitempty"`
	EngineFirstTokenFloorMS                *int64                 `json:"engine_first_token_floor_ms,omitempty"`
	EngineFirstTokenCeilingMS              *int64                 `json:"engine_first_token_ceiling_ms,omitempty"`
	EngineInterChunkStallMS                *int64                 `json:"engine_inter_chunk_stall_ms,omitempty"`
	EngineLoserGraceMS                     *int64                 `json:"engine_loser_grace_ms,omitempty"`
	EngineMaxConcurrentTimeoutVotes        *int64                 `json:"engine_max_concurrent_timeout_votes,omitempty"`
	EngineHedgeFirstTokenFloorMS           *int64                 `json:"engine_hedge_first_token_floor_ms,omitempty"`
	PerfEWMAHalfLifeSeconds                *int64                 `json:"perf_ewma_halflife_seconds,omitempty"`
	PerfConsecutiveFailThreshold           *int64                 `json:"perf_consecutive_fail_threshold,omitempty"`
	PerfFailureRateThreshold               *float64               `json:"perf_failure_rate_threshold,omitempty"`
	PerfFailureRateMinVolume               *float64               `json:"perf_failure_rate_min_volume,omitempty"`
	PerfEjectionBaseSeconds                *int64                 `json:"perf_ejection_base_seconds,omitempty"`
	PerfEjectionMaxSeconds                 *int64                 `json:"perf_ejection_max_seconds,omitempty"`
	PerfMaxEjectionFraction                *float64               `json:"perf_max_ejection_fraction,omitempty"`
	PerfMinAvailableHosts                  *int64                 `json:"perf_min_available_hosts,omitempty"`
	PerfHostStalenessSeconds               *int64                 `json:"perf_host_staleness_seconds,omitempty"`
}

// ParseOverrides rejects unknown fields: a typo in an admin PUT must be reported, never silently ignored.
func ParseOverrides(raw []byte) (Overrides, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var overrides Overrides
	if err := decoder.Decode(&overrides); err != nil {
		return Overrides{}, fmt.Errorf("parsing overrides: %w", err)
	}
	return overrides, nil
}

// ParseStoredOverrides reads back what an earlier build wrote. See README.md, "Overrides".
func ParseStoredOverrides(raw []byte) (Overrides, error) {
	var overrides Overrides
	if err := json.Unmarshal(raw, &overrides); err != nil {
		return Overrides{}, fmt.Errorf("parsing stored overrides: %w", err)
	}
	return overrides, nil
}

func (o Overrides) EncodeOverrides() ([]byte, error) {
	encoded, err := json.Marshal(o)
	if err != nil {
		return nil, fmt.Errorf("encoding overrides: %w", err)
	}
	return encoded, nil
}
