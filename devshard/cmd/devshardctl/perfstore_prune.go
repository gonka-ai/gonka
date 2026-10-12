package main

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

var accountingTables = []string{"request_accounting", "request_accounting_attempts", "request_accounting_aliases"}

// hostSampleBoundaryBatch continues LoadSamples' newest-first walk below beforeID for up to limit rows and returns the id of the live sample that ends it, or zero with the last id it read.
func (s *PerfStore) hostSampleBoundaryBatch(stopBefore time.Time, beforeID int64, limit int) (boundary, lastScannedID int64, scanned int, err error) {
	rows, err := s.db.Query(`SELECT id, send_time, source_escrow FROM perf_host_samples WHERE id < ? ORDER BY id DESC LIMIT ?`, beforeID, limit)
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
func (s *PerfStore) requestLogRetentionBoundary() (int64, error) {
	var boundary int64
	err := s.db.QueryRow(`SELECT id FROM perf_request_log ORDER BY id DESC LIMIT 1 OFFSET ?`, requestLogSize).Scan(&boundary)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return boundary, err
}

func (s *PerfStore) deleteOldestRowsUpTo(table string, boundaryID int64, limit int) (int64, error) {
	result, err := s.db.Exec(`DELETE FROM `+table+` WHERE id IN (SELECT id FROM `+table+` WHERE id <= ? ORDER BY id LIMIT ?)`, boundaryID, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// deleteUnretainedAccountingBatch reads the next limit rows after afterRowID and deletes those whose escrow is not retained; the check runs with no cursor open, since it may need the store's single connection or a lock held by a caller of the store.
func (s *PerfStore) deleteUnretainedAccountingBatch(table string, afterRowID int64, limit int, retainsEscrow func(string) bool) (lastRowID int64, scanned int, deleted int64, err error) {
	type accountingRow struct {
		rowID    int64
		escrowID string
	}
	rows, err := s.db.Query(`SELECT rowid, escrow_id FROM `+table+` WHERE rowid > ? ORDER BY rowid LIMIT ?`, afterRowID, limit)
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
	var expired []any
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
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(expired)), ",")
	result, err := s.db.Exec(`DELETE FROM `+table+` WHERE rowid IN (`+placeholders+`)`, expired...)
	if err != nil {
		return afterRowID, len(batch), 0, err
	}
	deleted, err = result.RowsAffected()
	return lastRowID, len(batch), deleted, err
}
