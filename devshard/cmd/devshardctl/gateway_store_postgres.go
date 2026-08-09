package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"common/storage/pgtimeouts"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresGatewayStore struct {
	pool      *pgxpool.Pool
	opTimeout time.Duration
}

func (s *PostgresGatewayStore) opCtx(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if s == nil || s.opTimeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, s.opTimeout)
}

func NewPostgresGatewayStore(ctx context.Context) (*PostgresGatewayStore, error) {
	cfg, err := pgxpool.ParseConfig("") // reads libpq env vars (PGHOST, PGPORT, ...)
	if err != nil {
		return nil, fmt.Errorf("parse gateway postgres config: %w", err)
	}
	// Fail-closed open: a blackholed host must not hang boot; statement/lock
	// timeouts are the server-side backstop when PG_OPERATION_TIMEOUT=0.
	pgtimeouts.ApplyConnConfig(cfg.ConnConfig)

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect gateway postgres store: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping gateway postgres store: %w", err)
	}
	s := &PostgresGatewayStore{pool: pool, opTimeout: pgtimeouts.OperationTimeout()}
	if err := s.ensureSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *PostgresGatewayStore) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS gateway_settings (
			id SMALLINT PRIMARY KEY CHECK (id = 1),
			chain_rest TEXT NOT NULL,
			public_api TEXT NOT NULL DEFAULT '',
			default_model TEXT NOT NULL,
			default_request_max_tokens BIGINT NOT NULL,
			request_max_tokens_cap BIGINT NOT NULL DEFAULT 4096,
			max_concurrent_requests BIGINT NOT NULL DEFAULT 512,
			max_concurrent_requests_per_10000_weight DOUBLE PRECISION NOT NULL DEFAULT 5.0,
			poc_max_concurrent_requests_per_10000_weight DOUBLE PRECISION NOT NULL DEFAULT 10.0,
			max_input_tokens_in_flight BIGINT NOT NULL,
			model_limits_json TEXT NOT NULL DEFAULT '',
			model_access_json TEXT NOT NULL DEFAULT '',
			tx_gas_limit BIGINT NOT NULL DEFAULT 0,
			participant_request_burst BIGINT NOT NULL DEFAULT 600,
			participant_recovery_per_minute BIGINT NOT NULL DEFAULT 10,
			participant_http_quarantine_ms BIGINT NOT NULL DEFAULT 3600000,
			participant_transport_failure_quarantine_ms BIGINT NOT NULL DEFAULT 1800000,
			participant_empty_stream_quarantine_ms BIGINT NOT NULL DEFAULT 1800000,
			participant_stalled_winner_quarantine_ms BIGINT NOT NULL DEFAULT 1800000,
			participant_empty_stream_threshold BIGINT NOT NULL DEFAULT 3,
			participant_eof_transport_failure_threshold BIGINT NOT NULL DEFAULT 3,
			redundancy_receipt_timeout_ms BIGINT NOT NULL DEFAULT 5000,
			redundancy_first_token_timeout_floor_ms BIGINT NOT NULL DEFAULT 1000,
			redundancy_per_input_token_first_token_lag_ms BIGINT NOT NULL DEFAULT 10,
			redundancy_inter_chunk_stall_timeout_ms BIGINT NOT NULL DEFAULT 60000,
			redundancy_streaming_attempt_hard_timeout_ms BIGINT NOT NULL DEFAULT 1200000,
			redundancy_non_stream_response_floor_ms BIGINT NOT NULL DEFAULT 20000,
			redundancy_non_stream_no_content_timeout_ms BIGINT NOT NULL DEFAULT 1200000,
			redundancy_non_stream_max_attempt_wait_ms BIGINT NOT NULL DEFAULT 1800000,
			redundancy_per_input_token_response_lag_ms BIGINT NOT NULL DEFAULT 20,
			redundancy_secondary_wait_after_winner_ms BIGINT NOT NULL DEFAULT 600000,
			redundancy_parallel_advantage_threshold DOUBLE PRECISION NOT NULL DEFAULT 0.5,
			redundancy_unresponsive_threshold DOUBLE PRECISION NOT NULL DEFAULT 1.0,
			redundancy_speed_policy TEXT NOT NULL DEFAULT 'hybrid',
			redundancy_pairwise_budget_percentile DOUBLE PRECISION NOT NULL DEFAULT 0.9,
			redundancy_pairwise_max_proactive_attempts BIGINT NOT NULL DEFAULT 3,
			redundancy_pairwise_min_direct_comparisons BIGINT NOT NULL DEFAULT 4,
			redundancy_pairwise_winner_hold_ms BIGINT NOT NULL DEFAULT 500,
			redundancy_pairwise_winner_hold_min_speedup DOUBLE PRECISION NOT NULL DEFAULT 0.1,
			redundancy_pairwise_winner_hold_min_samples BIGINT NOT NULL DEFAULT 6,
			perf_sample_size BIGINT NOT NULL DEFAULT 256,
			perf_window_ms BIGINT NOT NULL DEFAULT 3600000,
			escrow_rotation_enabled SMALLINT NOT NULL DEFAULT 0,
			escrow_rotation_settlement_enabled SMALLINT NOT NULL DEFAULT 0,
			escrow_rotation_pre_poc_blocks BIGINT NOT NULL DEFAULT 300,
			escrow_rotation_models_json TEXT NOT NULL DEFAULT '',
			gateway_disabled_enabled SMALLINT NOT NULL DEFAULT 0,
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
			active SMALLINT NOT NULL DEFAULT 1,
			route_prefix TEXT NOT NULL DEFAULT '',
			rotation_role TEXT NOT NULL DEFAULT '',
			rotation_epoch BIGINT NOT NULL DEFAULT 0,
			settlement_pending SMALLINT NOT NULL DEFAULT 0,
			protocol_version TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS participant_throttle_state (
			participant_key TEXT PRIMARY KEY,
			tokens DOUBLE PRECISION NOT NULL DEFAULT 0,
			last_refill_at TEXT NOT NULL,
			last_throttle_status BIGINT NOT NULL DEFAULT 0,
			quarantine_until_utc TEXT NOT NULL DEFAULT '',
			failure_strikes BIGINT NOT NULL DEFAULT 0,
			model_ids TEXT NOT NULL DEFAULT '',
			empty_stream_streak BIGINT NOT NULL DEFAULT 0,
			eof_transport_failure_streak BIGINT NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS gateway_rotation_status (
			model_id TEXT NOT NULL,
			stage TEXT NOT NULL,
			epoch BIGINT NOT NULL,
			role TEXT NOT NULL DEFAULT '',
			target_count BIGINT NOT NULL DEFAULT 0,
			existing_count BIGINT NOT NULL DEFAULT 0,
			created_count BIGINT NOT NULL DEFAULT 0,
			promoted_count BIGINT NOT NULL DEFAULT 0,
			settled_count BIGINT NOT NULL DEFAULT 0,
			settle_failed_count BIGINT NOT NULL DEFAULT 0,
			create_error TEXT NOT NULL DEFAULT '',
			completed SMALLINT NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (model_id, stage, epoch)
		)`,
		`CREATE TABLE IF NOT EXISTS gateway_suspicious_hosts (
			participant_key TEXT PRIMARY KEY,
			note TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS escrow_rotation_commitments (
			tx_hash TEXT PRIMARY KEY,
			model TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT '',
			epoch BIGINT NOT NULL DEFAULT 0,
			private_key_env TEXT NOT NULL DEFAULT '',
			protocol_version TEXT NOT NULL DEFAULT '',
			block_height BIGINT NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("init gateway postgres store: %w", err)
		}
	}

	if err := ensurePGGatewaySettingsColumn(ctx, s.pool, "public_api", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate gateway store: %w", err)
	}
	if err := ensurePGGatewaySettingsColumn(ctx, s.pool, "tx_gas_limit", "BIGINT NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate gateway tx settings: %w", err)
	}
	if err := ensurePGGatewaySettingsColumn(ctx, s.pool, "model_limits_json", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate gateway model limits: %w", err)
	}
	if err := ensurePGGatewaySettingsColumn(ctx, s.pool, "model_access_json", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate gateway model access: %w", err)
	}
	if err := ensurePGGatewaySettingsColumn(ctx, s.pool, "request_max_tokens_cap", "BIGINT NOT NULL DEFAULT 4096"); err != nil {
		return fmt.Errorf("migrate gateway max token cap: %w", err)
	}
	if err := ensurePGGatewaySettingsColumn(ctx, s.pool, "max_concurrent_requests_per_10000_weight", "DOUBLE PRECISION NOT NULL DEFAULT 5.0"); err != nil {
		return fmt.Errorf("migrate gateway weight concurrency: %w", err)
	}
	if err := ensurePGGatewaySettingsColumn(ctx, s.pool, "poc_max_concurrent_requests_per_10000_weight", "DOUBLE PRECISION NOT NULL DEFAULT 10.0"); err != nil {
		return fmt.Errorf("migrate gateway poc weight concurrency: %w", err)
	}
	if err := ensurePGGatewaySettingsTuningColumns(ctx, s.pool); err != nil {
		return fmt.Errorf("migrate gateway tuning settings: %w", err)
	}
	if err := ensurePGGatewaySettingsRotationColumns(ctx, s.pool); err != nil {
		return fmt.Errorf("migrate gateway rotation settings: %w", err)
	}
	if err := ensurePGGatewaySettingsDisabledColumns(ctx, s.pool); err != nil {
		return fmt.Errorf("migrate gateway disabled settings: %w", err)
	}
	if err := ensurePGGatewayDevshardsColumn(ctx, s.pool, "route_prefix", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate gateway devshards route prefix: %w", err)
	}
	if err := ensurePGGatewayDevshardsColumn(ctx, s.pool, "rotation_role", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate gateway devshard role: %w", err)
	}
	if err := ensurePGGatewayDevshardsColumn(ctx, s.pool, "rotation_epoch", "BIGINT NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate gateway devshard epoch: %w", err)
	}
	if err := ensurePGGatewayDevshardsColumn(ctx, s.pool, "settlement_pending", "SMALLINT NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate gateway devshard settlement_pending: %w", err)
	}
	if err := ensurePGGatewayDevshardsColumn(ctx, s.pool, "protocol_version", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate gateway devshard protocol version: %w", err)
	}
	if err := ensurePGColumn(ctx, s.pool, "escrow_rotation_commitments", "protocol_version", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate escrow rotation commitments protocol version: %w", err)
	}

	if _, err := s.pool.Exec(ctx, `
		UPDATE participant_throttle_state
		SET failure_strikes = GREATEST(COALESCE(empty_stream_streak, 0), COALESCE(eof_transport_failure_streak, 0))
		WHERE COALESCE(failure_strikes, 0) = 0
		  AND (COALESCE(empty_stream_streak, 0) > 0 OR COALESCE(eof_transport_failure_streak, 0) > 0)`); err != nil {
		return fmt.Errorf("migrate participant throttle strike values: %w", err)
	}
	return nil
}

func (s *PostgresGatewayStore) Close() error {
	if s == nil || s.pool == nil {
		return nil
	}
	s.pool.Close()
	return nil
}

func (s *PostgresGatewayStore) LoadState(ctx context.Context) (GatewayState, bool, error) {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	var state GatewayState
	row := s.pool.QueryRow(ctx, `
		SELECT `+gatewaySettingsSelectColumns()+`
		FROM gateway_settings
		WHERE id = 1`)
	settings, err := scanGatewaySettings(row)
	if err == pgx.ErrNoRows {
		return GatewayState{}, false, nil
	}
	if err != nil {
		return GatewayState{}, false, fmt.Errorf("load gateway settings: %w", err)
	}
	state.Settings = settings

	rows, err := s.pool.Query(ctx, `
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
	suspiciousHosts, err := s.LoadSuspiciousHosts(ctx)
	if err != nil {
		return GatewayState{}, false, err
	}
	state.SuspiciousHosts = suspiciousHosts
	return state, true, nil
}

func (s *PostgresGatewayStore) Initialize(ctx context.Context, settings GatewaySettings, devshards []GatewayDevshardState) error {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	settings = settings.WithTuningDefaults()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin gateway init: %w", err)
	}
	defer tx.Rollback(ctx)

	var count int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM gateway_settings WHERE id = 1`).Scan(&count); err != nil {
		return fmt.Errorf("count gateway settings: %w", err)
	}
	if count > 0 {
		return nil
	}

	insertArgs := settingsInsertArgs(settings, now)
	if _, err := tx.Exec(ctx, fmt.Sprintf(`
		INSERT INTO gateway_settings (%s)
		VALUES (%s)`,
		gatewaySettingsInsertColumnNames(),
		pgPlaceholderList(len(insertArgs)),
	), insertArgs...); err != nil {
		return fmt.Errorf("insert gateway settings: %w", err)
	}

	for _, devshard := range devshards {
		if err := s.upsertDevshardTx(ctx, tx, devshard, now); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresGatewayStore) UpdateSettings(ctx context.Context, settings GatewaySettings) error {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	colCount := len(gatewaySettingsColumnNames())
	args := settingsUpdateArgs(settings, updatedAt)
	args = append(args, 1)
	cmdTag, err := s.pool.Exec(ctx, fmt.Sprintf(`
		UPDATE gateway_settings
		SET %s,
		    updated_at = $%d
		WHERE id = $%d`, gatewaySettingsUpdateAssignmentsPG(), colCount+1, colCount+2), args...)
	if err != nil {
		return fmt.Errorf("update gateway settings: %w", err)
	}
	if cmdTag.RowsAffected() == 0 {
		return fmt.Errorf("gateway settings not initialized")
	}
	return nil
}

func (s *PostgresGatewayStore) SaveRotationStatus(ctx context.Context, status GatewayRotationStatus) error {
	if s == nil || s.pool == nil {
		return nil
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if strings.TrimSpace(status.UpdatedAt) != "" {
		now = strings.TrimSpace(status.UpdatedAt)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO gateway_rotation_status (
			model_id, stage, epoch, role, target_count, existing_count, created_count,
			promoted_count, settled_count, settle_failed_count, create_error, completed, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (model_id, stage, epoch) DO UPDATE SET
			role = EXCLUDED.role,
			target_count = EXCLUDED.target_count,
			existing_count = EXCLUDED.existing_count,
			created_count = EXCLUDED.created_count,
			promoted_count = EXCLUDED.promoted_count,
			settled_count = EXCLUDED.settled_count,
			settle_failed_count = EXCLUDED.settle_failed_count,
			create_error = EXCLUDED.create_error,
			completed = EXCLUDED.completed,
			updated_at = EXCLUDED.updated_at`,
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

func (s *PostgresGatewayStore) LoadRotationStatuses(ctx context.Context, limit int) ([]GatewayRotationStatus, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	query := `
		SELECT model_id, stage, epoch, role, target_count, existing_count, created_count,
		       promoted_count, settled_count, settle_failed_count, create_error, completed, updated_at
		FROM gateway_rotation_status
		ORDER BY updated_at DESC`
	args := []any{}
	if limit > 0 {
		query += ` LIMIT $1`
		args = append(args, limit)
	}
	rows, err := s.pool.Query(ctx, query, args...)
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

func (s *PostgresGatewayStore) SaveCommitment(ctx context.Context, c GatewayEscrowCommitment) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("gateway store unavailable")
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	createdAt := c.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	} else {
		createdAt = createdAt.UTC()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO escrow_rotation_commitments (
			tx_hash, model, role, epoch, private_key_env, protocol_version, block_height, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (tx_hash) DO UPDATE SET
			model = EXCLUDED.model,
			role = EXCLUDED.role,
			epoch = EXCLUDED.epoch,
			private_key_env = EXCLUDED.private_key_env,
			protocol_version = EXCLUDED.protocol_version,
			block_height = EXCLUDED.block_height,
			created_at = EXCLUDED.created_at`,
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

func (s *PostgresGatewayStore) LoadCommitments(ctx context.Context) ([]GatewayEscrowCommitment, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `
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

func (s *PostgresGatewayStore) DeleteCommitment(ctx context.Context, txHash string) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("gateway store unavailable")
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	if _, err := s.pool.Exec(ctx, `DELETE FROM escrow_rotation_commitments WHERE tx_hash = $1`, strings.TrimSpace(txHash)); err != nil {
		return fmt.Errorf("delete escrow commitment tx=%s: %w", txHash, err)
	}
	return nil
}

func (s *PostgresGatewayStore) LoadSuspiciousHosts(ctx context.Context) ([]GatewaySuspiciousHost, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `
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

func (s *PostgresGatewayStore) UpsertSuspiciousHosts(ctx context.Context, participantKeys []string, note string) ([]GatewaySuspiciousHost, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	participantKeys = normalizeParticipantKeys(participantKeys)
	if len(participantKeys) == 0 {
		return nil, fmt.Errorf("participant_keys must contain at least one key")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin suspicious host upsert: %w", err)
	}
	defer tx.Rollback(ctx)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	note = strings.TrimSpace(note)
	for _, key := range participantKeys {
		if _, err := tx.Exec(ctx, `
			INSERT INTO gateway_suspicious_hosts (participant_key, note, created_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (participant_key) DO UPDATE SET note = EXCLUDED.note`,
			key, note, now); err != nil {
			return nil, fmt.Errorf("upsert suspicious host %s: %w", key, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit suspicious host upsert: %w", err)
	}
	return s.LoadSuspiciousHosts(ctx)
}

func (s *PostgresGatewayStore) DeleteSuspiciousHosts(ctx context.Context, participantKeys []string) ([]GatewaySuspiciousHost, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	participantKeys = normalizeParticipantKeys(participantKeys)
	if len(participantKeys) == 0 {
		return nil, fmt.Errorf("participant_keys must contain at least one key")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin suspicious host delete: %w", err)
	}
	defer tx.Rollback(ctx)
	for _, key := range participantKeys {
		if _, err := tx.Exec(ctx, `DELETE FROM gateway_suspicious_hosts WHERE participant_key = $1`, key); err != nil {
			return nil, fmt.Errorf("delete suspicious host %s: %w", key, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit suspicious host delete: %w", err)
	}
	return s.LoadSuspiciousHosts(ctx)
}

func (s *PostgresGatewayStore) UpsertDevshard(ctx context.Context, devshard GatewayDevshardState) error {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin devshard upsert: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := s.upsertDevshardTx(ctx, tx, devshard, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresGatewayStore) upsertDevshardTx(ctx context.Context, tx pgx.Tx, devshard GatewayDevshardState, now string) error {
	createdAt := now
	_ = tx.QueryRow(ctx, `SELECT created_at FROM gateway_devshards WHERE id = $1`, devshard.ID).Scan(&createdAt)
	// Preserve the existing settlement_pending marker so an unrelated upsert
	// never silently clears a queued settlement; a brand-new row falls back
	// to the value carried on devshard.
	settlementPending := gatewayBoolToInt(devshard.SettlementPending)
	_ = tx.QueryRow(ctx, `SELECT settlement_pending FROM gateway_devshards WHERE id = $1`, devshard.ID).Scan(&settlementPending)
	if _, err := tx.Exec(ctx, `
		INSERT INTO gateway_devshards (
			id, private_key_hex, private_key_env, model, storage_path, active, created_at, updated_at, route_prefix,
			protocol_version, rotation_role, rotation_epoch, settlement_pending
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (id) DO UPDATE SET
			private_key_hex = EXCLUDED.private_key_hex,
			private_key_env = EXCLUDED.private_key_env,
			model = EXCLUDED.model,
			storage_path = EXCLUDED.storage_path,
			active = EXCLUDED.active,
			created_at = EXCLUDED.created_at,
			updated_at = EXCLUDED.updated_at,
			route_prefix = EXCLUDED.route_prefix,
			protocol_version = EXCLUDED.protocol_version,
			rotation_role = EXCLUDED.rotation_role,
			rotation_epoch = EXCLUDED.rotation_epoch,
			settlement_pending = EXCLUDED.settlement_pending`,
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
func (s *PostgresGatewayStore) GetDevshard(ctx context.Context, id string) (GatewayDevshardState, bool, error) {
	if s == nil || s.pool == nil {
		return GatewayDevshardState{}, false, fmt.Errorf("gateway store unavailable")
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	id = strings.TrimSpace(id)
	var devshard GatewayDevshardState
	var active int
	var settlementPending int
	err := s.pool.QueryRow(ctx, `
		SELECT id, private_key_hex, private_key_env, model, storage_path, active, created_at, updated_at, route_prefix,
		       protocol_version, rotation_role, rotation_epoch, settlement_pending
		FROM gateway_devshards
		WHERE id = $1`, id).Scan(
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
	if err == pgx.ErrNoRows {
		return GatewayDevshardState{}, false, nil
	}
	if err != nil {
		return GatewayDevshardState{}, false, fmt.Errorf("get devshard %s: %w", id, err)
	}
	devshard.Active = active != 0
	devshard.SettlementPending = settlementPending != 0
	return devshard, true, nil
}

func (s *PostgresGatewayStore) SetDevshardActive(ctx context.Context, id string, active bool) error {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	cmdTag, err := s.pool.Exec(ctx, `
		UPDATE gateway_devshards
		SET active = $1, updated_at = $2
		WHERE id = $3`,
		gatewayBoolToInt(active),
		time.Now().UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(id),
	)
	if err != nil {
		return fmt.Errorf("update devshard %s active=%t: %w", id, active, err)
	}
	if cmdTag.RowsAffected() == 0 {
		return fmt.Errorf("devshard %s not found", id)
	}
	return nil
}

func (s *PostgresGatewayStore) SetDevshardSettlementPending(ctx context.Context, id string, pending bool) error {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	cmdTag, err := s.pool.Exec(ctx, `
		UPDATE gateway_devshards
		SET settlement_pending = $1, updated_at = $2
		WHERE id = $3`,
		gatewayBoolToInt(pending),
		time.Now().UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(id),
	)
	if err != nil {
		return fmt.Errorf("update devshard %s settlement_pending=%t: %w", id, pending, err)
	}
	if cmdTag.RowsAffected() == 0 {
		return fmt.Errorf("devshard %s not found", id)
	}
	return nil
}

func (s *PostgresGatewayStore) DeleteDevshard(ctx context.Context, id string) error {
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	cmdTag, err := s.pool.Exec(ctx, `DELETE FROM gateway_devshards WHERE id = $1`, strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("delete devshard %s: %w", id, err)
	}
	if cmdTag.RowsAffected() == 0 {
		return fmt.Errorf("devshard %s not found", id)
	}
	return nil
}

func (s *PostgresGatewayStore) SaveParticipantThrottle(ctx context.Context, key string, modelIDs []string, tokens float64, lastRefillAt time.Time, status int, quarantineUntil time.Time, failureStrikes int) error {
	if s == nil || s.pool == nil {
		return nil
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	quarStr := ""
	if !quarantineUntil.IsZero() {
		quarStr = quarantineUntil.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO participant_throttle_state
			(participant_key, tokens, last_refill_at, last_throttle_status, quarantine_until_utc, failure_strikes, model_ids)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (participant_key) DO UPDATE SET
			tokens = EXCLUDED.tokens,
			last_refill_at = EXCLUDED.last_refill_at,
			last_throttle_status = EXCLUDED.last_throttle_status,
			quarantine_until_utc = EXCLUDED.quarantine_until_utc,
			failure_strikes = EXCLUDED.failure_strikes,
			model_ids = EXCLUDED.model_ids`,
		key, tokens, lastRefillAt.UTC().Format(time.RFC3339Nano), status, quarStr, failureStrikes, strings.Join(normalizeModelIDs(modelIDs), ","))
	if err != nil {
		return fmt.Errorf("save participant throttle %s: %w", key, err)
	}
	return nil
}

func (s *PostgresGatewayStore) DeleteParticipantThrottle(ctx context.Context, key string) error {
	if s == nil || s.pool == nil {
		return nil
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	if _, err := s.pool.Exec(ctx, `DELETE FROM participant_throttle_state WHERE participant_key = $1`, key); err != nil {
		return fmt.Errorf("delete participant throttle %s: %w", key, err)
	}
	return nil
}

func (s *PostgresGatewayStore) LoadParticipantThrottles(ctx context.Context) ([]ParticipantThrottleRow, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	ctx, cancel := s.opCtx(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT participant_key, tokens, last_refill_at, last_throttle_status,
		       COALESCE(quarantine_until_utc, '') AS quarantine_until_utc,
		       COALESCE(failure_strikes, 0) AS failure_strikes,
		       COALESCE(model_ids, '') AS model_ids
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

func ensurePGColumn(ctx context.Context, pool *pgxpool.Pool, table, columnName, columnDDL string) error {
	_, err := pool.Exec(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s`, table, columnName, columnDDL))
	return err
}

func ensurePGGatewaySettingsColumn(ctx context.Context, pool *pgxpool.Pool, columnName, columnDDL string) error {
	return ensurePGColumn(ctx, pool, "gateway_settings", columnName, columnDDL)
}

func ensurePGGatewaySettingsTuningColumns(ctx context.Context, pool *pgxpool.Pool) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{"participant_request_burst", "BIGINT NOT NULL DEFAULT 600"},
		{"participant_recovery_per_minute", "BIGINT NOT NULL DEFAULT 10"},
		{"participant_http_quarantine_ms", "BIGINT NOT NULL DEFAULT 3600000"},
		{"participant_transport_failure_quarantine_ms", "BIGINT NOT NULL DEFAULT 1800000"},
		{"participant_empty_stream_quarantine_ms", "BIGINT NOT NULL DEFAULT 1800000"},
		{"participant_stalled_winner_quarantine_ms", "BIGINT NOT NULL DEFAULT 1800000"},
		{"participant_empty_stream_threshold", "BIGINT NOT NULL DEFAULT 3"},
		{"participant_eof_transport_failure_threshold", "BIGINT NOT NULL DEFAULT 3"},
		{"redundancy_receipt_timeout_ms", "BIGINT NOT NULL DEFAULT 5000"},
		{"redundancy_first_token_timeout_floor_ms", "BIGINT NOT NULL DEFAULT 1000"},
		{"redundancy_per_input_token_first_token_lag_ms", "BIGINT NOT NULL DEFAULT 10"},
		{"redundancy_inter_chunk_stall_timeout_ms", "BIGINT NOT NULL DEFAULT 60000"},
		{"redundancy_streaming_attempt_hard_timeout_ms", "BIGINT NOT NULL DEFAULT 1200000"},
		{"redundancy_non_stream_response_floor_ms", "BIGINT NOT NULL DEFAULT 20000"},
		{"redundancy_non_stream_no_content_timeout_ms", "BIGINT NOT NULL DEFAULT 1200000"},
		{"redundancy_non_stream_max_attempt_wait_ms", "BIGINT NOT NULL DEFAULT 1800000"},
		{"redundancy_per_input_token_response_lag_ms", "BIGINT NOT NULL DEFAULT 20"},
		{"redundancy_secondary_wait_after_winner_ms", "BIGINT NOT NULL DEFAULT 600000"},
		{"redundancy_parallel_advantage_threshold", "DOUBLE PRECISION NOT NULL DEFAULT 0.5"},
		{"redundancy_unresponsive_threshold", "DOUBLE PRECISION NOT NULL DEFAULT 1.0"},
		{"redundancy_speed_policy", "TEXT NOT NULL DEFAULT 'hybrid'"},
		{"redundancy_pairwise_budget_percentile", "DOUBLE PRECISION NOT NULL DEFAULT 0.9"},
		{"redundancy_pairwise_max_proactive_attempts", "BIGINT NOT NULL DEFAULT 3"},
		{"redundancy_pairwise_min_direct_comparisons", "BIGINT NOT NULL DEFAULT 4"},
		{"redundancy_pairwise_winner_hold_ms", "BIGINT NOT NULL DEFAULT 500"},
		{"redundancy_pairwise_winner_hold_min_speedup", "DOUBLE PRECISION NOT NULL DEFAULT 0.1"},
		{"redundancy_pairwise_winner_hold_min_samples", "BIGINT NOT NULL DEFAULT 6"},
		{"perf_sample_size", "BIGINT NOT NULL DEFAULT 256"},
		{"perf_window_ms", "BIGINT NOT NULL DEFAULT 3600000"},
	}
	for _, column := range columns {
		if err := ensurePGGatewaySettingsColumn(ctx, pool, column.name, column.ddl); err != nil {
			return err
		}
	}
	return nil
}

func ensurePGGatewaySettingsRotationColumns(ctx context.Context, pool *pgxpool.Pool) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{"escrow_rotation_enabled", "SMALLINT NOT NULL DEFAULT 0"},
		{"escrow_rotation_settlement_enabled", "SMALLINT NOT NULL DEFAULT 0"},
		{"escrow_rotation_pre_poc_blocks", "BIGINT NOT NULL DEFAULT 300"},
		{"escrow_rotation_models_json", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		if err := ensurePGGatewaySettingsColumn(ctx, pool, column.name, column.ddl); err != nil {
			return err
		}
	}
	return nil
}

func ensurePGGatewaySettingsDisabledColumns(ctx context.Context, pool *pgxpool.Pool) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{"gateway_disabled_enabled", "SMALLINT NOT NULL DEFAULT 0"},
		{"gateway_disabled_message", "TEXT NOT NULL DEFAULT ''"},
		{"gateway_disabled_new_url", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		if err := ensurePGGatewaySettingsColumn(ctx, pool, column.name, column.ddl); err != nil {
			return err
		}
	}
	return nil
}

func ensurePGGatewayDevshardsColumn(ctx context.Context, pool *pgxpool.Pool, columnName, columnDDL string) error {
	return ensurePGColumn(ctx, pool, "gateway_devshards", columnName, columnDDL)
}

var _ GatewayStore = (*PostgresGatewayStore)(nil)
