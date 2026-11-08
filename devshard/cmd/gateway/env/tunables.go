package env

// Tunables is every setting an operator can set both in the environment and through the admin settings; nil = unset. See ../config/README.md, "Overrides".
type Tunables struct {
	DefaultMaxTokens                       *int64   `json:"default_max_tokens,omitempty"`
	MaxTokensCap                           *int64   `json:"max_tokens_cap,omitempty"`
	MaxConcurrentRequests                  *int64   `json:"max_concurrent_requests,omitempty"`
	MaxConcurrentRequestsPer10000Weight    *float64 `json:"max_concurrent_requests_per_10000_weight,omitempty"`
	PoCMaxConcurrentRequestsPer10000Weight *float64 `json:"poc_max_concurrent_requests_per_10000_weight,omitempty"`
	MaxInputTokensInFlight                 *int64   `json:"max_input_tokens_in_flight,omitempty"`
	AdmissionQueueWaitMS                   *int64   `json:"admission_queue_wait_ms,omitempty"`
	AdmissionQueuePerSlot                  *int64   `json:"admission_queue_per_slot,omitempty"`
	MatchWaitMS                            *int64   `json:"match_wait_ms,omitempty"`
	MaxConsecutiveBurns                    *int64   `json:"max_consecutive_burns,omitempty"`
	ForceUpstreamStreaming                 *bool    `json:"force_upstream_streaming,omitempty"`
	LogprobsOptimization                   *bool    `json:"logprobs_optimization,omitempty"`
	MaxBufferedResponseBytes               *int64   `json:"max_buffered_response_bytes,omitempty"`
	MinClientReadBytesPerSecond            *int64   `json:"min_client_read_bytes_per_second,omitempty"`
	WarmNewEscrows                         *bool    `json:"warm_new_escrows,omitempty"`
	Disabled                               *bool    `json:"disabled,omitempty"`
	DisabledMessage                        *string  `json:"disabled_message,omitempty"`
	DisabledRedirectURL                    *string  `json:"disabled_redirect_url,omitempty"`
	RotationEnabled                        *bool    `json:"rotation_enabled,omitempty"`
	RotationSettlementEnabled              *bool    `json:"rotation_settlement_enabled,omitempty"`
	RotationPreviousEpochSettlementEnabled *bool    `json:"rotation_previous_epoch_settlement_enabled,omitempty"`
	RotationPrePoCBlocks                   *int64   `json:"rotation_pre_poc_blocks,omitempty"`
	RotationModelsJSON                     *string  `json:"rotation_models_json,omitempty"`
	RotationSettleMarginBlocks             *int64   `json:"rotation_settle_margin_blocks,omitempty"`
	ChainSnapshotMaxAgeSeconds             *int64   `json:"chain_snapshot_max_age_seconds,omitempty"`
	EngineReceiptTimeoutMS                 *int64   `json:"engine_receipt_timeout_ms,omitempty"`
	EngineFirstTokenFloorMS                *int64   `json:"engine_first_token_floor_ms,omitempty"`
	EngineFirstTokenCeilingMS              *int64   `json:"engine_first_token_ceiling_ms,omitempty"`
	EngineInterChunkStallMS                *int64   `json:"engine_inter_chunk_stall_ms,omitempty"`
	EngineLoserGraceMS                     *int64   `json:"engine_loser_grace_ms,omitempty"`
	EngineMaxConcurrentTimeoutVotes        *int64   `json:"engine_max_concurrent_timeout_votes,omitempty"`
	EngineHedgeFirstTokenFloorMS           *int64   `json:"engine_hedge_first_token_floor_ms,omitempty"`
	PerfEWMAHalfLifeSeconds                *int64   `json:"perf_ewma_halflife_seconds,omitempty"`
	PerfConsecutiveFailThreshold           *int64   `json:"perf_consecutive_fail_threshold,omitempty"`
	PerfFailureRateThreshold               *float64 `json:"perf_failure_rate_threshold,omitempty"`
	PerfFailureRateMinVolume               *float64 `json:"perf_failure_rate_min_volume,omitempty"`
	PerfEjectionBaseSeconds                *int64   `json:"perf_ejection_base_seconds,omitempty"`
	PerfEjectionMaxSeconds                 *int64   `json:"perf_ejection_max_seconds,omitempty"`
	PerfMaxEjectionFraction                *float64 `json:"perf_max_ejection_fraction,omitempty"`
	PerfMinAvailableHosts                  *int64   `json:"perf_min_available_hosts,omitempty"`
	PerfHostStalenessSeconds               *int64   `json:"perf_host_staleness_seconds,omitempty"`
}
