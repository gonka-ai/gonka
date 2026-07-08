package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteGatewayStore struct {
	db *sql.DB
}

func NewSQLiteGatewayStore(path string) (*SQLiteGatewayStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open gateway store: %w", err)
	}
	// Serialize access and wait on contention instead of failing with "database is
	// locked". Mirrors storage/sqlite.go.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("apply gateway store pragma %q: %w", pragma, err)
		}
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
			settlement_pending INTEGER NOT NULL DEFAULT 0,
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
	if err := ensureGatewayDevshardsColumn(db, "route_prefix", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway devshards route prefix: %w", err)
	}
	if err := ensureGatewayDevshardsColumn(db, "rotation_role", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway devshard role: %w", err)
	}
	if err := ensureGatewayDevshardsColumn(db, "rotation_epoch", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway devshard epoch: %w", err)
	}
	if err := ensureGatewayDevshardsColumn(db, "settlement_pending", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway devshard settlement_pending: %w", err)
	}
	if err := ensureGatewayDevshardsColumn(db, "protocol_version", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway devshard protocol version: %w", err)
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
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS gateway_suspicious_hosts (
		participant_key TEXT PRIMARY KEY,
		note TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("init gateway suspicious hosts table: %w", err)
	}
	// Write-ahead intent for an escrow create (written before the on-chain tx).
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS escrow_rotation_commitments (
		tx_hash TEXT PRIMARY KEY,
		model TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT '',
		epoch INTEGER NOT NULL DEFAULT 0,
		private_key_env TEXT NOT NULL DEFAULT '',
		block_height INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("init escrow rotation commitments table: %w", err)
	}
	if err := ensureColumn(db, "escrow_rotation_commitments", "protocol_version", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate escrow rotation commitments protocol version: %w", err)
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
	if err := ensureColumn(db, "participant_throttle_state", "model_ids", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate participant throttle models: %w", err)
	}
	if err := ensureColumn(db, "participant_throttle_state", "failure_strikes", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate participant throttle strikes: %w", err)
	}
	if _, err := db.Exec(`
		UPDATE participant_throttle_state
		SET failure_strikes = MAX(IFNULL(empty_stream_streak, 0), IFNULL(eof_transport_failure_streak, 0))
		WHERE IFNULL(failure_strikes, 0) = 0
		  AND (IFNULL(empty_stream_streak, 0) > 0 OR IFNULL(eof_transport_failure_streak, 0) > 0)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate participant throttle strike values: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS gateway_pg_sync_journal (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		table_name TEXT NOT NULL,
		row_key TEXT NOT NULL,
		op TEXT NOT NULL,
		enqueued_at TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("init gateway sync journal: %w", err)
	}

	return &SQLiteGatewayStore{db: db}, nil
}

func (s *SQLiteGatewayStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteGatewayStore) LoadState() (GatewayState, bool, error) {
	var state GatewayState
	row := s.db.QueryRow(`
		SELECT ` + gatewaySettingsSelectColumns() + `
		FROM gateway_settings
		WHERE id = 1`)
	settings, err := scanGatewaySettings(row)
	if err == sql.ErrNoRows {
		return GatewayState{}, false, nil
	}
	if err != nil {
		return GatewayState{}, false, fmt.Errorf("load gateway settings: %w", err)
	}
	state.Settings = settings

	rows, err := s.db.Query(`
		SELECT id, private_key_hex, private_key_env, model, storage_path, active, created_at, updated_at, route_prefix,
		       protocol_version, rotation_role, rotation_epoch, settlement_pending
		FROM gateway_devshards
		ORDER BY id`)
	if err != nil {
		return GatewayState{}, false, fmt.Errorf("load gateway devshards: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var devshard GatewayDevshardState
		var active int
		var settlementPending int
		if err := rows.Scan(
			&devshard.ID,
			&devshard.PrivateKeyHex,
			&devshard.PrivateKeyEnv,
			&devshard.Model,
			&devshard.StoragePath,
			&active,
			&devshard.CreatedAt,
			&devshard.UpdatedAt,
			&devshard.RoutePrefix,
			&devshard.ProtocolVersion,
			&devshard.RotationRole,
			&devshard.RotationEpoch,
			&settlementPending,
		); err != nil {
			return GatewayState{}, false, fmt.Errorf("scan gateway devshard: %w", err)
		}
		devshard.Active = active != 0
		devshard.SettlementPending = settlementPending != 0
		state.Devshards = append(state.Devshards, devshard)
	}
	if err := rows.Err(); err != nil {
		return GatewayState{}, false, fmt.Errorf("iterate gateway devshards: %w", err)
	}
	suspiciousHosts, err := s.LoadSuspiciousHosts()
	if err != nil {
		return GatewayState{}, false, err
	}
	state.SuspiciousHosts = suspiciousHosts
	return state, true, nil
}

func (s *SQLiteGatewayStore) Initialize(settings GatewaySettings, devshards []GatewayDevshardState) error {
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

	if _, err := tx.Exec(fmt.Sprintf(`
		INSERT INTO gateway_settings (%s)
		VALUES (%s)`,
		gatewaySettingsInsertColumnNames(),
		sqlitePlaceholderList(len(settingsInsertArgs(settings, now))),
	), settingsInsertArgs(settings, now)...); err != nil {
		return fmt.Errorf("insert gateway settings: %w", err)
	}

	for _, devshard := range devshards {
		if err := s.upsertDevshardTx(tx, devshard, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteGatewayStore) UpdateSettings(settings GatewaySettings) error {
	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.Exec(fmt.Sprintf(`
		UPDATE gateway_settings
		SET %s,
		    updated_at = ?
		WHERE id = 1`, gatewaySettingsUpdateAssignments("?")),
		settingsUpdateArgs(settings, updatedAt)...,
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

func (s *SQLiteGatewayStore) SaveRotationStatus(status GatewayRotationStatus) error {
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

func (s *SQLiteGatewayStore) LoadRotationStatuses(limit int) ([]GatewayRotationStatus, error) {
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

// SaveCommitment records a create intent, keyed by tx hash.
func (s *SQLiteGatewayStore) SaveCommitment(c GatewayEscrowCommitment) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("gateway store unavailable")
	}
	createdAt := c.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	} else {
		createdAt = createdAt.UTC()
	}
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO escrow_rotation_commitments (
			tx_hash, model, role, epoch, private_key_env, protocol_version, block_height, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(c.TxHash),
		strings.TrimSpace(c.Model),
		strings.TrimSpace(c.Role),
		c.Epoch,
		c.PrivateKeyEnv,
		strings.TrimSpace(c.ProtocolVersion),
		c.BlockHeight,
		createdAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("save escrow commitment tx=%s: %w", c.TxHash, err)
	}
	return nil
}

// LoadCommitments returns all pending commitments (oldest first).
func (s *SQLiteGatewayStore) LoadCommitments() ([]GatewayEscrowCommitment, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT tx_hash, model, role, epoch, private_key_env, protocol_version, block_height, created_at
		FROM escrow_rotation_commitments
		ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("load escrow commitments: %w", err)
	}
	defer rows.Close()
	var commitments []GatewayEscrowCommitment
	for rows.Next() {
		var c GatewayEscrowCommitment
		var createdAt string
		if err := rows.Scan(&c.TxHash, &c.Model, &c.Role, &c.Epoch, &c.PrivateKeyEnv, &c.ProtocolVersion, &c.BlockHeight, &createdAt); err != nil {
			return nil, fmt.Errorf("scan escrow commitment: %w", err)
		}
		c.CreatedAt = scanGatewayDBTime(createdAt)
		commitments = append(commitments, c)
	}
	return commitments, rows.Err()
}

// DeleteCommitment clears a commitment once its escrow is persisted (or proven absent).
func (s *SQLiteGatewayStore) DeleteCommitment(txHash string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("gateway store unavailable")
	}
	if _, err := s.db.Exec(`DELETE FROM escrow_rotation_commitments WHERE tx_hash = ?`, strings.TrimSpace(txHash)); err != nil {
		return fmt.Errorf("delete escrow commitment tx=%s: %w", txHash, err)
	}
	return nil
}

func (s *SQLiteGatewayStore) LoadSuspiciousHosts() ([]GatewaySuspiciousHost, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	return scanGatewaySuspiciousHosts(s.db)
}

// loadSuspiciousHostsTx reads the suspicious hosts within an open transaction.
// It must be used instead of LoadSuspiciousHosts inside writeWithSyncJournal:
// the SQLite handle is capped at one connection (SetMaxOpenConns(1)), so a
// separate s.db.Query while the tx holds that connection would deadlock.
func (s *SQLiteGatewayStore) loadSuspiciousHostsTx(tx *sql.Tx) ([]GatewaySuspiciousHost, error) {
	if tx == nil {
		return nil, fmt.Errorf("nil transaction")
	}
	return scanGatewaySuspiciousHosts(tx)
}

type gatewayRowQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func scanGatewaySuspiciousHosts(q gatewayRowQueryer) ([]GatewaySuspiciousHost, error) {
	rows, err := q.Query(`
		SELECT participant_key, note, created_at
		FROM gateway_suspicious_hosts
		ORDER BY participant_key`)
	if err != nil {
		return nil, fmt.Errorf("load gateway suspicious hosts: %w", err)
	}
	defer rows.Close()

	var hosts []GatewaySuspiciousHost
	for rows.Next() {
		var host GatewaySuspiciousHost
		if err := rows.Scan(&host.ParticipantKey, &host.Note, &host.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan gateway suspicious host: %w", err)
		}
		hosts = append(hosts, host)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return hosts, nil
}

func (s *SQLiteGatewayStore) UpsertSuspiciousHosts(participantKeys []string, note string) ([]GatewaySuspiciousHost, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	participantKeys = normalizeParticipantKeys(participantKeys)
	if len(participantKeys) == 0 {
		return nil, fmt.Errorf("participant_keys must contain at least one key")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin suspicious host upsert: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	note = strings.TrimSpace(note)
	for _, key := range participantKeys {
		if _, err := tx.Exec(`
			INSERT INTO gateway_suspicious_hosts (participant_key, note, created_at)
			VALUES (?, ?, ?)
			ON CONFLICT(participant_key) DO UPDATE SET note = excluded.note`,
			key, note, now); err != nil {
			return nil, fmt.Errorf("upsert suspicious host %s: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit suspicious host upsert: %w", err)
	}
	return s.LoadSuspiciousHosts()
}

func (s *SQLiteGatewayStore) DeleteSuspiciousHosts(participantKeys []string) ([]GatewaySuspiciousHost, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	participantKeys = normalizeParticipantKeys(participantKeys)
	if len(participantKeys) == 0 {
		return nil, fmt.Errorf("participant_keys must contain at least one key")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin suspicious host delete: %w", err)
	}
	defer tx.Rollback()
	for _, key := range participantKeys {
		if _, err := tx.Exec(`DELETE FROM gateway_suspicious_hosts WHERE participant_key = ?`, key); err != nil {
			return nil, fmt.Errorf("delete suspicious host %s: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit suspicious host delete: %w", err)
	}
	return s.LoadSuspiciousHosts()
}

func (s *SQLiteGatewayStore) UpsertDevshard(devshard GatewayDevshardState) error {
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

func (s *SQLiteGatewayStore) upsertDevshardTx(tx *sql.Tx, devshard GatewayDevshardState, now string) error {
	createdAt := now
	_ = tx.QueryRow(`SELECT created_at FROM gateway_devshards WHERE id = ?`, devshard.ID).Scan(&createdAt)
	// Preserve the existing settlement_pending marker so an unrelated upsert
	// never silently clears a queued settlement; a brand-new row falls back
	// to the value carried on devshard.
	settlementPending := gatewayBoolToInt(devshard.SettlementPending)
	_ = tx.QueryRow(`SELECT settlement_pending FROM gateway_devshards WHERE id = ?`, devshard.ID).Scan(&settlementPending)
	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO gateway_devshards (
			id, private_key_hex, private_key_env, model, storage_path, active, created_at, updated_at, route_prefix,
			protocol_version, rotation_role, rotation_epoch, settlement_pending
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(devshard.ID),
		strings.TrimSpace(devshard.PrivateKeyHex),
		strings.TrimSpace(devshard.PrivateKeyEnv),
		strings.TrimSpace(devshard.Model),
		strings.TrimSpace(devshard.StoragePath),
		gatewayBoolToInt(devshard.Active),
		createdAt,
		now,
		strings.TrimSpace(devshard.RoutePrefix),
		strings.TrimSpace(devshard.ProtocolVersion),
		strings.TrimSpace(devshard.RotationRole),
		devshard.RotationEpoch,
		settlementPending,
	); err != nil {
		return fmt.Errorf("upsert gateway devshard %s: %w", devshard.ID, err)
	}
	return nil
}

// GetDevshard returns the registry record for a single devshard. The second
// return value is false when no row exists for the id. It is used by lazy
// hydration to look up the config of a non-resident devshard without loading
// the entire registry.
func (s *SQLiteGatewayStore) GetDevshard(id string) (GatewayDevshardState, bool, error) {
	id = strings.TrimSpace(id)
	var devshard GatewayDevshardState
	var active int
	var settlementPending int
	err := s.db.QueryRow(`
		SELECT id, private_key_hex, private_key_env, model, storage_path, active, created_at, updated_at, route_prefix,
		       protocol_version, rotation_role, rotation_epoch, settlement_pending
		FROM gateway_devshards
		WHERE id = ?`, id).Scan(
		&devshard.ID,
		&devshard.PrivateKeyHex,
		&devshard.PrivateKeyEnv,
		&devshard.Model,
		&devshard.StoragePath,
		&active,
		&devshard.CreatedAt,
		&devshard.UpdatedAt,
		&devshard.RoutePrefix,
		&devshard.ProtocolVersion,
		&devshard.RotationRole,
		&devshard.RotationEpoch,
		&settlementPending,
	)
	if err == sql.ErrNoRows {
		return GatewayDevshardState{}, false, nil
	}
	if err != nil {
		return GatewayDevshardState{}, false, fmt.Errorf("get devshard %s: %w", id, err)
	}
	devshard.Active = active != 0
	devshard.SettlementPending = settlementPending != 0
	return devshard, true, nil
}

func (s *SQLiteGatewayStore) SetDevshardActive(id string, active bool) error {
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

func (s *SQLiteGatewayStore) SetDevshardSettlementPending(id string, pending bool) error {
	res, err := s.db.Exec(`
		UPDATE gateway_devshards
		SET settlement_pending = ?, updated_at = ?
		WHERE id = ?`,
		gatewayBoolToInt(pending),
		time.Now().UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(id),
	)
	if err != nil {
		return fmt.Errorf("update devshard %s settlement_pending=%t: %w", id, pending, err)
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

func (s *SQLiteGatewayStore) DeleteDevshard(id string) error {
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

func normalizeParticipantKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	return normalized
}

func (s *SQLiteGatewayStore) SaveParticipantThrottle(key string, modelIDs []string, tokens float64, lastRefillAt time.Time, status int, quarantineUntil time.Time, failureStrikes int) error {
	if s == nil || s.db == nil {
		return nil
	}
	quarStr := ""
	if !quarantineUntil.IsZero() {
		quarStr = quarantineUntil.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO participant_throttle_state
			(participant_key, tokens, last_refill_at, last_throttle_status, quarantine_until_utc, failure_strikes, model_ids)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		key, tokens, lastRefillAt.UTC().Format(time.RFC3339Nano), status, quarStr, failureStrikes, strings.Join(normalizeModelIDs(modelIDs), ","))
	if err != nil {
		return fmt.Errorf("save participant throttle %s: %w", key, err)
	}
	return nil
}

func (s *SQLiteGatewayStore) DeleteParticipantThrottle(key string) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM participant_throttle_state WHERE participant_key = ?`, key)
	if err != nil {
		return fmt.Errorf("delete participant throttle %s: %w", key, err)
	}
	return nil
}

func (s *SQLiteGatewayStore) LoadParticipantThrottles() ([]ParticipantThrottleRow, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT participant_key, tokens, last_refill_at, last_throttle_status,
		       IFNULL(quarantine_until_utc, '') AS quarantine_until_utc,
		       IFNULL(failure_strikes, 0) AS failure_strikes,
		       IFNULL(model_ids, '') AS model_ids
		FROM participant_throttle_state`)
	if err != nil {
		return nil, fmt.Errorf("load participant throttles: %w", err)
	}
	defer rows.Close()

	var result []ParticipantThrottleRow
	for rows.Next() {
		var row ParticipantThrottleRow
		var lastRefillStr, quarantineStr, modelIDsStr string
		if err := rows.Scan(&row.Key, &row.Tokens, &lastRefillStr, &row.Status, &quarantineStr, &row.FailureStrikes, &modelIDsStr); err != nil {
			return nil, fmt.Errorf("scan participant throttle: %w", err)
		}
		row.ModelIDs = splitModelIDs(modelIDsStr)
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

func sqlitePlaceholderList(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?, ", n), ", ")
}

var _ GatewayStore = (*SQLiteGatewayStore)(nil)
