package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type GatewaySettings struct {
	ChainREST                      string                      `json:"chain_rest"`
	ChainGRPC                      string                      `json:"chain_grpc,omitempty"`
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
	Active            bool   `json:"active"`
	SettlementPending bool   `json:"settlement_pending,omitempty"`
	RotationRole      string `json:"rotation_role,omitempty"`
	RotationEpoch     uint64 `json:"rotation_epoch,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
}

type GatewaySuspiciousHost struct {
	ParticipantKey string `json:"participant_key"`
	Note           string `json:"note,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
}

type GatewayState struct {
	Settings        GatewaySettings         `json:"settings"`
	Devshards       []GatewayDevshardState  `json:"devshards"`
	SuspiciousHosts []GatewaySuspiciousHost `json:"suspicious_hosts,omitempty"`
}

// GatewayStore persists gateway management state (settings, devshards, throttle, rotation).
type GatewayStore interface {
	Close() error
	LoadState() (GatewayState, bool, error)
	Initialize(settings GatewaySettings, devshards []GatewayDevshardState) error
	UpdateSettings(settings GatewaySettings) error
	SaveRotationStatus(status GatewayRotationStatus) error
	LoadRotationStatuses(limit int) ([]GatewayRotationStatus, error)
	SaveCommitment(c GatewayEscrowCommitment) error
	LoadCommitments() ([]GatewayEscrowCommitment, error)
	DeleteCommitment(txHash string) error
	LoadSuspiciousHosts() ([]GatewaySuspiciousHost, error)
	UpsertSuspiciousHosts(participantKeys []string, note string) ([]GatewaySuspiciousHost, error)
	DeleteSuspiciousHosts(participantKeys []string) ([]GatewaySuspiciousHost, error)
	UpsertDevshard(devshard GatewayDevshardState) error
	GetDevshard(id string) (GatewayDevshardState, bool, error)
	SetDevshardActive(id string, active bool) error
	SetDevshardSettlementPending(id string, pending bool) error
	DeleteDevshard(id string) error
	SaveParticipantThrottle(key string, modelIDs []string, tokens float64, lastRefillAt time.Time, status int, quarantineUntil time.Time, failureStrikes int) error
	DeleteParticipantThrottle(key string) error
	LoadParticipantThrottles() ([]ParticipantThrottleRow, error)
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

// GatewayEscrowCommitment is the write-ahead intent for one escrow create, keyed by tx hash.
type GatewayEscrowCommitment struct {
	TxHash          string
	Model           string
	Role            string
	Epoch           uint64
	PrivateKeyEnv   string
	ProtocolVersion string
	BlockHeight     uint64
	CreatedAt       time.Time
}

// ParticipantThrottleRow represents a persisted reactive throttle state for one host.
type ParticipantThrottleRow struct {
	Key             string
	ModelIDs        []string
	Tokens          float64
	LastRefillAt    time.Time
	Status          int
	QuarantineUntil time.Time
	FailureStrikes  int
}

func gatewaySettingsSelectColumns() string {
	return strings.Join(gatewaySettingsColumnNames(), ", ")
}

func gatewaySettingsColumnNames() []string {
	return []string{
		"chain_rest", "public_api", "default_model", "default_request_max_tokens", "request_max_tokens_cap",
		"max_concurrent_requests", "max_concurrent_requests_per_10000_weight",
		"poc_max_concurrent_requests_per_10000_weight", "max_input_tokens_in_flight",
		"model_limits_json", "model_access_json", "tx_gas_limit",
		"participant_request_burst", "participant_recovery_per_minute",
		"participant_http_quarantine_ms", "participant_transport_failure_quarantine_ms",
		"participant_empty_stream_quarantine_ms", "participant_stalled_winner_quarantine_ms",
		"participant_empty_stream_threshold", "participant_eof_transport_failure_threshold",
		"redundancy_receipt_timeout_ms", "redundancy_first_token_timeout_floor_ms",
		"redundancy_per_input_token_first_token_lag_ms", "redundancy_inter_chunk_stall_timeout_ms",
		"redundancy_streaming_attempt_hard_timeout_ms",
		"redundancy_non_stream_response_floor_ms", "redundancy_non_stream_no_content_timeout_ms",
		"redundancy_non_stream_max_attempt_wait_ms", "redundancy_per_input_token_response_lag_ms",
		"redundancy_secondary_wait_after_winner_ms", "redundancy_parallel_advantage_threshold",
		"redundancy_unresponsive_threshold", "redundancy_speed_policy", "redundancy_pairwise_budget_percentile",
		"redundancy_pairwise_max_proactive_attempts", "redundancy_pairwise_min_direct_comparisons",
		"redundancy_pairwise_winner_hold_ms", "redundancy_pairwise_winner_hold_min_speedup",
		"redundancy_pairwise_winner_hold_min_samples",
		"perf_sample_size", "perf_window_ms",
		"escrow_rotation_enabled", "escrow_rotation_settlement_enabled",
		"escrow_rotation_pre_poc_blocks", "escrow_rotation_models_json",
		"gateway_disabled_enabled", "gateway_disabled_message", "gateway_disabled_new_url",
	}
}

func gatewaySettingsInsertColumnNames() string {
	return "id, " + gatewaySettingsSelectColumns() + ", updated_at"
}

func gatewaySettingsUpdateAssignments(param string) string {
	cols := gatewaySettingsColumnNames()
	parts := make([]string, len(cols))
	for i, col := range cols {
		parts[i] = col + " = " + param
	}
	return strings.Join(parts, ",\n\t\t    ")
}

type gatewaySettingsRow struct {
	settings                  GatewaySettings
	rotationEnabled           int
	rotationSettlementEnabled int
	rotationModelsJSON        string
	modelLimitsJSON           string
	modelAccessJSON           string
	disabledEnabled           int
}

func newGatewaySettingsRow(settings GatewaySettings) gatewaySettingsRow {
	settings = settings.WithTuningDefaults()
	return gatewaySettingsRow{
		settings:                  settings,
		rotationEnabled:           gatewayBoolToInt(settings.EscrowRotation.Enabled),
		rotationSettlementEnabled: gatewayBoolToInt(settings.EscrowRotation.SettlementEnabled),
		rotationModelsJSON:        mustMarshalEscrowRotationModels(settings.EscrowRotation.Models),
		modelLimitsJSON:           mustMarshalGatewayModelLimits(settings.ModelLimits),
		modelAccessJSON:           "",
		disabledEnabled:           gatewayBoolToInt(settings.Disabled.Enabled),
	}
}

func (r *gatewaySettingsRow) scanDestinations() []any {
	s := &r.settings
	return []any{
		&s.ChainREST,
		&s.PublicAPI,
		&s.DefaultModel,
		&s.DefaultRequestMaxTokens,
		&s.RequestMaxTokensCap,
		&s.MaxConcurrentRequests,
		&s.MaxConcurrentPer10000Weight,
		&s.PoCMaxConcurrentPer10000Weight,
		&s.MaxInputTokensInFlight,
		&r.modelLimitsJSON,
		&r.modelAccessJSON,
		&s.TxGasLimit,
		&s.ParticipantThrottle.RequestBurst,
		&s.ParticipantThrottle.RecoveryPerMinute,
		&s.ParticipantThrottle.HTTPQuarantineMS,
		&s.ParticipantThrottle.TransportFailureQuarantineMS,
		&s.ParticipantThrottle.EmptyStreamQuarantineMS,
		&s.ParticipantThrottle.StalledWinnerQuarantineMS,
		&s.ParticipantThrottle.EmptyStreamQuarantineThreshold,
		&s.ParticipantThrottle.EOFTransportFailureThreshold,
		&s.Redundancy.ReceiptTimeoutMS,
		&s.Redundancy.FirstTokenTimeoutFloorMS,
		&s.Redundancy.PerInputTokenFirstTokenLagMS,
		&s.Redundancy.InterChunkStallTimeoutMS,
		&s.Redundancy.StreamingAttemptHardTimeoutMS,
		&s.Redundancy.NonStreamResponseFloorMS,
		&s.Redundancy.NonStreamNoContentTimeoutMS,
		&s.Redundancy.NonStreamMaxAttemptWaitMS,
		&s.Redundancy.PerInputTokenResponseLagMS,
		&s.Redundancy.SecondaryWaitAfterWinnerMS,
		&s.Redundancy.ParallelAdvantageThreshold,
		&s.Redundancy.UnresponsiveThreshold,
		&s.Redundancy.SpeedPolicy,
		&s.Redundancy.PairwiseBudgetPercentile,
		&s.Redundancy.PairwiseMaxProactiveAttempts,
		&s.Redundancy.PairwiseMinDirectComparisons,
		&s.Redundancy.PairwiseWinnerHoldMS,
		&s.Redundancy.PairwiseWinnerHoldMinSpeedup,
		&s.Redundancy.PairwiseWinnerHoldMinSamples,
		&s.Perf.SampleSize,
		&s.Perf.WindowMS,
		&r.rotationEnabled,
		&r.rotationSettlementEnabled,
		&s.EscrowRotation.PrePoCBlocks,
		&r.rotationModelsJSON,
		&r.disabledEnabled,
		&s.Disabled.Message,
		&s.Disabled.NewURL,
	}
}

func (r *gatewaySettingsRow) finish() (GatewaySettings, error) {
	settings := r.settings
	settings.EscrowRotation.Enabled = r.rotationEnabled != 0
	settings.EscrowRotation.SettlementEnabled = r.rotationSettlementEnabled != 0
	if strings.TrimSpace(r.rotationModelsJSON) != "" {
		if err := json.Unmarshal([]byte(r.rotationModelsJSON), &settings.EscrowRotation.Models); err != nil {
			return GatewaySettings{}, fmt.Errorf("load gateway rotation models: %w", err)
		}
	}
	if strings.TrimSpace(r.modelLimitsJSON) != "" {
		if err := json.Unmarshal([]byte(r.modelLimitsJSON), &settings.ModelLimits); err != nil {
			return GatewaySettings{}, fmt.Errorf("load gateway model limits: %w", err)
		}
	}
	if strings.TrimSpace(r.modelAccessJSON) != "" {
		var legacyModelAccess []GatewayModelAccessSettings
		if err := json.Unmarshal([]byte(r.modelAccessJSON), &legacyModelAccess); err != nil {
			return GatewaySettings{}, fmt.Errorf("load gateway model access: %w", err)
		}
		settings.ModelLimits = applyLegacyModelAccessToLimits(settings.ModelLimits, legacyModelAccess)
	}
	settings.Disabled.Enabled = r.disabledEnabled != 0
	return settings.WithTuningDefaults(), nil
}

func (r *gatewaySettingsRow) valueArgs() []any {
	s := r.settings
	return []any{
		strings.TrimSpace(s.ChainREST),
		strings.TrimSpace(s.PublicAPI),
		strings.TrimSpace(s.DefaultModel),
		s.DefaultRequestMaxTokens,
		s.RequestMaxTokensCap,
		s.MaxConcurrentRequests,
		s.MaxConcurrentPer10000Weight,
		s.PoCMaxConcurrentPer10000Weight,
		s.MaxInputTokensInFlight,
		r.modelLimitsJSON,
		r.modelAccessJSON,
		s.TxGasLimit,
		s.ParticipantThrottle.RequestBurst,
		s.ParticipantThrottle.RecoveryPerMinute,
		s.ParticipantThrottle.HTTPQuarantineMS,
		s.ParticipantThrottle.TransportFailureQuarantineMS,
		s.ParticipantThrottle.EmptyStreamQuarantineMS,
		s.ParticipantThrottle.StalledWinnerQuarantineMS,
		s.ParticipantThrottle.EmptyStreamQuarantineThreshold,
		s.ParticipantThrottle.EOFTransportFailureThreshold,
		s.Redundancy.ReceiptTimeoutMS,
		s.Redundancy.FirstTokenTimeoutFloorMS,
		s.Redundancy.PerInputTokenFirstTokenLagMS,
		s.Redundancy.InterChunkStallTimeoutMS,
		s.Redundancy.StreamingAttemptHardTimeoutMS,
		s.Redundancy.NonStreamResponseFloorMS,
		s.Redundancy.NonStreamNoContentTimeoutMS,
		s.Redundancy.NonStreamMaxAttemptWaitMS,
		s.Redundancy.PerInputTokenResponseLagMS,
		s.Redundancy.SecondaryWaitAfterWinnerMS,
		s.Redundancy.ParallelAdvantageThreshold,
		s.Redundancy.UnresponsiveThreshold,
		s.Redundancy.SpeedPolicy,
		s.Redundancy.PairwiseBudgetPercentile,
		s.Redundancy.PairwiseMaxProactiveAttempts,
		s.Redundancy.PairwiseMinDirectComparisons,
		s.Redundancy.PairwiseWinnerHoldMS,
		s.Redundancy.PairwiseWinnerHoldMinSpeedup,
		s.Redundancy.PairwiseWinnerHoldMinSamples,
		s.Perf.SampleSize,
		s.Perf.WindowMS,
		r.rotationEnabled,
		r.rotationSettlementEnabled,
		s.EscrowRotation.PrePoCBlocks,
		r.rotationModelsJSON,
		r.disabledEnabled,
		strings.TrimSpace(s.Disabled.Message),
		strings.TrimSpace(s.Disabled.NewURL),
	}
}

type gatewaySettingsScanner interface {
	Scan(dest ...any) error
}

func scanGatewaySettings(scanner gatewaySettingsScanner) (GatewaySettings, error) {
	var row gatewaySettingsRow
	if err := scanner.Scan(row.scanDestinations()...); err != nil {
		return GatewaySettings{}, err
	}
	return row.finish()
}

func settingsInsertArgs(settings GatewaySettings, updatedAt string) []any {
	row := newGatewaySettingsRow(settings)
	args := []any{1}
	args = append(args, row.valueArgs()...)
	return append(args, updatedAt)
}

func settingsUpdateArgs(settings GatewaySettings, updatedAt string) []any {
	row := newGatewaySettingsRow(settings)
	args := row.valueArgs()
	return append(args, updatedAt)
}

func pgPlaceholderList(n int) string {
	if n <= 0 {
		return ""
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("$%d", i+1)
	}
	return strings.Join(parts, ", ")
}

func gatewaySettingsUpdateAssignmentsPG() string {
	cols := gatewaySettingsColumnNames()
	parts := make([]string, len(cols))
	for i, col := range cols {
		parts[i] = fmt.Sprintf("%s = $%d", col, i+1)
	}
	return strings.Join(parts, ",\n\t\t    ")
}

func scanGatewayDBTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", raw); err == nil {
		return t.UTC()
	}
	return time.Time{}
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

