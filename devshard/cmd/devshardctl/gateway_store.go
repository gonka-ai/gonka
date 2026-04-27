package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type GatewaySettings struct {
	ChainREST               string                      `json:"chain_rest"`
	PublicAPI               string                      `json:"public_api"`
	DefaultModel            string                      `json:"default_model"`
	DefaultRequestMaxTokens uint64                      `json:"default_request_max_tokens"`
	MaxConcurrentRequests   int64                       `json:"max_concurrent_requests"`
	MaxInputTokensInFlight  int64                       `json:"max_input_tokens_in_flight"`
	ParticipantThrottle     ParticipantThrottleSettings `json:"participant_throttle"`
	Redundancy              RedundancySettings          `json:"redundancy"`
	Perf                    PerfSettings                `json:"perf"`
}

type ParticipantThrottleSettings struct {
	RequestBurst                   int   `json:"request_burst"`
	RecoveryPerMinute              int   `json:"recovery_per_minute"`
	HTTPQuarantineMS               int64 `json:"http_quarantine_ms"`
	TransportFailureQuarantineMS   int64 `json:"transport_failure_quarantine_ms"`
	EmptyStreamQuarantineMS        int64 `json:"empty_stream_quarantine_ms"`
	StalledWinnerQuarantineMS      int64 `json:"stalled_winner_quarantine_ms"`
	EmptyStreamQuarantineThreshold int   `json:"empty_stream_threshold"`
}

type RedundancySettings struct {
	ReceiptTimeoutMS             int64   `json:"receipt_timeout_ms"`
	FirstTokenTimeoutFloorMS     int64   `json:"first_token_timeout_floor_ms"`
	PerInputTokenFirstTokenLagMS int64   `json:"per_input_token_first_token_lag_ms"`
	InterChunkStallTimeoutMS     int64   `json:"inter_chunk_stall_timeout_ms"`
	NonStreamResponseFloorMS     int64   `json:"non_stream_response_floor_ms"`
	PerInputTokenResponseLagMS   int64   `json:"per_input_token_response_lag_ms"`
	SecondaryWaitAfterWinnerMS   int64   `json:"secondary_wait_after_winner_ms"`
	ParallelAdvantageThreshold   float64 `json:"parallel_advantage_threshold"`
	UnresponsiveThreshold        float64 `json:"unresponsive_threshold"`
}

type PerfSettings struct {
	SampleSize int   `json:"sample_size"`
	WindowMS   int64 `json:"window_ms"`
}

func DefaultGatewaySettingsTuning() (ParticipantThrottleSettings, RedundancySettings, PerfSettings) {
	return DefaultParticipantThrottleSettings(), DefaultRedundancySettings(), PerfSettings{
		SampleSize: 256,
		WindowMS:   int64(time.Hour / time.Millisecond),
	}
}

func (s GatewaySettings) WithTuningDefaults() GatewaySettings {
	participantDefaults, redundancyDefaults, perfDefaults := DefaultGatewaySettingsTuning()
	if s.ParticipantThrottle == (ParticipantThrottleSettings{}) {
		s.ParticipantThrottle = participantDefaults
	}
	if s.Redundancy == (RedundancySettings{}) {
		s.Redundancy = redundancyDefaults
	}
	if s.Perf == (PerfSettings{}) {
		s.Perf = perfDefaults
	}
	return s
}

type GatewayDevshardState struct {
	RuntimeConfig
	Active    bool   `json:"active"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type GatewayState struct {
	Settings GatewaySettings      `json:"settings"`
	Devshards  []GatewayDevshardState `json:"devshards"`
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
			max_concurrent_requests INTEGER NOT NULL DEFAULT 512,
			max_input_tokens_in_flight INTEGER NOT NULL,
			participant_request_burst INTEGER NOT NULL DEFAULT 600,
			participant_recovery_per_minute INTEGER NOT NULL DEFAULT 10,
			participant_http_quarantine_ms INTEGER NOT NULL DEFAULT 3600000,
			participant_transport_failure_quarantine_ms INTEGER NOT NULL DEFAULT 1800000,
			participant_empty_stream_quarantine_ms INTEGER NOT NULL DEFAULT 1800000,
			participant_stalled_winner_quarantine_ms INTEGER NOT NULL DEFAULT 1800000,
			participant_empty_stream_threshold INTEGER NOT NULL DEFAULT 3,
			redundancy_receipt_timeout_ms INTEGER NOT NULL DEFAULT 5000,
			redundancy_first_token_timeout_floor_ms INTEGER NOT NULL DEFAULT 1000,
			redundancy_per_input_token_first_token_lag_ms INTEGER NOT NULL DEFAULT 10,
			redundancy_inter_chunk_stall_timeout_ms INTEGER NOT NULL DEFAULT 60000,
			redundancy_non_stream_response_floor_ms INTEGER NOT NULL DEFAULT 20000,
			redundancy_per_input_token_response_lag_ms INTEGER NOT NULL DEFAULT 20,
			redundancy_secondary_wait_after_winner_ms INTEGER NOT NULL DEFAULT 300000,
			redundancy_parallel_advantage_threshold REAL NOT NULL DEFAULT 0.5,
			redundancy_unresponsive_threshold REAL NOT NULL DEFAULT 1.0,
			perf_sample_size INTEGER NOT NULL DEFAULT 256,
			perf_window_ms INTEGER NOT NULL DEFAULT 3600000,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS gateway_devshards (
			id TEXT PRIMARY KEY,
			private_key_hex TEXT NOT NULL,
			private_key_env TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			storage_path TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1,
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
	if err := ensureGatewaySettingsTuningColumns(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway tuning settings: %w", err)
	}
	if err := ensureGatewayDevshardsColumn(db, "protocol_version", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway devshards: %w", err)
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
	if err := ensureColumn(db, "participant_throttle_state", "quarantine_until_utc", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate participant throttle: %w", err)
	}
	if err := ensureColumn(db, "participant_throttle_state", "empty_stream_streak", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate participant throttle streak: %w", err)
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
		SELECT chain_rest, public_api, default_model, default_request_max_tokens,
		       max_concurrent_requests, max_input_tokens_in_flight,
		       participant_request_burst, participant_recovery_per_minute,
		       participant_http_quarantine_ms, participant_transport_failure_quarantine_ms,
		       participant_empty_stream_quarantine_ms, participant_stalled_winner_quarantine_ms,
		       participant_empty_stream_threshold,
		       redundancy_receipt_timeout_ms, redundancy_first_token_timeout_floor_ms,
		       redundancy_per_input_token_first_token_lag_ms, redundancy_inter_chunk_stall_timeout_ms,
		       redundancy_non_stream_response_floor_ms, redundancy_per_input_token_response_lag_ms,
		       redundancy_secondary_wait_after_winner_ms, redundancy_parallel_advantage_threshold,
		       redundancy_unresponsive_threshold, perf_sample_size, perf_window_ms
		FROM gateway_settings
		WHERE id = 1`)
	err := row.Scan(
		&state.Settings.ChainREST,
		&state.Settings.PublicAPI,
		&state.Settings.DefaultModel,
		&state.Settings.DefaultRequestMaxTokens,
		&state.Settings.MaxConcurrentRequests,
		&state.Settings.MaxInputTokensInFlight,
		&state.Settings.ParticipantThrottle.RequestBurst,
		&state.Settings.ParticipantThrottle.RecoveryPerMinute,
		&state.Settings.ParticipantThrottle.HTTPQuarantineMS,
		&state.Settings.ParticipantThrottle.TransportFailureQuarantineMS,
		&state.Settings.ParticipantThrottle.EmptyStreamQuarantineMS,
		&state.Settings.ParticipantThrottle.StalledWinnerQuarantineMS,
		&state.Settings.ParticipantThrottle.EmptyStreamQuarantineThreshold,
		&state.Settings.Redundancy.ReceiptTimeoutMS,
		&state.Settings.Redundancy.FirstTokenTimeoutFloorMS,
		&state.Settings.Redundancy.PerInputTokenFirstTokenLagMS,
		&state.Settings.Redundancy.InterChunkStallTimeoutMS,
		&state.Settings.Redundancy.NonStreamResponseFloorMS,
		&state.Settings.Redundancy.PerInputTokenResponseLagMS,
		&state.Settings.Redundancy.SecondaryWaitAfterWinnerMS,
		&state.Settings.Redundancy.ParallelAdvantageThreshold,
		&state.Settings.Redundancy.UnresponsiveThreshold,
		&state.Settings.Perf.SampleSize,
		&state.Settings.Perf.WindowMS,
	)
	if err == sql.ErrNoRows {
		return GatewayState{}, false, nil
	}
	if err != nil {
		return GatewayState{}, false, fmt.Errorf("load gateway settings: %w", err)
	}
	state.Settings = state.Settings.WithTuningDefaults()

	rows, err := s.db.Query(`
		SELECT id, private_key_hex, private_key_env, model, storage_path, active, created_at, updated_at, protocol_version
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
			id, chain_rest, public_api, default_model, default_request_max_tokens,
			max_concurrent_requests, max_input_tokens_in_flight,
			participant_request_burst, participant_recovery_per_minute,
			participant_http_quarantine_ms, participant_transport_failure_quarantine_ms,
			participant_empty_stream_quarantine_ms, participant_stalled_winner_quarantine_ms,
			participant_empty_stream_threshold,
			redundancy_receipt_timeout_ms, redundancy_first_token_timeout_floor_ms,
			redundancy_per_input_token_first_token_lag_ms, redundancy_inter_chunk_stall_timeout_ms,
			redundancy_non_stream_response_floor_ms, redundancy_per_input_token_response_lag_ms,
			redundancy_secondary_wait_after_winner_ms, redundancy_parallel_advantage_threshold,
			redundancy_unresponsive_threshold, perf_sample_size, perf_window_ms, updated_at
		) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(settings.ChainREST),
		strings.TrimSpace(settings.PublicAPI),
		strings.TrimSpace(settings.DefaultModel),
		settings.DefaultRequestMaxTokens,
		settings.MaxConcurrentRequests,
		settings.MaxInputTokensInFlight,
		settings.ParticipantThrottle.RequestBurst,
		settings.ParticipantThrottle.RecoveryPerMinute,
		settings.ParticipantThrottle.HTTPQuarantineMS,
		settings.ParticipantThrottle.TransportFailureQuarantineMS,
		settings.ParticipantThrottle.EmptyStreamQuarantineMS,
		settings.ParticipantThrottle.StalledWinnerQuarantineMS,
		settings.ParticipantThrottle.EmptyStreamQuarantineThreshold,
		settings.Redundancy.ReceiptTimeoutMS,
		settings.Redundancy.FirstTokenTimeoutFloorMS,
		settings.Redundancy.PerInputTokenFirstTokenLagMS,
		settings.Redundancy.InterChunkStallTimeoutMS,
		settings.Redundancy.NonStreamResponseFloorMS,
		settings.Redundancy.PerInputTokenResponseLagMS,
		settings.Redundancy.SecondaryWaitAfterWinnerMS,
		settings.Redundancy.ParallelAdvantageThreshold,
		settings.Redundancy.UnresponsiveThreshold,
		settings.Perf.SampleSize,
		settings.Perf.WindowMS,
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
		    max_concurrent_requests = ?,
		    max_input_tokens_in_flight = ?,
		    participant_request_burst = ?,
		    participant_recovery_per_minute = ?,
		    participant_http_quarantine_ms = ?,
		    participant_transport_failure_quarantine_ms = ?,
		    participant_empty_stream_quarantine_ms = ?,
		    participant_stalled_winner_quarantine_ms = ?,
		    participant_empty_stream_threshold = ?,
		    redundancy_receipt_timeout_ms = ?,
		    redundancy_first_token_timeout_floor_ms = ?,
		    redundancy_per_input_token_first_token_lag_ms = ?,
		    redundancy_inter_chunk_stall_timeout_ms = ?,
		    redundancy_non_stream_response_floor_ms = ?,
		    redundancy_per_input_token_response_lag_ms = ?,
		    redundancy_secondary_wait_after_winner_ms = ?,
		    redundancy_parallel_advantage_threshold = ?,
		    redundancy_unresponsive_threshold = ?,
		    perf_sample_size = ?,
		    perf_window_ms = ?,
		    updated_at = ?
		WHERE id = 1`,
		strings.TrimSpace(settings.ChainREST),
		strings.TrimSpace(settings.PublicAPI),
		strings.TrimSpace(settings.DefaultModel),
		settings.DefaultRequestMaxTokens,
		settings.MaxConcurrentRequests,
		settings.MaxInputTokensInFlight,
		settings.ParticipantThrottle.RequestBurst,
		settings.ParticipantThrottle.RecoveryPerMinute,
		settings.ParticipantThrottle.HTTPQuarantineMS,
		settings.ParticipantThrottle.TransportFailureQuarantineMS,
		settings.ParticipantThrottle.EmptyStreamQuarantineMS,
		settings.ParticipantThrottle.StalledWinnerQuarantineMS,
		settings.ParticipantThrottle.EmptyStreamQuarantineThreshold,
		settings.Redundancy.ReceiptTimeoutMS,
		settings.Redundancy.FirstTokenTimeoutFloorMS,
		settings.Redundancy.PerInputTokenFirstTokenLagMS,
		settings.Redundancy.InterChunkStallTimeoutMS,
		settings.Redundancy.NonStreamResponseFloorMS,
		settings.Redundancy.PerInputTokenResponseLagMS,
		settings.Redundancy.SecondaryWaitAfterWinnerMS,
		settings.Redundancy.ParallelAdvantageThreshold,
		settings.Redundancy.UnresponsiveThreshold,
		settings.Perf.SampleSize,
		settings.Perf.WindowMS,
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
			id, private_key_hex, private_key_env, model, storage_path, active, created_at, updated_at, protocol_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(devshard.ID),
		strings.TrimSpace(devshard.PrivateKeyHex),
		strings.TrimSpace(devshard.PrivateKeyEnv),
		strings.TrimSpace(devshard.Model),
		strings.TrimSpace(devshard.StoragePath),
		gatewayBoolToInt(devshard.Active),
		createdAt,
		now,
		strings.TrimSpace(devshard.ProtocolVersion),
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
	Key               string
	Tokens            float64
	LastRefillAt      time.Time
	Status            int
	QuarantineUntil   time.Time // wall-clock end of unified quarantine; zero if unset
	EmptyStreamStreak int
}

func (s *GatewayStore) SaveParticipantThrottle(key string, tokens float64, lastRefillAt time.Time, status int, quarantineUntil time.Time, emptyStreamStreak int) error {
	if s == nil || s.db == nil {
		return nil
	}
	quarStr := ""
	if !quarantineUntil.IsZero() {
		quarStr = quarantineUntil.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO participant_throttle_state
			(participant_key, tokens, last_refill_at, last_throttle_status, quarantine_until_utc, empty_stream_streak)
		VALUES (?, ?, ?, ?, ?, ?)`,
		key, tokens, lastRefillAt.UTC().Format(time.RFC3339Nano), status, quarStr, emptyStreamStreak)
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
		if err := rows.Scan(&row.Key, &row.Tokens, &lastRefillStr, &row.Status, &row.EmptyStreamStreak, &quarantineStr); err != nil {
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
		{"redundancy_receipt_timeout_ms", "INTEGER NOT NULL DEFAULT 5000"},
		{"redundancy_first_token_timeout_floor_ms", "INTEGER NOT NULL DEFAULT 1000"},
		{"redundancy_per_input_token_first_token_lag_ms", "INTEGER NOT NULL DEFAULT 10"},
		{"redundancy_inter_chunk_stall_timeout_ms", "INTEGER NOT NULL DEFAULT 60000"},
		{"redundancy_non_stream_response_floor_ms", "INTEGER NOT NULL DEFAULT 20000"},
		{"redundancy_per_input_token_response_lag_ms", "INTEGER NOT NULL DEFAULT 20"},
		{"redundancy_secondary_wait_after_winner_ms", "INTEGER NOT NULL DEFAULT 300000"},
		{"redundancy_parallel_advantage_threshold", "REAL NOT NULL DEFAULT 0.5"},
		{"redundancy_unresponsive_threshold", "REAL NOT NULL DEFAULT 1.0"},
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
