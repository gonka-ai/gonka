package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"devshard/cmd/gateway/store"
	"devshard/cmd/gateway/store/internal/rows"
)

const (
	upsertDevshardStatement = `
		INSERT INTO devshards (escrow_id, private_key_env, model, active, rotation_role, rotation_epoch, settlement_pending, route_prefix)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(escrow_id) DO UPDATE SET
			private_key_env = excluded.private_key_env,
			model = excluded.model,
			active = excluded.active,
			rotation_role = excluded.rotation_role,
			rotation_epoch = excluded.rotation_epoch,
			gone_from_chain = CASE WHEN excluded.active = 1 THEN 0 ELSE devshards.gone_from_chain END,
			updated_at = datetime('now')`
	unparkedCondition = `devshards.settlement_pending = 0 AND devshards.settle_tx_hash = ''`
	devshardColumns   = `escrow_id, private_key_env, model, active, rotation_role, rotation_epoch, settlement_pending, settle_tx_hash, route_prefix, chain_epoch, amount, gone_from_chain`
)

// UpsertDevshard replaces every field except settlement_pending, settle_tx_hash, route_prefix, chain_epoch, amount and gone_from_chain. See ../README.md, "The devshard registry".
func (s *Backend) UpsertDevshard(ctx context.Context, record store.DevshardRecord) error {
	_, err := s.db.ExecContext(ctx, upsertDevshardStatement,
		record.EscrowID, record.PrivateKeyEnv, record.Model, record.Active,
		record.RotationRole, record.RotationEpoch, record.SettlementPending, record.RoutePrefix)
	if err != nil {
		return fmt.Errorf("upserting devshard %s: %w", record.EscrowID, err)
	}
	return nil
}

// ListDevshards returns every record ordered by escrow id.
func (s *Backend) ListDevshards(ctx context.Context) ([]store.DevshardRecord, error) {
	result, err := s.db.QueryContext(ctx, `SELECT `+devshardColumns+` FROM devshards ORDER BY escrow_id`)
	if err != nil {
		return nil, fmt.Errorf("listing devshards: %w", err)
	}
	defer result.Close()
	var records []store.DevshardRecord
	for result.Next() {
		record, err := scanDevshard(result)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("iterating devshards: %w", err)
	}
	return records, nil
}

// Devshard returns one record and whether it exists.
func (s *Backend) Devshard(ctx context.Context, escrowID string) (store.DevshardRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+devshardColumns+` FROM devshards WHERE escrow_id = ?`, escrowID)
	record, err := scanDevshard(row)
	if errors.Is(err, sql.ErrNoRows) {
		return store.DevshardRecord{}, false, nil
	}
	if err != nil {
		return store.DevshardRecord{}, false, err
	}
	return record, true, nil
}

func scanDevshard(row interface {
	Scan(destinations ...any) error
}) (store.DevshardRecord, error) {
	var record store.DevshardRecord
	if err := row.Scan(&record.EscrowID, &record.PrivateKeyEnv, &record.Model,
		&record.Active, &record.RotationRole, &record.RotationEpoch, &record.SettlementPending,
		&record.SettleTxHash, &record.RoutePrefix, &record.ChainEpoch, &record.Amount, &record.GoneFromChain); err != nil {
		return store.DevshardRecord{}, fmt.Errorf("scanning devshard row: %w", err)
	}
	return record, nil
}

func (s *Backend) SetDevshardActive(ctx context.Context, escrowID string, active bool) error {
	return s.updateDevshardField(ctx, `UPDATE devshards SET active = ?1, gone_from_chain = CASE WHEN ?1 = 1 THEN 0 ELSE gone_from_chain END, updated_at = datetime('now') WHERE escrow_id = ?2`, active, escrowID)
}

func (s *Backend) SetDevshardSettlementPending(ctx context.Context, escrowID string, pending bool) error {
	return s.updateDevshardField(ctx, `UPDATE devshards SET settlement_pending = ?, updated_at = datetime('now') WHERE escrow_id = ?`, pending, escrowID)
}

// SetDevshardChainFacts records the epoch the chain stamped on the escrow and the amount it locked; a zero leaves its column as it was.
func (s *Backend) SetDevshardChainFacts(ctx context.Context, escrowID string, chainEpoch, amount uint64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE devshards SET
		chain_epoch = CASE WHEN CAST(? AS BIGINT) > 0 THEN ? ELSE chain_epoch END,
		amount = CASE WHEN CAST(? AS BIGINT) > 0 THEN ? ELSE amount END,
		updated_at = datetime('now') WHERE escrow_id = ?`,
		chainEpoch, chainEpoch, amount, amount, escrowID)
	if err != nil {
		return fmt.Errorf("recording chain facts of devshard %s: %w", escrowID, err)
	}
	return rows.RequireOneRow(result, escrowID)
}

// MarkDevshardGoneFromChain takes a row the chain no longer holds out of service and out of the unsettled count in one statement.
func (s *Backend) MarkDevshardGoneFromChain(ctx context.Context, escrowID string) error {
	return s.updateDevshardField(ctx, `UPDATE devshards SET active = 0, gone_from_chain = ?, updated_at = datetime('now') WHERE escrow_id = ?`, true, escrowID)
}

// ParkForSettlement deactivates and marks pending in one statement, because no recovery path picks up inactive-and-not-pending.
func (s *Backend) ParkForSettlement(ctx context.Context, escrowID string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE devshards SET active = 0, settlement_pending = 1, updated_at = datetime('now') WHERE escrow_id = ?`,
		escrowID)
	if err != nil {
		return fmt.Errorf("parking devshard %s: %w", escrowID, err)
	}
	return rows.RequireOneRow(result, escrowID)
}

// ParkForSettlementIfActive reports whether it parked a serving row. See ../README.md, "The devshard registry".
func (s *Backend) ParkForSettlementIfActive(ctx context.Context, escrowID string) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE devshards SET active = 0, settlement_pending = 1, updated_at = datetime('now') WHERE escrow_id = ? AND active = 1`,
		escrowID)
	if err != nil {
		return false, fmt.Errorf("parking devshard %s: %w", escrowID, err)
	}
	return rows.MatchedOneRow(result, escrowID)
}

func (s *Backend) DeleteDevshard(ctx context.Context, escrowID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM devshards WHERE escrow_id = ?`, escrowID)
	if err != nil {
		return fmt.Errorf("deleting devshard %s: %w", escrowID, err)
	}
	return rows.RequireOneRow(result, escrowID)
}

func (s *Backend) updateDevshardField(ctx context.Context, query string, value any, escrowID string) error {
	result, err := s.db.ExecContext(ctx, query, value, escrowID)
	if err != nil {
		return fmt.Errorf("updating devshard %s: %w", escrowID, err)
	}
	return rows.RequireOneRow(result, escrowID)
}

// DevshardSettleTxHash reads from the row, not from the caller's copy, which predates what an earlier step in the same tick broadcast.
func (s *Backend) DevshardSettleTxHash(ctx context.Context, escrowID string) (hash string, broadcastAt time.Time, err error) {
	var stamp string
	err = s.WithRetry(ctx, func() error {
		return s.db.QueryRowContext(ctx,
			`SELECT settle_tx_hash, settle_tx_at FROM devshards WHERE escrow_id = ?`, escrowID).Scan(&hash, &stamp)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("reading settle tx hash for escrow %s: %w", escrowID, err)
	}
	return hash, store.ParseSettleStamp(stamp), nil
}

// SetDevshardRotationRole touches nothing else: a whole-record upsert would carry the caller's stale copy back into the row.
func (s *Backend) SetDevshardRotationRole(ctx context.Context, escrowID string, role store.Role) error {
	return s.updateDevshardField(ctx,
		`UPDATE devshards SET rotation_role = ?, updated_at = datetime('now') WHERE escrow_id = ?`, role, escrowID)
}

// SetDevshardSettleTxHash records what a settle broadcast, so a later tick can ask the chain about it instead of building a second one; the stamp is the process clock's, the one the tick reads it against.
func (s *Backend) SetDevshardSettleTxHash(ctx context.Context, escrowID, txHash string) error {
	stamp := store.SettleStamp(txHash, time.Now())
	result, err := s.db.ExecContext(ctx,
		`UPDATE devshards SET settle_tx_hash = ?, settle_tx_at = ?, updated_at = datetime('now') WHERE escrow_id = ?`,
		txHash, stamp, escrowID)
	if err != nil {
		return fmt.Errorf("updating devshard %s: %w", escrowID, err)
	}
	return rows.RequireOneRow(result, escrowID)
}

// ActivateDevshardUnlessParked reports whether it activated the row; a parked row, or one carrying a settle hash, is left as it is.
func (s *Backend) ActivateDevshardUnlessParked(ctx context.Context, escrowID string) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE devshards SET active = 1, gone_from_chain = 0, updated_at = datetime('now') WHERE escrow_id = ? AND `+unparkedCondition,
		escrowID)
	if err != nil {
		return false, fmt.Errorf("activating devshard %s: %w", escrowID, err)
	}
	return rows.MatchedOneRow(result, escrowID)
}

// UpsertDevshardUnlessParked reports whether it wrote the row; it is UpsertDevshard except that a parked row, or one carrying a settle hash, is left as it is.
func (s *Backend) UpsertDevshardUnlessParked(ctx context.Context, record store.DevshardRecord) (bool, error) {
	result, err := s.db.ExecContext(ctx, upsertDevshardStatement+` WHERE `+unparkedCondition,
		record.EscrowID, record.PrivateKeyEnv, record.Model, record.Active,
		record.RotationRole, record.RotationEpoch, record.SettlementPending, record.RoutePrefix)
	if err != nil {
		return false, fmt.Errorf("upserting devshard %s: %w", record.EscrowID, err)
	}
	return rows.MatchedOneRow(result, record.EscrowID)
}
