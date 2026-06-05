package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type GatewaySettings struct {
	ChainREST                      string                      `json:"chain_rest"`
	PublicAPI                      string                      `json:"public_api"`
	DefaultModel                   string                      `json:"default_model"`
	DefaultRequestMaxTokens        uint64                      `json:"default_request_max_tokens"`
	RequestMaxTokensCap            uint64                      `json:"request_max_tokens_cap"`
	MaxConcurrentRequests          int64                       `json:"max_concurrent_requests"`
	MaxConcurrentPer10000Weight    float64                     `json:"max_concurrent_requests_per_10000_weight"`
	PoCMaxConcurrentPer10000Weight float64                     `json:"poc_max_concurrent_requests_per_10000_weight"`
	MaxInputTokensInFlight         int64                       `json:"max_input_tokens_in_flight"`
	ModelLimits                    []GatewayModelLimitSettings `json:"model_limits,omitempty"`
	TxGasLimit                     uint64                      `json:"tx_gas_limit,omitempty"`
	Disabled                       GatewayDisabledSettings     `json:"disabled"`
	ParticipantThrottle            ParticipantThrottleSettings `json:"participant_throttle"`
	Redundancy                     RedundancySettings          `json:"redundancy"`
	Perf                           PerfSettings                `json:"perf"`
	EscrowRotation                 EscrowRotationSettings      `json:"escrow_rotation"`
}

type GatewayModelLimitSettings struct {
	ModelID                 string `json:"model_id"`
	MaxConcurrentRequests   int64  `json:"max_concurrent_requests"`
	MaxInputTokensInFlight  int64  `json:"max_input_tokens_in_flight"`
	DefaultRequestMaxTokens uint64 `json:"default_request_max_tokens,omitempty"`
	RequestMaxTokensCap     uint64 `json:"request_max_tokens_cap,omitempty"`
	AccessMode              string `json:"access_mode,omitempty"`
	AccessMessage           string `json:"access_message,omitempty"`
}

type GatewayModelAccessSettings struct {
	ModelID string `json:"model_id"`
	Enabled bool   `json:"enabled"`
	Message string `json:"message,omitempty"`
}

type ParticipantThrottleSettings struct {
	RequestBurst                   int   `json:"request_burst"`
	RecoveryPerMinute              int   `json:"recovery_per_minute"`
	HTTPQuarantineMS               int64 `json:"http_quarantine_ms"`
	TransportFailureQuarantineMS   int64 `json:"transport_failure_quarantine_ms"`
	EmptyStreamQuarantineMS        int64 `json:"empty_stream_quarantine_ms"`
	StalledWinnerQuarantineMS      int64 `json:"stalled_winner_quarantine_ms"`
	EmptyStreamQuarantineThreshold int   `json:"empty_stream_threshold"`
	EOFTransportFailureThreshold   int   `json:"eof_transport_failure_threshold"`
}

type RedundancySettings struct {
	ReceiptTimeoutMS              int64   `json:"receipt_timeout_ms"`
	FirstTokenTimeoutFloorMS      int64   `json:"first_token_timeout_floor_ms"`
	PerInputTokenFirstTokenLagMS  int64   `json:"per_input_token_first_token_lag_ms"`
	InterChunkStallTimeoutMS      int64   `json:"inter_chunk_stall_timeout_ms"`
	StreamingAttemptHardTimeoutMS int64   `json:"streaming_attempt_hard_timeout_ms"`
	NonStreamResponseFloorMS      int64   `json:"non_stream_response_floor_ms"`
	NonStreamNoContentTimeoutMS   int64   `json:"non_stream_no_content_timeout_ms"`
	NonStreamMaxAttemptWaitMS     int64   `json:"non_stream_max_attempt_wait_ms"`
	PerInputTokenResponseLagMS    int64   `json:"per_input_token_response_lag_ms"`
	SecondaryWaitAfterWinnerMS    int64   `json:"secondary_wait_after_winner_ms"`
	ParallelAdvantageThreshold    float64 `json:"parallel_advantage_threshold"`
	UnresponsiveThreshold         float64 `json:"unresponsive_threshold"`
	SpeedPolicy                   string  `json:"speed_policy"`
	PairwiseBudgetPercentile      float64 `json:"pairwise_budget_percentile"`
	PairwiseMaxProactiveAttempts  int     `json:"pairwise_max_proactive_attempts"`
	PairwiseMinDirectComparisons  int     `json:"pairwise_min_direct_comparisons"`
	PairwiseWinnerHoldMS          int64   `json:"pairwise_winner_hold_ms"`
	PairwiseWinnerHoldMinSpeedup  float64 `json:"pairwise_winner_hold_min_speedup"`
	PairwiseWinnerHoldMinSamples  int     `json:"pairwise_winner_hold_min_samples"`
}

type PerfSettings struct {
	SampleSize int   `json:"sample_size"`
	WindowMS   int64 `json:"window_ms"`
}

type EscrowRotationSettings struct {
	Enabled           bool                          `json:"enabled"`
	SettlementEnabled bool                          `json:"settlement_enabled"`
	PrePoCBlocks      int64                         `json:"pre_poc_blocks"`
	Models            []EscrowRotationModelSettings `json:"models,omitempty"`
}

type EscrowRotationModelSettings struct {
	ModelID       string `json:"model_id"`
	TempCount     int    `json:"temp_count"`
	TargetCount   int    `json:"target_count"`
	Amount        uint64 `json:"amount"`
	PrivateKeyEnv string `json:"private_key_env"`
}

const (
	defaultMaxConcurrentPer10000Weight    = 5.0
	defaultPoCMaxConcurrentPer10000Weight = 10.0
)

func DefaultGatewaySettingsTuning() (ParticipantThrottleSettings, RedundancySettings, PerfSettings) {
	return DefaultParticipantThrottleSettings(), DefaultRedundancySettings(), PerfSettings{
		SampleSize: 256,
		WindowMS:   int64(time.Hour / time.Millisecond),
	}
}

func (s GatewaySettings) WithTuningDefaults() GatewaySettings {
	participantDefaults, redundancyDefaults, perfDefaults := DefaultGatewaySettingsTuning()
	if s.DefaultRequestMaxTokens == 0 {
		s.DefaultRequestMaxTokens = 3_072
	}
	if s.RequestMaxTokensCap == 0 {
		s.RequestMaxTokensCap = 4_096
	}
	if s.MaxConcurrentPer10000Weight == 0 {
		s.MaxConcurrentPer10000Weight = defaultMaxConcurrentPer10000Weight
	}
	if s.PoCMaxConcurrentPer10000Weight == 0 {
		s.PoCMaxConcurrentPer10000Weight = s.MaxConcurrentPer10000Weight
		if s.PoCMaxConcurrentPer10000Weight == defaultMaxConcurrentPer10000Weight {
			s.PoCMaxConcurrentPer10000Weight = defaultPoCMaxConcurrentPer10000Weight
		}
	}
	s.Disabled = s.Disabled.WithDefaults()
	if s.ParticipantThrottle == (ParticipantThrottleSettings{}) {
		s.ParticipantThrottle = participantDefaults
	}
	if s.Redundancy == (RedundancySettings{}) {
		s.Redundancy = redundancyDefaults
	}
	if s.Redundancy.StreamingAttemptHardTimeoutMS == 0 {
		s.Redundancy.StreamingAttemptHardTimeoutMS = redundancyDefaults.StreamingAttemptHardTimeoutMS
	}
	if s.Redundancy.NonStreamNoContentTimeoutMS == 0 {
		s.Redundancy.NonStreamNoContentTimeoutMS = redundancyDefaults.NonStreamNoContentTimeoutMS
	}
	if s.Redundancy.NonStreamMaxAttemptWaitMS == 0 {
		s.Redundancy.NonStreamMaxAttemptWaitMS = redundancyDefaults.NonStreamMaxAttemptWaitMS
	}
	if s.Redundancy.SpeedPolicy == "" {
		s.Redundancy.SpeedPolicy = redundancyDefaults.SpeedPolicy
	}
	if s.Redundancy.PairwiseBudgetPercentile == 0 {
		s.Redundancy.PairwiseBudgetPercentile = redundancyDefaults.PairwiseBudgetPercentile
	}
	if s.Redundancy.PairwiseMaxProactiveAttempts == 0 {
		s.Redundancy.PairwiseMaxProactiveAttempts = redundancyDefaults.PairwiseMaxProactiveAttempts
	}
	if s.Redundancy.PairwiseMinDirectComparisons == 0 {
		s.Redundancy.PairwiseMinDirectComparisons = redundancyDefaults.PairwiseMinDirectComparisons
	}
	if s.Redundancy.PairwiseWinnerHoldMS == 0 {
		s.Redundancy.PairwiseWinnerHoldMS = redundancyDefaults.PairwiseWinnerHoldMS
	}
	if s.Redundancy.PairwiseWinnerHoldMinSpeedup == 0 {
		s.Redundancy.PairwiseWinnerHoldMinSpeedup = redundancyDefaults.PairwiseWinnerHoldMinSpeedup
	}
	if s.Redundancy.PairwiseWinnerHoldMinSamples == 0 {
		s.Redundancy.PairwiseWinnerHoldMinSamples = redundancyDefaults.PairwiseWinnerHoldMinSamples
	}
	if s.Perf == (PerfSettings{}) {
		s.Perf = perfDefaults
	}
	if s.EscrowRotation.PrePoCBlocks == 0 {
		s.EscrowRotation.PrePoCBlocks = 300
	}
	for i := range s.EscrowRotation.Models {
		model := &s.EscrowRotation.Models[i]
		model.ModelID = strings.TrimSpace(model.ModelID)
		model.PrivateKeyEnv = strings.TrimSpace(model.PrivateKeyEnv)
	}
	s.ModelLimits = normalizeGatewayModelLimits(s.ModelLimits)
	return s
}

func normalizeGatewayModelLimits(limits []GatewayModelLimitSettings) []GatewayModelLimitSettings {
	if len(limits) == 0 {
		return nil
	}
	normalized := make([]GatewayModelLimitSettings, 0, len(limits))
	seen := make(map[string]int, len(limits))
	for _, limit := range limits {
		limit.ModelID = strings.TrimSpace(limit.ModelID)
		limit.AccessMode = normalizeGatewayAccessMode(limit.AccessMode)
		limit.AccessMessage = strings.TrimSpace(limit.AccessMessage)
		if limit.ModelID == "" {
			continue
		}
		if idx, ok := seen[limit.ModelID]; ok {
			normalized[idx] = limit
			continue
		}
		seen[limit.ModelID] = len(normalized)
		normalized = append(normalized, limit)
	}
	return normalized
}

func normalizeGatewayAccessMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	mode = strings.ReplaceAll(mode, "-", "_")
	mode = strings.ReplaceAll(mode, " ", "_")
	switch mode {
	case "":
		return ""
	case "open", "public", "none":
		return string(gatewayAccessModeOpen)
	case "api_key", "apikey", "api_keys", "api", "key":
		return string(gatewayAccessModeAPIKey)
	case "admin", "admin_only", "admin_key":
		return string(gatewayAccessModeAdminOnly)
	default:
		return mode
	}
}

func gatewayModelAccessModeLabel(mode string) string {
	mode = normalizeGatewayAccessMode(mode)
	if mode == "" {
		return string(gatewayAccessModeAdminOnly)
	}
	return mode
}

func applyLegacyModelAccessToLimits(limits []GatewayModelLimitSettings, access []GatewayModelAccessSettings) []GatewayModelLimitSettings {
	if len(access) == 0 {
		return limits
	}
	limits = normalizeGatewayModelLimits(limits)
	byModel := make(map[string]int, len(limits))
	for i, limit := range limits {
		byModel[limit.ModelID] = i
	}
	for _, entry := range normalizeGatewayModelAccess(access) {
		mode := string(gatewayAccessModeOpen)
		if !entry.Enabled {
			mode = string(gatewayAccessModeAdminOnly)
		}
		if idx, ok := byModel[entry.ModelID]; ok {
			if limits[idx].AccessMode == "" {
				limits[idx].AccessMode = mode
			}
			if limits[idx].AccessMessage == "" {
				limits[idx].AccessMessage = entry.Message
			}
			continue
		}
		byModel[entry.ModelID] = len(limits)
		limits = append(limits, GatewayModelLimitSettings{
			ModelID:       entry.ModelID,
			AccessMode:    mode,
			AccessMessage: entry.Message,
		})
	}
	return normalizeGatewayModelLimits(limits)
}

func normalizeGatewayModelAccess(access []GatewayModelAccessSettings) []GatewayModelAccessSettings {
	if len(access) == 0 {
		return nil
	}
	normalized := make([]GatewayModelAccessSettings, 0, len(access))
	seen := make(map[string]int, len(access))
	for _, entry := range access {
		entry.ModelID = strings.TrimSpace(entry.ModelID)
		entry.Message = strings.TrimSpace(entry.Message)
		if entry.ModelID == "" {
			continue
		}
		if idx, ok := seen[entry.ModelID]; ok {
			normalized[idx] = entry
			continue
		}
		seen[entry.ModelID] = len(normalized)
		normalized = append(normalized, entry)
	}
	return normalized
}

type GatewayDevshardState struct {
	RuntimeConfig
	Active        bool   `json:"active"`
	RotationRole  string `json:"rotation_role,omitempty"`
	RotationEpoch uint64 `json:"rotation_epoch,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

type GatewayState struct {
	Settings  GatewaySettings        `json:"settings"`
	Devshards []GatewayDevshardState `json:"devshards"`
}

type GatewayStore struct {
	db *sql.DB
}

func NewGatewayStore(path string) (*GatewayStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open gateway store: %w", err)
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS gateway_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			chain_rest TEXT NOT NULL,
			public_api TEXT NOT NULL DEFAULT '',
			default_model TEXT NOT NULL,
			default_request_max_tokens INTEGER NOT NULL,
			request_max_tokens_cap INTEGER NOT NULL DEFAULT 4096,
			max_concurrent_requests INTEGER NOT NULL DEFAULT 512,
			max_concurrent_requests_per_10000_weight REAL NOT NULL DEFAULT 5.0,
			poc_max_concurrent_requests_per_10000_weight REAL NOT NULL DEFAULT 10.0,
			max_input_tokens_in_flight INTEGER NOT NULL,
			model_limits_json TEXT NOT NULL DEFAULT '',
			model_access_json TEXT NOT NULL DEFAULT '',
			tx_gas_limit INTEGER NOT NULL DEFAULT 0,
			participant_request_burst INTEGER NOT NULL DEFAULT 600,
			participant_recovery_per_minute INTEGER NOT NULL DEFAULT 10,
			participant_http_quarantine_ms INTEGER NOT NULL DEFAULT 3600000,
			participant_transport_failure_quarantine_ms INTEGER NOT NULL DEFAULT 1800000,
			participant_empty_stream_quarantine_ms INTEGER NOT NULL DEFAULT 1800000,
			participant_stalled_winner_quarantine_ms INTEGER NOT NULL DEFAULT 1800000,
			participant_empty_stream_threshold INTEGER NOT NULL DEFAULT 3,
			participant_eof_transport_failure_threshold INTEGER NOT NULL DEFAULT 3,
			redundancy_receipt_timeout_ms INTEGER NOT NULL DEFAULT 5000,
			redundancy_first_token_timeout_floor_ms INTEGER NOT NULL DEFAULT 1000,
			redundancy_per_input_token_first_token_lag_ms INTEGER NOT NULL DEFAULT 10,
			redundancy_inter_chunk_stall_timeout_ms INTEGER NOT NULL DEFAULT 60000,
			redundancy_streaming_attempt_hard_timeout_ms INTEGER NOT NULL DEFAULT 1200000,
			redundancy_non_stream_response_floor_ms INTEGER NOT NULL DEFAULT 20000,
			redundancy_non_stream_no_content_timeout_ms INTEGER NOT NULL DEFAULT 1200000,
			redundancy_non_stream_max_attempt_wait_ms INTEGER NOT NULL DEFAULT 1800000,
			redundancy_per_input_token_response_lag_ms INTEGER NOT NULL DEFAULT 20,
			redundancy_secondary_wait_after_winner_ms INTEGER NOT NULL DEFAULT 600000,
			redundancy_parallel_advantage_threshold REAL NOT NULL DEFAULT 0.5,
			redundancy_unresponsive_threshold REAL NOT NULL DEFAULT 1.0,
			redundancy_speed_policy TEXT NOT NULL DEFAULT 'hybrid',
			redundancy_pairwise_budget_percentile REAL NOT NULL DEFAULT 0.9,
			redundancy_pairwise_max_proactive_attempts INTEGER NOT NULL DEFAULT 3,
			redundancy_pairwise_min_direct_comparisons INTEGER NOT NULL DEFAULT 4,
			redundancy_pairwise_winner_hold_ms INTEGER NOT NULL DEFAULT 500,
			redundancy_pairwise_winner_hold_min_speedup REAL NOT NULL DEFAULT 0.1,
			redundancy_pairwise_winner_hold_min_samples INTEGER NOT NULL DEFAULT 6,
			perf_sample_size INTEGER NOT NULL DEFAULT 256,
			perf_window_ms INTEGER NOT NULL DEFAULT 3600000,
			escrow_rotation_enabled INTEGER NOT NULL DEFAULT 0,
			escrow_rotation_settlement_enabled INTEGER NOT NULL DEFAULT 0,
			escrow_rotation_pre_poc_blocks INTEGER NOT NULL DEFAULT 300,
			escrow_rotation_models_json TEXT NOT NULL DEFAULT '',
			gateway_disabled_enabled INTEGER NOT NULL DEFAULT 0,
			gateway_disabled_message TEXT NOT NULL DEFAULT '',
			gateway_disabled_new_url TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS gateway_devshards (
			id TEXT PRIMARY KEY,
			private_key_hex TEXT NOT NULL,
			private_key_env TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			storage_path TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1,
			rotation_role TEXT NOT NULL DEFAULT '',
			rotation_epoch INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("init gateway store: %w", err)
		}
	}
	if err := ensureGatewaySettingsColumn(db, "public_api", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway store: %w", err)
	}
	if err := ensureGatewaySettingsColumn(db, "tx_gas_limit", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway tx settings: %w", err)
	}
	if err := ensureGatewaySettingsColumn(db, "model_limits_json", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway model limits: %w", err)
	}
	if err := ensureGatewaySettingsColumn(db, "model_access_json", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway model access: %w", err)
	}
	if err := ensureGatewaySettingsColumn(db, "request_max_tokens_cap", "INTEGER NOT NULL DEFAULT 4096"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway max token cap: %w", err)
	}
	if err := ensureGatewaySettingsColumn(db, "max_concurrent_requests_per_10000_weight", "REAL NOT NULL DEFAULT 5.0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway weight concurrency: %w", err)
	}
	if err := ensureGatewaySettingsColumn(db, "poc_max_concurrent_requests_per_10000_weight", "REAL NOT NULL DEFAULT 10.0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway poc weight concurrency: %w", err)
	}
	if err := ensureGatewaySettingsTuningColumns(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway tuning settings: %w", err)
	}
	if err := ensureGatewaySettingsRotationColumns(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway rotation settings: %w", err)
	}
	if err := ensureGatewaySettingsDisabledColumns(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway disabled settings: %w", err)
	}
	if err := ensureGatewayDevshardsColumn(db, "protocol_version", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway devshards: %w", err)
	}
	if err := ensureGatewayDevshardsColumn(db, "rotation_role", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway devshard role: %w", err)
	}
	if err := ensureGatewayDevshardsColumn(db, "rotation_epoch", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway devshard epoch: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS participant_throttle_state (
		participant_key TEXT PRIMARY KEY,
		tokens REAL NOT NULL DEFAULT 0,
		last_refill_at TEXT NOT NULL,
		last_throttle_status INTEGER NOT NULL DEFAULT 0,
		empty_stream_streak INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("init participant throttle table: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS gateway_rotation_status (
		model_id TEXT NOT NULL,
		stage TEXT NOT NULL,
		epoch INTEGER NOT NULL,
		role TEXT NOT NULL DEFAULT '',
		target_count INTEGER NOT NULL DEFAULT 0,
		existing_count INTEGER NOT NULL DEFAULT 0,
		created_count INTEGER NOT NULL DEFAULT 0,
		promoted_count INTEGER NOT NULL DEFAULT 0,
		settled_count INTEGER NOT NULL DEFAULT 0,
		settle_failed_count INTEGER NOT NULL DEFAULT 0,
		create_error TEXT NOT NULL DEFAULT '',
		completed INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (model_id, stage, epoch)
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("init gateway rotation status table: %w", err)
	}
	if err := ensureColumn(db, "participant_throttle_state", "quarantine_until_utc", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate participant throttle: %w", err)
	}
	if err := ensureColumn(db, "participant_throttle_state", "empty_stream_streak", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate participant throttle streak: %w", err)
	}
	if err := ensureColumn(db, "participant_throttle_state", "eof_transport_failure_streak", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate participant throttle eof streak: %w", err)
	}

	return &GatewayStore{db: db}, nil
}

func (s *GatewayStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *GatewayStore) LoadState() (GatewayState, bool, error) {
	var state GatewayState
	row := s.db.QueryRow(`
		SELECT chain_rest, public_api, default_model, default_request_max_tokens, request_max_tokens_cap,
		       max_concurrent_requests, max_concurrent_requests_per_10000_weight,
		       poc_max_concurrent_requests_per_10000_weight, max_input_tokens_in_flight,
		       model_limits_json, model_access_json, tx_gas_limit,
		       participant_request_burst, participant_recovery_per_minute,
		       participant_http_quarantine_ms, participant_transport_failure_quarantine_ms,
		       participant_empty_stream_quarantine_ms, participant_stalled_winner_quarantine_ms,
		       participant_empty_stream_threshold, participant_eof_transport_failure_threshold,
		       redundancy_receipt_timeout_ms, redundancy_first_token_timeout_floor_ms,
		       redundancy_per_input_token_first_token_lag_ms, redundancy_inter_chunk_stall_timeout_ms,
		       redundancy_streaming_attempt_hard_timeout_ms,
		       redundancy_non_stream_response_floor_ms, redundancy_non_stream_no_content_timeout_ms,
		       redundancy_non_stream_max_attempt_wait_ms, redundancy_per_input_token_response_lag_ms,
		       redundancy_secondary_wait_after_winner_ms, redundancy_parallel_advantage_threshold,
		       redundancy_unresponsive_threshold, redundancy_speed_policy, redundancy_pairwise_budget_percentile,
		       redundancy_pairwise_max_proactive_attempts, redundancy_pairwise_min_direct_comparisons,
		       redundancy_pairwise_winner_hold_ms, redundancy_pairwise_winner_hold_min_speedup,
		       redundancy_pairwise_winner_hold_min_samples,
		       perf_sample_size, perf_window_ms,
		       escrow_rotation_enabled, escrow_rotation_settlement_enabled,
		       escrow_rotation_pre_poc_blocks, escrow_rotation_models_json,
	       gateway_disabled_enabled, gateway_disabled_message, gateway_disabled_new_url
		FROM gateway_settings
		WHERE id = 1`)
	var rotationEnabled int
	var rotationSettlementEnabled int
	var disabledEnabled int
	var rotationModelsJSON string
	var modelLimitsJSON string
	var modelAccessJSON string
	err := row.Scan(
		&state.Settings.ChainREST,
		&state.Settings.PublicAPI,
		&state.Settings.DefaultModel,
		&state.Settings.DefaultRequestMaxTokens,
		&state.Settings.RequestMaxTokensCap,
		&state.Settings.MaxConcurrentRequests,
		&state.Settings.MaxConcurrentPer10000Weight,
		&state.Settings.PoCMaxConcurrentPer10000Weight,
		&state.Settings.MaxInputTokensInFlight,
		&modelLimitsJSON,
		&modelAccessJSON,
		&state.Settings.TxGasLimit,
		&state.Settings.ParticipantThrottle.RequestBurst,
		&state.Settings.ParticipantThrottle.RecoveryPerMinute,
		&state.Settings.ParticipantThrottle.HTTPQuarantineMS,
		&state.Settings.ParticipantThrottle.TransportFailureQuarantineMS,
		&state.Settings.ParticipantThrottle.EmptyStreamQuarantineMS,
		&state.Settings.ParticipantThrottle.StalledWinnerQuarantineMS,
		&state.Settings.ParticipantThrottle.EmptyStreamQuarantineThreshold,
		&state.Settings.ParticipantThrottle.EOFTransportFailureThreshold,
		&state.Settings.Redundancy.ReceiptTimeoutMS,
		&state.Settings.Redundancy.FirstTokenTimeoutFloorMS,
		&state.Settings.Redundancy.PerInputTokenFirstTokenLagMS,
		&state.Settings.Redundancy.InterChunkStallTimeoutMS,
		&state.Settings.Redundancy.StreamingAttemptHardTimeoutMS,
		&state.Settings.Redundancy.NonStreamResponseFloorMS,
		&state.Settings.Redundancy.NonStreamNoContentTimeoutMS,
		&state.Settings.Redundancy.NonStreamMaxAttemptWaitMS,
		&state.Settings.Redundancy.PerInputTokenResponseLagMS,
		&state.Settings.Redundancy.SecondaryWaitAfterWinnerMS,
		&state.Settings.Redundancy.ParallelAdvantageThreshold,
		&state.Settings.Redundancy.UnresponsiveThreshold,
		&state.Settings.Redundancy.SpeedPolicy,
		&state.Settings.Redundancy.PairwiseBudgetPercentile,
		&state.Settings.Redundancy.PairwiseMaxProactiveAttempts,
		&state.Settings.Redundancy.PairwiseMinDirectComparisons,
		&state.Settings.Redundancy.PairwiseWinnerHoldMS,
		&state.Settings.Redundancy.PairwiseWinnerHoldMinSpeedup,
		&state.Settings.Redundancy.PairwiseWinnerHoldMinSamples,
		&state.Settings.Perf.SampleSize,
		&state.Settings.Perf.WindowMS,
		&rotationEnabled,
		&rotationSettlementEnabled,
		&state.Settings.EscrowRotation.PrePoCBlocks,
		&rotationModelsJSON,
		&disabledEnabled,
		&state.Settings.Disabled.Message,
		&state.Settings.Disabled.NewURL,
	)
	if err == sql.ErrNoRows {
		return GatewayState{}, false, nil
	}
	if err != nil {
		return GatewayState{}, false, fmt.Errorf("load gateway settings: %w", err)
	}
	state.Settings.EscrowRotation.Enabled = rotationEnabled != 0
	state.Settings.EscrowRotation.SettlementEnabled = rotationSettlementEnabled != 0
	if strings.TrimSpace(rotationModelsJSON) != "" {
		if err := json.Unmarshal([]byte(rotationModelsJSON), &state.Settings.EscrowRotation.Models); err != nil {
			return GatewayState{}, false, fmt.Errorf("load gateway rotation models: %w", err)
		}
	}
	if strings.TrimSpace(modelLimitsJSON) != "" {
		if err := json.Unmarshal([]byte(modelLimitsJSON), &state.Settings.ModelLimits); err != nil {
			return GatewayState{}, false, fmt.Errorf("load gateway model limits: %w", err)
		}
	}
	if strings.TrimSpace(modelAccessJSON) != "" {
		var legacyModelAccess []GatewayModelAccessSettings
		if err := json.Unmarshal([]byte(modelAccessJSON), &legacyModelAccess); err != nil {
			return GatewayState{}, false, fmt.Errorf("load gateway model access: %w", err)
		}
		state.Settings.ModelLimits = applyLegacyModelAccessToLimits(state.Settings.ModelLimits, legacyModelAccess)
	}
	state.Settings.Disabled.Enabled = disabledEnabled != 0
	state.Settings = state.Settings.WithTuningDefaults()

	rows, err := s.db.Query(`
		SELECT id, private_key_hex, private_key_env, model, storage_path, active, created_at, updated_at, protocol_version,
		       rotation_role, rotation_epoch
		FROM gateway_devshards
		ORDER BY id`)
	if err != nil {
		return GatewayState{}, false, fmt.Errorf("load gateway devshards: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var devshard GatewayDevshardState
		var active int
		if err := rows.Scan(
			&devshard.ID,
			&devshard.PrivateKeyHex,
			&devshard.PrivateKeyEnv,
			&devshard.Model,
			&devshard.StoragePath,
			&active,
			&devshard.CreatedAt,
			&devshard.UpdatedAt,
			&devshard.ProtocolVersion,
			&devshard.RotationRole,
			&devshard.RotationEpoch,
		); err != nil {
			return GatewayState{}, false, fmt.Errorf("scan gateway devshard: %w", err)
		}
		devshard.Active = active != 0
		state.Devshards = append(state.Devshards, devshard)
	}
	if err := rows.Err(); err != nil {
		return GatewayState{}, false, fmt.Errorf("iterate gateway devshards: %w", err)
	}
	return state, true, nil
}

func (s *GatewayStore) Initialize(settings GatewaySettings, devshards []GatewayDevshardState) error {
	settings = settings.WithTuningDefaults()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin gateway init: %w", err)
	}
	defer tx.Rollback()

	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM gateway_settings WHERE id = 1`).Scan(&count); err != nil {
		return fmt.Errorf("count gateway settings: %w", err)
	}
	if count > 0 {
		return nil
	}

	if _, err := tx.Exec(`
		INSERT INTO gateway_settings (
			id, chain_rest, public_api, default_model, default_request_max_tokens, request_max_tokens_cap,
			max_concurrent_requests, max_concurrent_requests_per_10000_weight,
			poc_max_concurrent_requests_per_10000_weight, max_input_tokens_in_flight,
			model_limits_json, model_access_json, tx_gas_limit,
			participant_request_burst, participant_recovery_per_minute,
			participant_http_quarantine_ms, participant_transport_failure_quarantine_ms,
			participant_empty_stream_quarantine_ms, participant_stalled_winner_quarantine_ms,
			participant_empty_stream_threshold, participant_eof_transport_failure_threshold,
			redundancy_receipt_timeout_ms, redundancy_first_token_timeout_floor_ms,
			redundancy_per_input_token_first_token_lag_ms, redundancy_inter_chunk_stall_timeout_ms,
			redundancy_streaming_attempt_hard_timeout_ms,
			redundancy_non_stream_response_floor_ms, redundancy_non_stream_no_content_timeout_ms,
			redundancy_non_stream_max_attempt_wait_ms, redundancy_per_input_token_response_lag_ms,
			redundancy_secondary_wait_after_winner_ms, redundancy_parallel_advantage_threshold,
			redundancy_unresponsive_threshold, redundancy_speed_policy, redundancy_pairwise_budget_percentile,
			redundancy_pairwise_max_proactive_attempts, redundancy_pairwise_min_direct_comparisons,
			redundancy_pairwise_winner_hold_ms, redundancy_pairwise_winner_hold_min_speedup,
			redundancy_pairwise_winner_hold_min_samples,
			perf_sample_size, perf_window_ms,
			escrow_rotation_enabled, escrow_rotation_settlement_enabled,
			escrow_rotation_pre_poc_blocks, escrow_rotation_models_json,
			gateway_disabled_enabled, gateway_disabled_message, gateway_disabled_new_url,
			updated_at
		) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(settings.ChainREST),
		strings.TrimSpace(settings.PublicAPI),
		strings.TrimSpace(settings.DefaultModel),
		settings.DefaultRequestMaxTokens,
		settings.RequestMaxTokensCap,
		settings.MaxConcurrentRequests,
		settings.MaxConcurrentPer10000Weight,
		settings.PoCMaxConcurrentPer10000Weight,
		settings.MaxInputTokensInFlight,
		mustMarshalGatewayModelLimits(settings.ModelLimits),
		"",
		settings.TxGasLimit,
		settings.ParticipantThrottle.RequestBurst,
		settings.ParticipantThrottle.RecoveryPerMinute,
		settings.ParticipantThrottle.HTTPQuarantineMS,
		settings.ParticipantThrottle.TransportFailureQuarantineMS,
		settings.ParticipantThrottle.EmptyStreamQuarantineMS,
		settings.ParticipantThrottle.StalledWinnerQuarantineMS,
		settings.ParticipantThrottle.EmptyStreamQuarantineThreshold,
		settings.ParticipantThrottle.EOFTransportFailureThreshold,
		settings.Redundancy.ReceiptTimeoutMS,
		settings.Redundancy.FirstTokenTimeoutFloorMS,
		settings.Redundancy.PerInputTokenFirstTokenLagMS,
		settings.Redundancy.InterChunkStallTimeoutMS,
		settings.Redundancy.StreamingAttemptHardTimeoutMS,
		settings.Redundancy.NonStreamResponseFloorMS,
		settings.Redundancy.NonStreamNoContentTimeoutMS,
		settings.Redundancy.NonStreamMaxAttemptWaitMS,
		settings.Redundancy.PerInputTokenResponseLagMS,
		settings.Redundancy.SecondaryWaitAfterWinnerMS,
		settings.Redundancy.ParallelAdvantageThreshold,
		settings.Redundancy.UnresponsiveThreshold,
		settings.Redundancy.SpeedPolicy,
		settings.Redundancy.PairwiseBudgetPercentile,
		settings.Redundancy.PairwiseMaxProactiveAttempts,
		settings.Redundancy.PairwiseMinDirectComparisons,
		settings.Redundancy.PairwiseWinnerHoldMS,
		settings.Redundancy.PairwiseWinnerHoldMinSpeedup,
		settings.Redundancy.PairwiseWinnerHoldMinSamples,
		settings.Perf.SampleSize,
		settings.Perf.WindowMS,
		gatewayBoolToInt(settings.EscrowRotation.Enabled),
		gatewayBoolToInt(settings.EscrowRotation.SettlementEnabled),
		settings.EscrowRotation.PrePoCBlocks,
		mustMarshalEscrowRotationModels(settings.EscrowRotation.Models),
		gatewayBoolToInt(settings.Disabled.Enabled),
		strings.TrimSpace(settings.Disabled.Message),
		strings.TrimSpace(settings.Disabled.NewURL),
		now,
	); err != nil {
		return fmt.Errorf("insert gateway settings: %w", err)
	}

	for _, devshard := range devshards {
		if err := s.upsertDevshardTx(tx, devshard, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *GatewayStore) UpdateSettings(settings GatewaySettings) error {
	settings = settings.WithTuningDefaults()
	res, err := s.db.Exec(`
		UPDATE gateway_settings
		SET chain_rest = ?,
		    public_api = ?,
		    default_model = ?,
		    default_request_max_tokens = ?,
		    request_max_tokens_cap = ?,
		    max_concurrent_requests = ?,
		    max_concurrent_requests_per_10000_weight = ?,
		    poc_max_concurrent_requests_per_10000_weight = ?,
		    max_input_tokens_in_flight = ?,
		    model_limits_json = ?,
		    model_access_json = ?,
		    tx_gas_limit = ?,
		    participant_request_burst = ?,
		    participant_recovery_per_minute = ?,
		    participant_http_quarantine_ms = ?,
		    participant_transport_failure_quarantine_ms = ?,
		    participant_empty_stream_quarantine_ms = ?,
		    participant_stalled_winner_quarantine_ms = ?,
		    participant_empty_stream_threshold = ?,
		    participant_eof_transport_failure_threshold = ?,
		    redundancy_receipt_timeout_ms = ?,
		    redundancy_first_token_timeout_floor_ms = ?,
		    redundancy_per_input_token_first_token_lag_ms = ?,
		    redundancy_inter_chunk_stall_timeout_ms = ?,
		    redundancy_streaming_attempt_hard_timeout_ms = ?,
		    redundancy_non_stream_response_floor_ms = ?,
		    redundancy_non_stream_no_content_timeout_ms = ?,
		    redundancy_non_stream_max_attempt_wait_ms = ?,
		    redundancy_per_input_token_response_lag_ms = ?,
		    redundancy_secondary_wait_after_winner_ms = ?,
		    redundancy_parallel_advantage_threshold = ?,
		    redundancy_unresponsive_threshold = ?,
		    redundancy_speed_policy = ?,
		    redundancy_pairwise_budget_percentile = ?,
		    redundancy_pairwise_max_proactive_attempts = ?,
		    redundancy_pairwise_min_direct_comparisons = ?,
		    redundancy_pairwise_winner_hold_ms = ?,
		    redundancy_pairwise_winner_hold_min_speedup = ?,
		    redundancy_pairwise_winner_hold_min_samples = ?,
		    perf_sample_size = ?,
		    perf_window_ms = ?,
		    escrow_rotation_enabled = ?,
		    escrow_rotation_settlement_enabled = ?,
		    escrow_rotation_pre_poc_blocks = ?,
		    escrow_rotation_models_json = ?,
		    gateway_disabled_enabled = ?,
		    gateway_disabled_message = ?,
		    gateway_disabled_new_url = ?,
		    updated_at = ?
		WHERE id = 1`,
		strings.TrimSpace(settings.ChainREST),
		strings.TrimSpace(settings.PublicAPI),
		strings.TrimSpace(settings.DefaultModel),
		settings.DefaultRequestMaxTokens,
		settings.RequestMaxTokensCap,
		settings.MaxConcurrentRequests,
		settings.MaxConcurrentPer10000Weight,
		settings.PoCMaxConcurrentPer10000Weight,
		settings.MaxInputTokensInFlight,
		mustMarshalGatewayModelLimits(settings.ModelLimits),
		"",
		settings.TxGasLimit,
		settings.ParticipantThrottle.RequestBurst,
		settings.ParticipantThrottle.RecoveryPerMinute,
		settings.ParticipantThrottle.HTTPQuarantineMS,
		settings.ParticipantThrottle.TransportFailureQuarantineMS,
		settings.ParticipantThrottle.EmptyStreamQuarantineMS,
		settings.ParticipantThrottle.StalledWinnerQuarantineMS,
		settings.ParticipantThrottle.EmptyStreamQuarantineThreshold,
		settings.ParticipantThrottle.EOFTransportFailureThreshold,
		settings.Redundancy.ReceiptTimeoutMS,
		settings.Redundancy.FirstTokenTimeoutFloorMS,
		settings.Redundancy.PerInputTokenFirstTokenLagMS,
		settings.Redundancy.InterChunkStallTimeoutMS,
		settings.Redundancy.StreamingAttemptHardTimeoutMS,
		settings.Redundancy.NonStreamResponseFloorMS,
		settings.Redundancy.NonStreamNoContentTimeoutMS,
		settings.Redundancy.NonStreamMaxAttemptWaitMS,
		settings.Redundancy.PerInputTokenResponseLagMS,
		settings.Redundancy.SecondaryWaitAfterWinnerMS,
		settings.Redundancy.ParallelAdvantageThreshold,
		settings.Redundancy.UnresponsiveThreshold,
		settings.Redundancy.SpeedPolicy,
		settings.Redundancy.PairwiseBudgetPercentile,
		settings.Redundancy.PairwiseMaxProactiveAttempts,
		settings.Redundancy.PairwiseMinDirectComparisons,
		settings.Redundancy.PairwiseWinnerHoldMS,
		settings.Redundancy.PairwiseWinnerHoldMinSpeedup,
		settings.Redundancy.PairwiseWinnerHoldMinSamples,
		settings.Perf.SampleSize,
		settings.Perf.WindowMS,
		gatewayBoolToInt(settings.EscrowRotation.Enabled),
		gatewayBoolToInt(settings.EscrowRotation.SettlementEnabled),
		settings.EscrowRotation.PrePoCBlocks,
		mustMarshalEscrowRotationModels(settings.EscrowRotation.Models),
		gatewayBoolToInt(settings.Disabled.Enabled),
		strings.TrimSpace(settings.Disabled.Message),
		strings.TrimSpace(settings.Disabled.NewURL),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("update gateway settings: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for gateway settings update: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("gateway settings not initialized")
	}
	return nil
}

type GatewayRotationStatus struct {
	ModelID           string `json:"model_id"`
	Stage             string `json:"stage"`
	Epoch             uint64 `json:"epoch"`
	Role              string `json:"role,omitempty"`
	TargetCount       int    `json:"target_count"`
	ExistingCount     int    `json:"existing_count"`
	CreatedCount      int    `json:"created_count"`
	PromotedCount     int    `json:"promoted_count"`
	SettledCount      int    `json:"settled_count"`
	SettleFailedCount int    `json:"settle_failed_count"`
	CreateError       string `json:"create_error,omitempty"`
	Completed         bool   `json:"completed"`
	UpdatedAt         string `json:"updated_at"`
}

func (s *GatewayStore) SaveRotationStatus(status GatewayRotationStatus) error {
	if s == nil || s.db == nil {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if strings.TrimSpace(status.UpdatedAt) != "" {
		now = strings.TrimSpace(status.UpdatedAt)
	}
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO gateway_rotation_status (
			model_id, stage, epoch, role, target_count, existing_count, created_count,
			promoted_count, settled_count, settle_failed_count, create_error, completed, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(status.ModelID),
		strings.TrimSpace(status.Stage),
		status.Epoch,
		strings.TrimSpace(status.Role),
		status.TargetCount,
		status.ExistingCount,
		status.CreatedCount,
		status.PromotedCount,
		status.SettledCount,
		status.SettleFailedCount,
		strings.TrimSpace(status.CreateError),
		gatewayBoolToInt(status.Completed),
		now,
	)
	if err != nil {
		return fmt.Errorf("save gateway rotation status model=%q stage=%q epoch=%d: %w", status.ModelID, status.Stage, status.Epoch, err)
	}
	return nil
}

func (s *GatewayStore) LoadRotationStatuses(limit int) ([]GatewayRotationStatus, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	query := `
		SELECT model_id, stage, epoch, role, target_count, existing_count, created_count,
		       promoted_count, settled_count, settle_failed_count, create_error, completed, updated_at
		FROM gateway_rotation_status
		ORDER BY updated_at DESC`
	args := []any{}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("load gateway rotation statuses: %w", err)
	}
	defer rows.Close()
	var statuses []GatewayRotationStatus
	for rows.Next() {
		var status GatewayRotationStatus
		var completed int
		if err := rows.Scan(
			&status.ModelID,
			&status.Stage,
			&status.Epoch,
			&status.Role,
			&status.TargetCount,
			&status.ExistingCount,
			&status.CreatedCount,
			&status.PromotedCount,
			&status.SettledCount,
			&status.SettleFailedCount,
			&status.CreateError,
			&completed,
			&status.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan gateway rotation status: %w", err)
		}
		status.Completed = completed != 0
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return statuses, nil
}

func (s *GatewayStore) UpsertDevshard(devshard GatewayDevshardState) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin devshard upsert: %w", err)
	}
	defer tx.Rollback()
	if err := s.upsertDevshardTx(tx, devshard, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *GatewayStore) upsertDevshardTx(tx *sql.Tx, devshard GatewayDevshardState, now string) error {
	createdAt := now
	_ = tx.QueryRow(`SELECT created_at FROM gateway_devshards WHERE id = ?`, devshard.ID).Scan(&createdAt)
	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO gateway_devshards (
			id, private_key_hex, private_key_env, model, storage_path, active, created_at, updated_at, protocol_version,
			rotation_role, rotation_epoch
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(devshard.ID),
		strings.TrimSpace(devshard.PrivateKeyHex),
		strings.TrimSpace(devshard.PrivateKeyEnv),
		strings.TrimSpace(devshard.Model),
		strings.TrimSpace(devshard.StoragePath),
		gatewayBoolToInt(devshard.Active),
		createdAt,
		now,
		strings.TrimSpace(devshard.ProtocolVersion),
		strings.TrimSpace(devshard.RotationRole),
		devshard.RotationEpoch,
	); err != nil {
		return fmt.Errorf("upsert gateway devshard %s: %w", devshard.ID, err)
	}
	return nil
}

func (s *GatewayStore) SetDevshardActive(id string, active bool) error {
	res, err := s.db.Exec(`
		UPDATE gateway_devshards
		SET active = ?, updated_at = ?
		WHERE id = ?`,
		gatewayBoolToInt(active),
		time.Now().UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(id),
	)
	if err != nil {
		return fmt.Errorf("update devshard %s active=%t: %w", id, active, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for devshard %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("devshard %s not found", id)
	}
	return nil
}

func (s *GatewayStore) DeleteDevshard(id string) error {
	res, err := s.db.Exec(`DELETE FROM gateway_devshards WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("delete devshard %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for delete devshard %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("devshard %s not found", id)
	}
	return nil
}

// ParticipantThrottleRow represents a persisted reactive throttle state for one host.
type ParticipantThrottleRow struct {
	Key                       string
	Tokens                    float64
	LastRefillAt              time.Time
	Status                    int
	QuarantineUntil           time.Time // wall-clock end of unified quarantine; zero if unset
	EmptyStreamStreak         int
	EOFTransportFailureStreak int
}

func (s *GatewayStore) SaveParticipantThrottle(key string, tokens float64, lastRefillAt time.Time, status int, quarantineUntil time.Time, emptyStreamStreak int, eofTransportFailureStreak int) error {
	if s == nil || s.db == nil {
		return nil
	}
	quarStr := ""
	if !quarantineUntil.IsZero() {
		quarStr = quarantineUntil.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO participant_throttle_state
			(participant_key, tokens, last_refill_at, last_throttle_status, quarantine_until_utc, empty_stream_streak, eof_transport_failure_streak)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		key, tokens, lastRefillAt.UTC().Format(time.RFC3339Nano), status, quarStr, emptyStreamStreak, eofTransportFailureStreak)
	if err != nil {
		return fmt.Errorf("save participant throttle %s: %w", key, err)
	}
	return nil
}

func (s *GatewayStore) DeleteParticipantThrottle(key string) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM participant_throttle_state WHERE participant_key = ?`, key)
	if err != nil {
		return fmt.Errorf("delete participant throttle %s: %w", key, err)
	}
	return nil
}

func (s *GatewayStore) LoadParticipantThrottles() ([]ParticipantThrottleRow, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT participant_key, tokens, last_refill_at, last_throttle_status,
		       IFNULL(empty_stream_streak, 0) AS empty_stream_streak,
		       IFNULL(eof_transport_failure_streak, 0) AS eof_transport_failure_streak,
		       IFNULL(quarantine_until_utc, '') AS quarantine_until_utc
		FROM participant_throttle_state`)
	if err != nil {
		return nil, fmt.Errorf("load participant throttles: %w", err)
	}
	defer rows.Close()

	var result []ParticipantThrottleRow
	for rows.Next() {
		var row ParticipantThrottleRow
		var lastRefillStr, quarantineStr string
		if err := rows.Scan(&row.Key, &row.Tokens, &lastRefillStr, &row.Status, &row.EmptyStreamStreak, &row.EOFTransportFailureStreak, &quarantineStr); err != nil {
			return nil, fmt.Errorf("scan participant throttle: %w", err)
		}
		row.LastRefillAt, err = time.Parse(time.RFC3339Nano, lastRefillStr)
		if err != nil {
			return nil, fmt.Errorf("parse last_refill_at for %s: %w", row.Key, err)
		}
		if strings.TrimSpace(quarantineStr) != "" {
			row.QuarantineUntil, err = time.Parse(time.RFC3339Nano, quarantineStr)
			if err != nil {
				return nil, fmt.Errorf("parse quarantine_until_utc for %s: %w", row.Key, err)
			}
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func gatewayBoolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func mustMarshalEscrowRotationModels(models []EscrowRotationModelSettings) string {
	if len(models) == 0 {
		return ""
	}
	b, err := json.Marshal(models)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func mustMarshalGatewayModelLimits(limits []GatewayModelLimitSettings) string {
	limits = normalizeGatewayModelLimits(limits)
	if len(limits) == 0 {
		return ""
	}
	b, err := json.Marshal(limits)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func ensureGatewaySettingsColumn(db *sql.DB, columnName, columnDDL string) error {
	return ensureColumn(db, "gateway_settings", columnName, columnDDL)
}

func ensureGatewaySettingsTuningColumns(db *sql.DB) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{"participant_request_burst", "INTEGER NOT NULL DEFAULT 600"},
		{"participant_recovery_per_minute", "INTEGER NOT NULL DEFAULT 10"},
		{"participant_http_quarantine_ms", "INTEGER NOT NULL DEFAULT 3600000"},
		{"participant_transport_failure_quarantine_ms", "INTEGER NOT NULL DEFAULT 1800000"},
		{"participant_empty_stream_quarantine_ms", "INTEGER NOT NULL DEFAULT 1800000"},
		{"participant_stalled_winner_quarantine_ms", "INTEGER NOT NULL DEFAULT 1800000"},
		{"participant_empty_stream_threshold", "INTEGER NOT NULL DEFAULT 3"},
		{"participant_eof_transport_failure_threshold", "INTEGER NOT NULL DEFAULT 3"},
		{"redundancy_receipt_timeout_ms", "INTEGER NOT NULL DEFAULT 5000"},
		{"redundancy_first_token_timeout_floor_ms", "INTEGER NOT NULL DEFAULT 1000"},
		{"redundancy_per_input_token_first_token_lag_ms", "INTEGER NOT NULL DEFAULT 10"},
		{"redundancy_inter_chunk_stall_timeout_ms", "INTEGER NOT NULL DEFAULT 60000"},
		{"redundancy_streaming_attempt_hard_timeout_ms", "INTEGER NOT NULL DEFAULT 1200000"},
		{"redundancy_non_stream_response_floor_ms", "INTEGER NOT NULL DEFAULT 20000"},
		{"redundancy_non_stream_no_content_timeout_ms", "INTEGER NOT NULL DEFAULT 1200000"},
		{"redundancy_non_stream_max_attempt_wait_ms", "INTEGER NOT NULL DEFAULT 1800000"},
		{"redundancy_per_input_token_response_lag_ms", "INTEGER NOT NULL DEFAULT 20"},
		{"redundancy_secondary_wait_after_winner_ms", "INTEGER NOT NULL DEFAULT 600000"},
		{"redundancy_parallel_advantage_threshold", "REAL NOT NULL DEFAULT 0.5"},
		{"redundancy_unresponsive_threshold", "REAL NOT NULL DEFAULT 1.0"},
		{"redundancy_speed_policy", "TEXT NOT NULL DEFAULT 'hybrid'"},
		{"redundancy_pairwise_budget_percentile", "REAL NOT NULL DEFAULT 0.9"},
		{"redundancy_pairwise_max_proactive_attempts", "INTEGER NOT NULL DEFAULT 3"},
		{"redundancy_pairwise_min_direct_comparisons", "INTEGER NOT NULL DEFAULT 4"},
		{"redundancy_pairwise_winner_hold_ms", "INTEGER NOT NULL DEFAULT 500"},
		{"redundancy_pairwise_winner_hold_min_speedup", "REAL NOT NULL DEFAULT 0.1"},
		{"redundancy_pairwise_winner_hold_min_samples", "INTEGER NOT NULL DEFAULT 6"},
		{"perf_sample_size", "INTEGER NOT NULL DEFAULT 256"},
		{"perf_window_ms", "INTEGER NOT NULL DEFAULT 3600000"},
	}
	for _, column := range columns {
		if err := ensureGatewaySettingsColumn(db, column.name, column.ddl); err != nil {
			return err
		}
	}
	return nil
}

func ensureGatewaySettingsRotationColumns(db *sql.DB) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{"escrow_rotation_enabled", "INTEGER NOT NULL DEFAULT 0"},
		{"escrow_rotation_settlement_enabled", "INTEGER NOT NULL DEFAULT 0"},
		{"escrow_rotation_pre_poc_blocks", "INTEGER NOT NULL DEFAULT 300"},
		{"escrow_rotation_models_json", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		if err := ensureGatewaySettingsColumn(db, column.name, column.ddl); err != nil {
			return err
		}
	}
	return nil
}

func ensureGatewaySettingsDisabledColumns(db *sql.DB) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{"gateway_disabled_enabled", "INTEGER NOT NULL DEFAULT 0"},
		{"gateway_disabled_message", "TEXT NOT NULL DEFAULT ''"},
		{"gateway_disabled_new_url", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		if err := ensureGatewaySettingsColumn(db, column.name, column.ddl); err != nil {
			return err
		}
	}
	return nil
}

func ensureGatewayDevshardsColumn(db *sql.DB, columnName, columnDDL string) error {
	return ensureColumn(db, "gateway_devshards", columnName, columnDDL)
}

func ensureColumn(db *sql.DB, table, columnName, columnDDL string) error {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var dataType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == columnName {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, columnName, columnDDL))
	return err
}
