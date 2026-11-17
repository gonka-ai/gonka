package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// hostSampleBoundaryBatch continues LoadSamples' newest-first walk below beforeID for up to limit rows and returns the id of the live sample that ends it, or zero with the last id it read.
func (s *sqlitePerfStore) hostSampleBoundaryBatch(ctx context.Context, stopBefore time.Time, beforeID int64, limit int) (boundary, lastScannedID int64, scanned int, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, send_time, source_escrow FROM perf_host_samples WHERE id < ? ORDER BY id DESC LIMIT ?`, beforeID, limit)
	if err != nil {
		return 0, beforeID, 0, err
	}
	lastScannedID = beforeID
	var scanErr error
	for boundary == 0 && rows.Next() {
		var (
			id           int64
			sendTimeText string
			sourceEscrow string
		)
		if scanErr = rows.Scan(&id, &sendTimeText, &sourceEscrow); scanErr != nil {
			break
		}
		scanned++
		lastScannedID = id
		if isPastSampleWindow(strToTime(sendTimeText), sourceEscrow, stopBefore) {
			boundary = id
		}
	}
	if err := errors.Join(scanErr, rows.Err(), rows.Close()); err != nil {
		return 0, beforeID, 0, err
	}
	return boundary, lastScannedID, scanned, nil
}

// requestLogRetentionBoundary is the newest request log id LoadRequests no longer reads, or zero when it reads them all.
func (s *sqlitePerfStore) requestLogRetentionBoundary(ctx context.Context) (int64, error) {
	var boundary int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM perf_request_log ORDER BY id DESC LIMIT 1 OFFSET ?`, requestLogSize).Scan(&boundary)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return boundary, err
}

func (s *sqlitePerfStore) deleteHostSamplesUpTo(ctx context.Context, boundaryID int64, limit int) (int64, error) {
	return s.deleteRowsUpTo(ctx, `DELETE FROM perf_host_samples WHERE id IN (SELECT id FROM perf_host_samples WHERE id <= ? ORDER BY id LIMIT ?)`, boundaryID, limit)
}

func (s *sqlitePerfStore) deleteRequestLogUpTo(ctx context.Context, boundaryID int64, limit int) (int64, error) {
	return s.deleteRowsUpTo(ctx, `DELETE FROM perf_request_log WHERE id IN (SELECT id FROM perf_request_log WHERE id <= ? ORDER BY id LIMIT ?)`, boundaryID, limit)
}

func (s *sqlitePerfStore) deleteRowsUpTo(ctx context.Context, statement string, boundaryID int64, limit int) (int64, error) {
	result, err := s.db.ExecContext(ctx, statement, boundaryID, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *sqlitePerfStore) deleteUnretainedAccountingRequests(ctx context.Context, afterRowID int64, limit int, retainsEscrow func(string) bool) (int64, int, int64, error) {
	return s.deleteUnretainedAccountingBatch(ctx, `SELECT rowid, escrow_id FROM request_accounting WHERE rowid > ? ORDER BY rowid LIMIT ?`,
		`DELETE FROM request_accounting WHERE rowid IN (SELECT value FROM json_each(?))`, afterRowID, limit, retainsEscrow)
}

func (s *sqlitePerfStore) deleteUnretainedAccountingAttempts(ctx context.Context, afterRowID int64, limit int, retainsEscrow func(string) bool) (int64, int, int64, error) {
	return s.deleteUnretainedAccountingBatch(ctx, `SELECT rowid, escrow_id FROM request_accounting_attempts WHERE rowid > ? ORDER BY rowid LIMIT ?`,
		`DELETE FROM request_accounting_attempts WHERE rowid IN (SELECT value FROM json_each(?))`, afterRowID, limit, retainsEscrow)
}

func (s *sqlitePerfStore) deleteUnretainedAccountingAliases(ctx context.Context, afterRowID int64, limit int, retainsEscrow func(string) bool) (int64, int, int64, error) {
	return s.deleteUnretainedAccountingBatch(ctx, `SELECT rowid, escrow_id FROM request_accounting_aliases WHERE rowid > ? ORDER BY rowid LIMIT ?`,
		`DELETE FROM request_accounting_aliases WHERE rowid IN (SELECT value FROM json_each(?))`, afterRowID, limit, retainsEscrow)
}

// deleteUnretainedAccountingBatch reads the next limit rows after afterRowID and deletes those whose escrow is not retained; the check runs with no cursor open, since it may need the store's single connection or a lock held by a caller of the store.
func (s *sqlitePerfStore) deleteUnretainedAccountingBatch(ctx context.Context, selectBatch, deleteByRowIDs string, afterRowID int64, limit int, retainsEscrow func(string) bool) (lastRowID int64, scanned int, deleted int64, err error) {
	type accountingRow struct {
		rowID    int64
		escrowID string
	}
	rows, err := s.db.QueryContext(ctx, selectBatch, afterRowID, limit)
	if err != nil {
		return afterRowID, 0, 0, err
	}
	batch := make([]accountingRow, 0, limit)
	var scanErr error
	for rows.Next() {
		var row accountingRow
		if scanErr = rows.Scan(&row.rowID, &row.escrowID); scanErr != nil {
			break
		}
		batch = append(batch, row)
	}
	if err := errors.Join(scanErr, rows.Err(), rows.Close()); err != nil {
		return afterRowID, 0, 0, err
	}
	if len(batch) == 0 {
		return afterRowID, 0, 0, nil
	}
	retained := make(map[string]bool)
	var expired []int64
	for _, row := range batch {
		keep, checked := retained[row.escrowID]
		if !checked {
			keep = retainsEscrow(row.escrowID)
			retained[row.escrowID] = keep
		}
		if !keep {
			expired = append(expired, row.rowID)
		}
	}
	lastRowID = batch[len(batch)-1].rowID
	if len(expired) == 0 {
		return lastRowID, len(batch), 0, nil
	}
	expiredJSON, err := json.Marshal(expired)
	if err != nil {
		return afterRowID, len(batch), 0, err
	}
	result, err := s.db.ExecContext(ctx, deleteByRowIDs, string(expiredJSON))
	if err != nil {
		return afterRowID, len(batch), 0, err
	}
	deleted, err = result.RowsAffected()
	return lastRowID, len(batch), deleted, err
}
