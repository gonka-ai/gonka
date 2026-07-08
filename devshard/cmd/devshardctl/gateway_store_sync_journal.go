package main

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	gatewayTableSettings         = "gateway_settings"
	gatewayTableDevshards        = "gateway_devshards"
	gatewayTableThrottle         = "participant_throttle_state"
	gatewayTableRotationStatus   = "gateway_rotation_status"
	gatewayTableSuspiciousHosts  = "gateway_suspicious_hosts"
	gatewayTableCommitments      = "escrow_rotation_commitments"
	gatewaySettingsRowKey        = "1"
	gatewaySyncOpUpsert          = "upsert"
	gatewaySyncOpDelete          = "delete"
)

// gatewaySyncJournalDrainChunkSize bounds how many raw journal rows are loaded
// and coalesced per drain round. A var so tests can shrink it.
var gatewaySyncJournalDrainChunkSize = 1000

type gatewaySyncEntry struct {
	tableName string
	rowKey    string
	op        string
}

type gatewaySyncKey struct {
	tableName string
	rowKey    string
}

type gatewaySyncJournalRow struct {
	seq       int64
	tableName string
	rowKey    string
	op        string
}

func gatewayRotationStatusRowKey(modelID, stage string, epoch uint64) string {
	return fmt.Sprintf("%s|%s|%d", strings.TrimSpace(modelID), strings.TrimSpace(stage), epoch)
}

func parseRotationStatusRowKey(rowKey string) (string, string, uint64, error) {
	parts := strings.Split(rowKey, "|")
	if len(parts) != 3 {
		return "", "", 0, fmt.Errorf("invalid rotation status row key %q", rowKey)
	}
	epoch, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return "", "", 0, fmt.Errorf("parse rotation status epoch in %q: %w", rowKey, err)
	}
	return parts[0], parts[1], epoch, nil
}

func coalesceSyncJournalRows(rows []gatewaySyncJournalRow) map[gatewaySyncKey]string {
	out := make(map[gatewaySyncKey]string, len(rows))
	for _, row := range rows {
		out[gatewaySyncKey{tableName: row.tableName, rowKey: row.rowKey}] = row.op
	}
	return out
}

func (s *SQLiteGatewayStore) writeWithSyncJournal(entries []gatewaySyncEntry, fn func(*sql.Tx) error) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("gateway store unavailable")
	}
	if len(entries) == 0 {
		return fmt.Errorf("sync journal entries required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin sync journal write: %w", err)
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, entry := range entries {
		if _, err := tx.Exec(`
			INSERT INTO gateway_pg_sync_journal (table_name, row_key, op, enqueued_at)
			VALUES (?, ?, ?, ?)`,
			entry.tableName, entry.rowKey, entry.op, now,
		); err != nil {
			return fmt.Errorf("enqueue sync journal %s/%s: %w", entry.tableName, entry.rowKey, err)
		}
	}
	return tx.Commit()
}

func (s *SQLiteGatewayStore) countSyncJournalEntries() (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM gateway_pg_sync_journal`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count sync journal entries: %w", err)
	}
	return count, nil
}

func (s *SQLiteGatewayStore) loadSyncJournalChunk(afterSeq int64, limit int) ([]gatewaySyncJournalRow, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = gatewaySyncJournalDrainChunkSize
	}
	rows, err := s.db.Query(`
		SELECT seq, table_name, row_key, op
		FROM gateway_pg_sync_journal
		WHERE seq > ?
		ORDER BY seq ASC
		LIMIT ?`, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("load sync journal chunk after %d: %w", afterSeq, err)
	}
	defer rows.Close()

	var result []gatewaySyncJournalRow
	for rows.Next() {
		var row gatewaySyncJournalRow
		if err := rows.Scan(&row.seq, &row.tableName, &row.rowKey, &row.op); err != nil {
			return nil, fmt.Errorf("scan sync journal row: %w", err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *SQLiteGatewayStore) loadSyncJournalUpTo(maxSeq int64) ([]gatewaySyncJournalRow, error) {
	rows, err := s.db.Query(`
		SELECT seq, table_name, row_key, op
		FROM gateway_pg_sync_journal
		WHERE seq <= ?
		ORDER BY seq ASC`, maxSeq)
	if err != nil {
		return nil, fmt.Errorf("load sync journal up to %d: %w", maxSeq, err)
	}
	defer rows.Close()

	var result []gatewaySyncJournalRow
	for rows.Next() {
		var row gatewaySyncJournalRow
		if err := rows.Scan(&row.seq, &row.tableName, &row.rowKey, &row.op); err != nil {
			return nil, fmt.Errorf("scan sync journal row: %w", err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *SQLiteGatewayStore) deleteSyncJournalUpTo(maxSeq int64) error {
	if _, err := s.db.Exec(`DELETE FROM gateway_pg_sync_journal WHERE seq <= ?`, maxSeq); err != nil {
		return fmt.Errorf("delete sync journal up to %d: %w", maxSeq, err)
	}
	return nil
}

func drainGatewaySyncJournal(ctx context.Context, sqlite *SQLiteGatewayStore, pg *PostgresGatewayStore) (empty bool, err error) {
	if sqlite == nil || pg == nil || pg.pool == nil {
		return true, nil
	}
	count, err := sqlite.countSyncJournalEntries()
	if err != nil {
		return false, err
	}
	if count == 0 {
		return true, nil
	}

	var lastSeq int64
	for {
		chunk, err := sqlite.loadSyncJournalChunk(lastSeq, gatewaySyncJournalDrainChunkSize)
		if err != nil {
			return false, err
		}
		if len(chunk) == 0 {
			break
		}
		chunkMaxSeq := chunk[len(chunk)-1].seq
		coalesced := coalesceSyncJournalRows(chunk)
		if err := applyCoalescedSyncJournalToPG(ctx, pg, sqlite, coalesced); err != nil {
			return false, err
		}
		if err := sqlite.deleteSyncJournalUpTo(chunkMaxSeq); err != nil {
			return false, err
		}
		lastSeq = chunkMaxSeq
	}

	remaining, err := sqlite.countSyncJournalEntries()
	if err != nil {
		return false, err
	}
	return remaining == 0, nil
}

func applyCoalescedSyncJournalToPG(ctx context.Context, pg *PostgresGatewayStore, sqlite *SQLiteGatewayStore, coalesced map[gatewaySyncKey]string) error {
	if len(coalesced) == 0 {
		return nil
	}
	tx, err := pg.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin sync journal drain: %w", err)
	}
	defer tx.Rollback(ctx)

	for key, op := range coalesced {
		switch op {
		case gatewaySyncOpDelete:
			if err := deleteGatewayRowFromPG(ctx, tx, key.tableName, key.rowKey); err != nil {
				return err
			}
		case gatewaySyncOpUpsert:
			if err := applyGatewaySyncUpsertToPG(ctx, tx, sqlite, key.tableName, key.rowKey); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown sync journal op %q for %s/%s", op, key.tableName, key.rowKey)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit sync journal drain: %w", err)
	}
	return nil
}

func applyGatewaySyncUpsertToPG(ctx context.Context, tx pgx.Tx, sqlite *SQLiteGatewayStore, tableName, rowKey string) error {
	// When the SQLite row is gone, skip the upsert: per-chunk coalescing may replay
	// a stale upsert from an earlier chunk while the matching delete lives in a
	// later chunk. The delete removes the row from Postgres once that chunk runs.
	switch tableName {
	case gatewayTableSettings:
		settings, updatedAt, ok, err := sqlite.loadGatewaySettingsRow()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		return applyGatewaySettingsUpsertToPG(ctx, tx, settings, updatedAt)
	case gatewayTableDevshards:
		devshard, ok, err := sqlite.loadGatewayDevshardRow(rowKey)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		return applyGatewayDevshardToPG(ctx, tx, devshard, time.Now().UTC().Format(time.RFC3339Nano))
	case gatewayTableThrottle:
		row, ok, err := sqlite.loadParticipantThrottleRow(rowKey)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		return applyGatewayThrottleToPG(ctx, tx, row)
	case gatewayTableRotationStatus:
		modelID, stage, epoch, err := parseRotationStatusRowKey(rowKey)
		if err != nil {
			return err
		}
		status, ok, err := sqlite.loadGatewayRotationStatusRow(modelID, stage, epoch)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		return applyGatewayRotationStatusToPG(ctx, tx, status)
	case gatewayTableSuspiciousHosts:
		host, ok, err := sqlite.loadGatewaySuspiciousHostRow(rowKey)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		return applyGatewaySuspiciousHostToPG(ctx, tx, host)
	case gatewayTableCommitments:
		commitment, ok, err := sqlite.loadGatewayCommitmentRow(rowKey)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		return applyGatewayCommitmentToPG(ctx, tx, commitment)
	default:
		return fmt.Errorf("unknown sync journal table %q", tableName)
	}
}

func deleteGatewayRowFromPG(ctx context.Context, tx pgx.Tx, tableName, rowKey string) error {
	switch tableName {
	case gatewayTableSettings:
		_, err := tx.Exec(ctx, `DELETE FROM gateway_settings WHERE id = 1`)
		return err
	case gatewayTableDevshards:
		_, err := tx.Exec(ctx, `DELETE FROM gateway_devshards WHERE id = $1`, rowKey)
		return err
	case gatewayTableThrottle:
		_, err := tx.Exec(ctx, `DELETE FROM participant_throttle_state WHERE participant_key = $1`, rowKey)
		return err
	case gatewayTableRotationStatus:
		modelID, stage, epoch, err := parseRotationStatusRowKey(rowKey)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			DELETE FROM gateway_rotation_status
			WHERE model_id = $1 AND stage = $2 AND epoch = $3`,
			modelID, stage, epoch)
		return err
	case gatewayTableSuspiciousHosts:
		_, err := tx.Exec(ctx, `DELETE FROM gateway_suspicious_hosts WHERE participant_key = $1`, rowKey)
		return err
	case gatewayTableCommitments:
		_, err := tx.Exec(ctx, `DELETE FROM escrow_rotation_commitments WHERE tx_hash = $1`, rowKey)
		return err
	default:
		return fmt.Errorf("unknown sync journal table %q", tableName)
	}
}

func gatewaySettingsUpsertAssignmentsPG() string {
	cols := gatewaySettingsColumnNames()
	parts := make([]string, len(cols))
	for i, col := range cols {
		parts[i] = fmt.Sprintf("%s = EXCLUDED.%s", col, col)
	}
	return strings.Join(parts, ",\n\t\t    ")
}

func applyGatewaySettingsUpsertToPG(ctx context.Context, tx pgx.Tx, settings GatewaySettings, updatedAt string) error {
	insertArgs := settingsInsertArgs(settings, updatedAt)
	_, err := tx.Exec(ctx, fmt.Sprintf(`
		INSERT INTO gateway_settings (%s)
		VALUES (%s)
		ON CONFLICT (id) DO UPDATE SET %s,
			updated_at = EXCLUDED.updated_at`,
		gatewaySettingsInsertColumnNames(),
		pgPlaceholderList(len(insertArgs)),
		gatewaySettingsUpsertAssignmentsPG(),
	), insertArgs...)
	if err != nil {
		return fmt.Errorf("apply gateway settings to postgres: %w", err)
	}
	return nil
}

func applyGatewayDevshardToPG(ctx context.Context, tx pgx.Tx, devshard GatewayDevshardState, now string) error {
	createdAt := strings.TrimSpace(devshard.CreatedAt)
	if createdAt == "" {
		createdAt = now
	}
	updatedAt := strings.TrimSpace(devshard.UpdatedAt)
	if updatedAt == "" {
		updatedAt = now
	}
	_, err := tx.Exec(ctx, `
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
		updatedAt,
		strings.TrimSpace(devshard.RoutePrefix),
		strings.TrimSpace(devshard.ProtocolVersion),
		strings.TrimSpace(devshard.RotationRole),
		devshard.RotationEpoch,
		gatewayBoolToInt(devshard.SettlementPending),
	)
	if err != nil {
		return fmt.Errorf("apply gateway devshard %s to postgres: %w", devshard.ID, err)
	}
	return nil
}

func applyGatewayThrottleToPG(ctx context.Context, tx pgx.Tx, row ParticipantThrottleRow) error {
	quarStr := ""
	if !row.QuarantineUntil.IsZero() {
		quarStr = row.QuarantineUntil.UTC().Format(time.RFC3339Nano)
	}
	_, err := tx.Exec(ctx, `
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
		row.Key,
		row.Tokens,
		row.LastRefillAt.UTC().Format(time.RFC3339Nano),
		row.Status,
		quarStr,
		row.FailureStrikes,
		strings.Join(normalizeModelIDs(row.ModelIDs), ","),
	)
	if err != nil {
		return fmt.Errorf("apply participant throttle %s to postgres: %w", row.Key, err)
	}
	return nil
}

func applyGatewayRotationStatusToPG(ctx context.Context, tx pgx.Tx, status GatewayRotationStatus) error {
	updatedAt := strings.TrimSpace(status.UpdatedAt)
	if updatedAt == "" {
		updatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := tx.Exec(ctx, `
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
		updatedAt,
	)
	if err != nil {
		return fmt.Errorf("apply rotation status model=%q stage=%q epoch=%d to postgres: %w", status.ModelID, status.Stage, status.Epoch, err)
	}
	return nil
}

func applyGatewaySuspiciousHostToPG(ctx context.Context, tx pgx.Tx, host GatewaySuspiciousHost) error {
	createdAt := strings.TrimSpace(host.CreatedAt)
	if createdAt == "" {
		createdAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO gateway_suspicious_hosts (participant_key, note, created_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (participant_key) DO UPDATE
		SET note = EXCLUDED.note, created_at = EXCLUDED.created_at`,
		host.ParticipantKey, host.Note, createdAt,
	)
	if err != nil {
		return fmt.Errorf("apply suspicious host %s to postgres: %w", host.ParticipantKey, err)
	}
	return nil
}

func applyGatewayCommitmentToPG(ctx context.Context, tx pgx.Tx, c GatewayEscrowCommitment) error {
	createdAt := c.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	} else {
		createdAt = createdAt.UTC()
	}
	_, err := tx.Exec(ctx, `
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
		return fmt.Errorf("apply escrow commitment tx=%s to postgres: %w", c.TxHash, err)
	}
	return nil
}

func (s *SQLiteGatewayStore) loadGatewaySettingsRow() (GatewaySettings, string, bool, error) {
	if s == nil || s.db == nil {
		return GatewaySettings{}, "", false, nil
	}
	row := s.db.QueryRow(`
		SELECT ` + gatewaySettingsSelectColumns() + `
		FROM gateway_settings
		WHERE id = 1`)
	settings, err := scanGatewaySettings(row)
	if err == sql.ErrNoRows {
		return GatewaySettings{}, "", false, nil
	}
	if err != nil {
		return GatewaySettings{}, "", false, fmt.Errorf("load gateway settings row: %w", err)
	}
	var updatedAt string
	if err := s.db.QueryRow(`SELECT updated_at FROM gateway_settings WHERE id = 1`).Scan(&updatedAt); err != nil {
		return GatewaySettings{}, "", false, fmt.Errorf("load gateway settings updated_at: %w", err)
	}
	return settings, updatedAt, true, nil
}

func (s *SQLiteGatewayStore) loadGatewayDevshardRow(id string) (GatewayDevshardState, bool, error) {
	if s == nil || s.db == nil {
		return GatewayDevshardState{}, false, nil
	}
	var devshard GatewayDevshardState
	var active, settlementPending int
	err := s.db.QueryRow(`
		SELECT id, private_key_hex, private_key_env, model, storage_path, active, created_at, updated_at, route_prefix,
		       protocol_version, rotation_role, rotation_epoch, settlement_pending
		FROM gateway_devshards
		WHERE id = ?`, strings.TrimSpace(id)).Scan(
		&devshard.ID, &devshard.PrivateKeyHex, &devshard.PrivateKeyEnv, &devshard.Model, &devshard.StoragePath,
		&active, &devshard.CreatedAt, &devshard.UpdatedAt, &devshard.RoutePrefix, &devshard.ProtocolVersion,
		&devshard.RotationRole, &devshard.RotationEpoch, &settlementPending,
	)
	if err == sql.ErrNoRows {
		return GatewayDevshardState{}, false, nil
	}
	if err != nil {
		return GatewayDevshardState{}, false, fmt.Errorf("load gateway devshard %s: %w", id, err)
	}
	devshard.Active = active != 0
	devshard.SettlementPending = settlementPending != 0
	return devshard, true, nil
}

func (s *SQLiteGatewayStore) loadParticipantThrottleRow(key string) (ParticipantThrottleRow, bool, error) {
	if s == nil || s.db == nil {
		return ParticipantThrottleRow{}, false, nil
	}
	var row ParticipantThrottleRow
	var lastRefillStr, quarantineStr, modelIDsStr string
	err := s.db.QueryRow(`
		SELECT participant_key, tokens, last_refill_at, last_throttle_status,
		       IFNULL(quarantine_until_utc, '') AS quarantine_until_utc,
		       IFNULL(failure_strikes, 0) AS failure_strikes,
		       IFNULL(model_ids, '') AS model_ids
		FROM participant_throttle_state
		WHERE participant_key = ?`, key).Scan(
		&row.Key, &row.Tokens, &lastRefillStr, &row.Status, &quarantineStr, &row.FailureStrikes, &modelIDsStr,
	)
	if err == sql.ErrNoRows {
		return ParticipantThrottleRow{}, false, nil
	}
	if err != nil {
		return ParticipantThrottleRow{}, false, fmt.Errorf("load participant throttle %s: %w", key, err)
	}
	row.ModelIDs = splitModelIDs(modelIDsStr)
	row.LastRefillAt, err = time.Parse(time.RFC3339Nano, lastRefillStr)
	if err != nil {
		return ParticipantThrottleRow{}, false, fmt.Errorf("parse last_refill_at for %s: %w", key, err)
	}
	if strings.TrimSpace(quarantineStr) != "" {
		row.QuarantineUntil, err = time.Parse(time.RFC3339Nano, quarantineStr)
		if err != nil {
			return ParticipantThrottleRow{}, false, fmt.Errorf("parse quarantine_until_utc for %s: %w", key, err)
		}
	}
	return row, true, nil
}

func (s *SQLiteGatewayStore) loadGatewayRotationStatusRow(modelID, stage string, epoch uint64) (GatewayRotationStatus, bool, error) {
	if s == nil || s.db == nil {
		return GatewayRotationStatus{}, false, nil
	}
	var status GatewayRotationStatus
	var completed int
	err := s.db.QueryRow(`
		SELECT model_id, stage, epoch, role, target_count, existing_count, created_count,
		       promoted_count, settled_count, settle_failed_count, create_error, completed, updated_at
		FROM gateway_rotation_status
		WHERE model_id = ? AND stage = ? AND epoch = ?`,
		strings.TrimSpace(modelID), strings.TrimSpace(stage), epoch,
	).Scan(
		&status.ModelID, &status.Stage, &status.Epoch, &status.Role,
		&status.TargetCount, &status.ExistingCount, &status.CreatedCount,
		&status.PromotedCount, &status.SettledCount, &status.SettleFailedCount,
		&status.CreateError, &completed, &status.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return GatewayRotationStatus{}, false, nil
	}
	if err != nil {
		return GatewayRotationStatus{}, false, fmt.Errorf("load rotation status: %w", err)
	}
	status.Completed = completed != 0
	return status, true, nil
}

func (s *SQLiteGatewayStore) loadGatewaySuspiciousHostRow(participantKey string) (GatewaySuspiciousHost, bool, error) {
	if s == nil || s.db == nil {
		return GatewaySuspiciousHost{}, false, nil
	}
	var host GatewaySuspiciousHost
	err := s.db.QueryRow(`
		SELECT participant_key, note, created_at
		FROM gateway_suspicious_hosts
		WHERE participant_key = ?`, strings.TrimSpace(participantKey),
	).Scan(&host.ParticipantKey, &host.Note, &host.CreatedAt)
	if err == sql.ErrNoRows {
		return GatewaySuspiciousHost{}, false, nil
	}
	if err != nil {
		return GatewaySuspiciousHost{}, false, fmt.Errorf("load suspicious host %s: %w", participantKey, err)
	}
	return host, true, nil
}

func (s *SQLiteGatewayStore) loadGatewayCommitmentRow(txHash string) (GatewayEscrowCommitment, bool, error) {
	if s == nil || s.db == nil {
		return GatewayEscrowCommitment{}, false, nil
	}
	var c GatewayEscrowCommitment
	var createdAt string
	err := s.db.QueryRow(`
		SELECT tx_hash, model, role, epoch, private_key_env, protocol_version, block_height, created_at
		FROM escrow_rotation_commitments
		WHERE tx_hash = ?`, strings.TrimSpace(txHash),
	).Scan(&c.TxHash, &c.Model, &c.Role, &c.Epoch, &c.PrivateKeyEnv, &c.ProtocolVersion, &c.BlockHeight, &createdAt)
	if err == sql.ErrNoRows {
		return GatewayEscrowCommitment{}, false, nil
	}
	if err != nil {
		return GatewayEscrowCommitment{}, false, fmt.Errorf("load commitment %s: %w", txHash, err)
	}
	c.CreatedAt = scanGatewayDBTime(createdAt)
	return c, true, nil
}

func (s *SQLiteGatewayStore) updateSettingsTx(tx *sql.Tx, settings GatewaySettings) error {
	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.Exec(fmt.Sprintf(`
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

func (s *SQLiteGatewayStore) saveRotationStatusTx(tx *sql.Tx, status GatewayRotationStatus) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if strings.TrimSpace(status.UpdatedAt) != "" {
		now = strings.TrimSpace(status.UpdatedAt)
	}
	_, err := tx.Exec(`
		INSERT OR REPLACE INTO gateway_rotation_status (
			model_id, stage, epoch, role, target_count, existing_count, created_count,
			promoted_count, settled_count, settle_failed_count, create_error, completed, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(status.ModelID), strings.TrimSpace(status.Stage), status.Epoch,
		strings.TrimSpace(status.Role), status.TargetCount, status.ExistingCount, status.CreatedCount,
		status.PromotedCount, status.SettledCount, status.SettleFailedCount,
		strings.TrimSpace(status.CreateError), gatewayBoolToInt(status.Completed), now,
	)
	return err
}

func (s *SQLiteGatewayStore) saveCommitmentTx(tx *sql.Tx, c GatewayEscrowCommitment) error {
	createdAt := c.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	} else {
		createdAt = createdAt.UTC()
	}
	_, err := tx.Exec(`
		INSERT OR REPLACE INTO escrow_rotation_commitments (
			tx_hash, model, role, epoch, private_key_env, protocol_version, block_height, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(c.TxHash), strings.TrimSpace(c.Model), strings.TrimSpace(c.Role),
		c.Epoch, c.PrivateKeyEnv, strings.TrimSpace(c.ProtocolVersion), c.BlockHeight,
		createdAt.Format(time.RFC3339Nano),
	)
	return err
}

func (s *SQLiteGatewayStore) deleteCommitmentTx(tx *sql.Tx, txHash string) error {
	_, err := tx.Exec(`DELETE FROM escrow_rotation_commitments WHERE tx_hash = ?`, strings.TrimSpace(txHash))
	return err
}

func (s *SQLiteGatewayStore) upsertSuspiciousHostsTx(tx *sql.Tx, participantKeys []string, note string) error {
	participantKeys = normalizeParticipantKeys(participantKeys)
	if len(participantKeys) == 0 {
		return fmt.Errorf("participant_keys must contain at least one key")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	note = strings.TrimSpace(note)
	for _, key := range participantKeys {
		if _, err := tx.Exec(`
			INSERT INTO gateway_suspicious_hosts (participant_key, note, created_at)
			VALUES (?, ?, ?)
			ON CONFLICT(participant_key) DO UPDATE SET note = excluded.note`,
			key, note, now); err != nil {
			return fmt.Errorf("upsert suspicious host %s: %w", key, err)
		}
	}
	return nil
}

func (s *SQLiteGatewayStore) deleteSuspiciousHostsTx(tx *sql.Tx, participantKeys []string) error {
	for _, key := range normalizeParticipantKeys(participantKeys) {
		if _, err := tx.Exec(`DELETE FROM gateway_suspicious_hosts WHERE participant_key = ?`, key); err != nil {
			return fmt.Errorf("delete suspicious host %s: %w", key, err)
		}
	}
	return nil
}

func (s *SQLiteGatewayStore) setDevshardActiveTx(tx *sql.Tx, id string, active bool) error {
	res, err := tx.Exec(`UPDATE gateway_devshards SET active = ?, updated_at = ? WHERE id = ?`,
		gatewayBoolToInt(active), time.Now().UTC().Format(time.RFC3339Nano), strings.TrimSpace(id))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("devshard %s not found", id)
	}
	return nil
}

func (s *SQLiteGatewayStore) setDevshardSettlementPendingTx(tx *sql.Tx, id string, pending bool) error {
	res, err := tx.Exec(`UPDATE gateway_devshards SET settlement_pending = ?, updated_at = ? WHERE id = ?`,
		gatewayBoolToInt(pending), time.Now().UTC().Format(time.RFC3339Nano), strings.TrimSpace(id))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("devshard %s not found", id)
	}
	return nil
}

func (s *SQLiteGatewayStore) deleteDevshardTx(tx *sql.Tx, id string) error {
	res, err := tx.Exec(`DELETE FROM gateway_devshards WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("devshard %s not found", id)
	}
	return nil
}

func (s *SQLiteGatewayStore) saveParticipantThrottleTx(tx *sql.Tx, key string, modelIDs []string, tokens float64, lastRefillAt time.Time, status int, quarantineUntil time.Time, failureStrikes int) error {
	quarStr := ""
	if !quarantineUntil.IsZero() {
		quarStr = quarantineUntil.UTC().Format(time.RFC3339Nano)
	}
	_, err := tx.Exec(`
		INSERT OR REPLACE INTO participant_throttle_state
			(participant_key, tokens, last_refill_at, last_throttle_status, quarantine_until_utc, failure_strikes, model_ids)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		key, tokens, lastRefillAt.UTC().Format(time.RFC3339Nano), status, quarStr, failureStrikes,
		strings.Join(normalizeModelIDs(modelIDs), ","),
	)
	return err
}

func (s *SQLiteGatewayStore) deleteParticipantThrottleTx(tx *sql.Tx, key string) error {
	_, err := tx.Exec(`DELETE FROM participant_throttle_state WHERE participant_key = ?`, key)
	return err
}

func (s *SQLiteGatewayStore) initializeFallbackTx(tx *sql.Tx, settings GatewaySettings, devshards []GatewayDevshardState) error {
	settings = settings.WithTuningDefaults()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM gateway_settings WHERE id = 1`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err := tx.Exec(fmt.Sprintf(`INSERT INTO gateway_settings (%s) VALUES (%s)`,
			gatewaySettingsInsertColumnNames(),
			sqlitePlaceholderList(len(settingsInsertArgs(settings, now))),
		), settingsInsertArgs(settings, now)...); err != nil {
			return err
		}
	}
	for _, devshard := range devshards {
		if err := s.upsertDevshardTx(tx, devshard, now); err != nil {
			return err
		}
	}
	return nil
}
