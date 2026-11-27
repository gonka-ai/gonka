package postgres

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
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (escrow_id) DO UPDATE SET
			private_key_env = excluded.private_key_env,
			model = excluded.model,
			active = excluded.active,
			rotation_role = excluded.rotation_role,
			rotation_epoch = excluded.rotation_epoch,
			gone_from_chain = CASE WHEN excluded.active = 1 THEN 0 ELSE devshards.gone_from_chain END,
			updated_at = ` + nowExpression
	unparkedCondition = `devshards.settlement_pending = 0 AND devshards.settle_tx_hash = ''`
	devshardColumns   = `escrow_id, private_key_env, model, active, rotation_role, rotation_epoch, settlement_pending, settle_tx_hash, route_prefix, chain_epoch, amount, gone_from_chain`
)

func flag(value bool) int64 {
	if value {
		return flagSet
	}
	return flagClear
}

func upsertArguments(record store.DevshardRecord) []any {
	return []any{record.EscrowID, record.PrivateKeyEnv, record.Model, flag(record.Active),
		string(record.RotationRole), record.RotationEpoch, flag(record.SettlementPending), record.RoutePrefix}
}

// UpsertDevshard replaces every field except settlement_pending, settle_tx_hash, route_prefix, chain_epoch, amount and gone_from_chain. See ../README.md, "The devshard registry".
func (s *Backend) UpsertDevshard(ctx context.Context, record store.DevshardRecord) error {
	if _, err := s.database.exec(ctx, upsertDevshardStatement, upsertArguments(record)...); err != nil {
		return fmt.Errorf("upserting devshard %s: %w", record.EscrowID, err)
	}
	return nil
}

// ListDevshards returns every record ordered by escrow id.
func (s *Backend) ListDevshards(ctx context.Context) ([]store.DevshardRecord, error) {
	var records []store.DevshardRecord
	err := s.database.forEachRow(ctx, `SELECT `+devshardColumns+` FROM devshards ORDER BY escrow_id`, nil, func(result *sql.Rows) error {
		record, err := scanDevshard(result)
		if err != nil {
			return err
		}
		records = append(records, record)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing devshards: %w", err)
	}
	return records, nil
}

// Devshard returns one record and whether it exists.
func (s *Backend) Devshard(ctx context.Context, escrowID string) (store.DevshardRecord, bool, error) {
	var record store.DevshardRecord
	err := s.database.scanRow(ctx, `SELECT `+devshardColumns+` FROM devshards WHERE escrow_id = $1`, []any{escrowID}, devshardDestinations(&record)...)
	if errors.Is(err, sql.ErrNoRows) {
		return store.DevshardRecord{}, false, nil
	}
	if err != nil {
		return store.DevshardRecord{}, false, fmt.Errorf("scanning devshard row: %w", err)
	}
	return record, true, nil
}

func devshardDestinations(record *store.DevshardRecord) []any {
	return []any{&record.EscrowID, &record.PrivateKeyEnv, &record.Model,
		&record.Active, &record.RotationRole, &record.RotationEpoch, &record.SettlementPending,
		&record.SettleTxHash, &record.RoutePrefix, &record.ChainEpoch, &record.Amount, &record.GoneFromChain}
}

func scanDevshard(result *sql.Rows) (store.DevshardRecord, error) {
	var record store.DevshardRecord
	if err := result.Scan(devshardDestinations(&record)...); err != nil {
		return store.DevshardRecord{}, fmt.Errorf("scanning devshard row: %w", err)
	}
	return record, nil
}

// SetDevshardActive turns serving on or off; turning it on also clears gone_from_chain.
func (s *Backend) SetDevshardActive(ctx context.Context, escrowID string, active bool) error {
	return s.updateDevshard(ctx, escrowID, `UPDATE devshards SET active = $1::bigint, gone_from_chain = CASE WHEN $1::bigint = 1 THEN 0 ELSE gone_from_chain END, updated_at = `+nowExpression+` WHERE escrow_id = $2`, flag(active), escrowID)
}

// SetDevshardSettlementPending queues or clears the settle.
func (s *Backend) SetDevshardSettlementPending(ctx context.Context, escrowID string, pending bool) error {
	return s.updateDevshard(ctx, escrowID, `UPDATE devshards SET settlement_pending = $1, updated_at = `+nowExpression+` WHERE escrow_id = $2`, flag(pending), escrowID)
}

// SetDevshardChainFacts records the epoch the chain stamped on the escrow and the amount it locked; a zero leaves its column as it was.
func (s *Backend) SetDevshardChainFacts(ctx context.Context, escrowID string, chainEpoch, amount uint64) error {
	return s.updateDevshard(ctx, escrowID, `UPDATE devshards SET
		chain_epoch = CASE WHEN $1::bigint > 0 THEN $1::bigint ELSE chain_epoch END,
		amount = CASE WHEN $2::bigint > 0 THEN $2::bigint ELSE amount END,
		updated_at = `+nowExpression+` WHERE escrow_id = $3`, chainEpoch, amount, escrowID)
}

// MarkDevshardGoneFromChain takes a row the chain no longer holds out of service and out of the unsettled count in one statement.
func (s *Backend) MarkDevshardGoneFromChain(ctx context.Context, escrowID string) error {
	return s.updateDevshard(ctx, escrowID, `UPDATE devshards SET active = 0, gone_from_chain = $1, updated_at = `+nowExpression+` WHERE escrow_id = $2`, flagSet, escrowID)
}

// ParkForSettlement deactivates and marks pending in one statement, because no recovery path picks up inactive-and-not-pending.
func (s *Backend) ParkForSettlement(ctx context.Context, escrowID string) error {
	return s.updateDevshard(ctx, escrowID, `UPDATE devshards SET active = 0, settlement_pending = 1, updated_at = `+nowExpression+` WHERE escrow_id = $1`, escrowID)
}

// ParkForSettlementIfActive reports whether it parked a serving row. See ../README.md, "The devshard registry".
func (s *Backend) ParkForSettlementIfActive(ctx context.Context, escrowID string) (bool, error) {
	return s.updateDevshardIfMatched(ctx, escrowID, `UPDATE devshards SET active = 0, settlement_pending = 1, updated_at = `+nowExpression+` WHERE escrow_id = $1 AND active = 1`, escrowID)
}

// DeleteDevshard removes the row, and reports a missing one.
func (s *Backend) DeleteDevshard(ctx context.Context, escrowID string) error {
	result, err := s.database.exec(ctx, `DELETE FROM devshards WHERE escrow_id = $1`, escrowID)
	if err != nil {
		return fmt.Errorf("deleting devshard %s: %w", escrowID, err)
	}
	return rows.RequireOneRow(result, escrowID)
}

func (s *Backend) updateDevshard(ctx context.Context, escrowID, query string, args ...any) error {
	result, err := s.database.exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating devshard %s: %w", escrowID, err)
	}
	return rows.RequireOneRow(result, escrowID)
}

func (s *Backend) updateDevshardIfMatched(ctx context.Context, escrowID, query string, args ...any) (bool, error) {
	result, err := s.database.exec(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("updating devshard %s: %w", escrowID, err)
	}
	return rows.MatchedOneRow(result, escrowID)
}

// DevshardSettleTxHash reads from the row, not from the caller's copy, which predates what an earlier step in the same tick broadcast.
func (s *Backend) DevshardSettleTxHash(ctx context.Context, escrowID string) (hash string, broadcastAt time.Time, err error) {
	var stamp string
	err = s.WithRetry(ctx, func() error {
		return s.database.scanRow(ctx, `SELECT settle_tx_hash, settle_tx_at FROM devshards WHERE escrow_id = $1`, []any{escrowID}, &hash, &stamp)
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
	return s.updateDevshard(ctx, escrowID, `UPDATE devshards SET rotation_role = $1, updated_at = `+nowExpression+` WHERE escrow_id = $2`, string(role), escrowID)
}

// SetDevshardSettleTxHash records what a settle broadcast, so a later tick can ask the chain about it instead of building a second one; the stamp is the process clock's, the one the tick reads it against.
func (s *Backend) SetDevshardSettleTxHash(ctx context.Context, escrowID, txHash string) error {
	return s.updateDevshard(ctx, escrowID, `UPDATE devshards SET settle_tx_hash = $1, settle_tx_at = $2, updated_at = `+nowExpression+` WHERE escrow_id = $3`,
		txHash, store.SettleStamp(txHash, time.Now()), escrowID)
}

// ActivateDevshardUnlessParked reports whether it activated the row; a parked row, or one carrying a settle hash, is left as it is.
func (s *Backend) ActivateDevshardUnlessParked(ctx context.Context, escrowID string) (bool, error) {
	return s.updateDevshardIfMatched(ctx, escrowID, `UPDATE devshards SET active = 1, gone_from_chain = 0, updated_at = `+nowExpression+` WHERE escrow_id = $1 AND `+unparkedCondition, escrowID)
}

// UpsertDevshardUnlessParked reports whether it wrote the row; it is UpsertDevshard except that a parked row, or one carrying a settle hash, is left as it is.
func (s *Backend) UpsertDevshardUnlessParked(ctx context.Context, record store.DevshardRecord) (bool, error) {
	return s.updateDevshardIfMatched(ctx, record.EscrowID, upsertDevshardStatement+` WHERE `+unparkedCondition, upsertArguments(record)...)
}
