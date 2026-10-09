package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

type RequestAccountingAttempt struct {
	RequestID      string    `json:"-"`
	EscrowID       string    `json:"-"`
	Nonce          uint64    `json:"nonce"`
	HostIdx        int       `json:"host_idx"`
	ParticipantKey string    `json:"participant_key,omitempty"`
	Probe          bool      `json:"probe"`
	Winner         bool      `json:"winner"`
	CreatedAt      time.Time `json:"created_at"`
}

type RequestAccountingRecord struct {
	RequestID           string                     `json:"request_id"`
	EscrowID            string                     `json:"escrow_id"`
	Model               string                     `json:"model,omitempty"`
	StartedAt           time.Time                  `json:"started_at"`
	CompletedAt         time.Time                  `json:"completed_at,omitempty"`
	Outcome             string                     `json:"outcome"`
	Decision            string                     `json:"decision,omitempty"`
	WinnerNonce         uint64                     `json:"winner_nonce,omitempty"`
	CachedFromRequestID string                     `json:"cached_from_request_id,omitempty"`
	CachedFromEscrowID  string                     `json:"cached_from_escrow_id,omitempty"`
	Attempts            []RequestAccountingAttempt `json:"attempts"`
}

type requestAccountingAlias struct {
	sourceRequestID string
	sourceEscrowID  string
	reason          string
}

// importedAccountingRequest is a request_accounting row of a per-escrow perf file, with times kept as written.
type importedAccountingRequest struct {
	record      RequestAccountingRecord
	startedAt   string
	completedAt string
}

// importedAccountingAttempt is a request_accounting_attempts row of a per-escrow perf file.
type importedAccountingAttempt struct {
	attempt   RequestAccountingAttempt
	probe     int
	winner    int
	createdAt string
}

// accountingRecordReader is what FindAccountingRequest needs from a backend to resolve a cached request.
type accountingRecordReader interface {
	findAccountingRequestDirect(ctx context.Context, requestID, escrowID string) (RequestAccountingRecord, bool, error)
	findAccountingAlias(ctx context.Context, requestID, escrowID string) (requestAccountingAlias, bool, error)
}

// resolveAccountingRequest returns the request's own record or, for a cached request, its source's record under the request's ids.
func resolveAccountingRequest(ctx context.Context, reader accountingRecordReader, requestID, escrowID string) (RequestAccountingRecord, bool, error) {
	if requestID == "" || escrowID == "" {
		return RequestAccountingRecord{}, false, nil
	}
	rec, ok, err := reader.findAccountingRequestDirect(ctx, requestID, escrowID)
	if err != nil || ok {
		return rec, ok, err
	}

	alias, aliasOK, err := reader.findAccountingAlias(ctx, requestID, escrowID)
	if err != nil || !aliasOK {
		return RequestAccountingRecord{}, false, err
	}
	rec, ok, err = reader.findAccountingRequestDirect(ctx, alias.sourceRequestID, alias.sourceEscrowID)
	if err != nil || !ok {
		return RequestAccountingRecord{}, ok, err
	}
	rec.RequestID = requestID
	rec.EscrowID = escrowID
	rec.Outcome = "cached"
	rec.Decision = alias.reason
	rec.CachedFromRequestID = alias.sourceRequestID
	rec.CachedFromEscrowID = alias.sourceEscrowID
	return rec, true, nil
}

func isAccountingAliasStorable(requestID, escrowID, sourceRequestID, sourceEscrowID string) bool {
	if requestID == "" || escrowID == "" || sourceRequestID == "" || sourceEscrowID == "" {
		return false
	}
	return requestID != sourceRequestID || escrowID != sourceEscrowID
}

// importedAccountingPageVisitor receives one page of an escrow's imported accounting rows.
type importedAccountingPageVisitor struct {
	requests func([]importedAccountingRequest) error
	attempts func([]importedAccountingAttempt) error
}

// forEachImportedAccountingPage streams one escrow's accounting from a per-escrow perf file a page at a time; a file without the tables has none.
func forEachImportedAccountingPage(ctx context.Context, sourcePath, escrowID string, visit importedAccountingPageVisitor) (int64, int64, error) {
	sourceDB, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		return 0, 0, fmt.Errorf("open request accounting source: %w", err)
	}
	defer func() { _ = sourceDB.Close() }()
	sourceDB.SetMaxOpenConns(1)

	var requestCount, attemptCount int64
	for afterRowID := int64(0); ; {
		page, lastRowID, err := readImportedAccountingRequests(ctx, sourceDB, escrowID, afterRowID)
		if err != nil {
			return 0, 0, err
		}
		if len(page) > 0 {
			if err := visit.requests(page); err != nil {
				return 0, 0, err
			}
		}
		requestCount += int64(len(page))
		if len(page) < perfImportPageSize {
			break
		}
		afterRowID = lastRowID
	}
	for afterRowID := int64(0); ; {
		page, lastRowID, err := readImportedAccountingAttempts(ctx, sourceDB, escrowID, afterRowID)
		if err != nil {
			return 0, 0, err
		}
		if len(page) > 0 {
			if err := visit.attempts(page); err != nil {
				return 0, 0, err
			}
		}
		attemptCount += int64(len(page))
		if len(page) < perfImportPageSize {
			break
		}
		afterRowID = lastRowID
	}
	return requestCount, attemptCount, nil
}

func readImportedAccountingRequests(ctx context.Context, sourceDB *sql.DB, escrowID string, afterRowID int64) ([]importedAccountingRequest, int64, error) {
	rows, err := sourceDB.QueryContext(ctx,
		`SELECT rowid, request_id, escrow_id, model, started_at, completed_at, outcome, decision, winner_nonce
		 FROM request_accounting
		 WHERE escrow_id = ? AND rowid > ?
		 ORDER BY rowid LIMIT ?`,
		escrowID, afterRowID, perfImportPageSize,
	)
	if err != nil {
		if isSQLiteMissingTable(err) {
			return nil, afterRowID, nil
		}
		return nil, afterRowID, fmt.Errorf("read request accounting source: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var page []importedAccountingRequest
	lastRowID := afterRowID
	for rows.Next() {
		var imported importedAccountingRequest
		rec := &imported.record
		if err := rows.Scan(&lastRowID, &rec.RequestID, &rec.EscrowID, &rec.Model, &imported.startedAt, &imported.completedAt, &rec.Outcome, &rec.Decision, &rec.WinnerNonce); err != nil {
			return nil, afterRowID, err
		}
		page = append(page, imported)
	}
	return page, lastRowID, rows.Err()
}

func readImportedAccountingAttempts(ctx context.Context, sourceDB *sql.DB, escrowID string, afterRowID int64) ([]importedAccountingAttempt, int64, error) {
	rows, err := sourceDB.QueryContext(ctx,
		`SELECT rowid, request_id, escrow_id, nonce, host_idx, participant_key, probe, winner, created_at
		 FROM request_accounting_attempts
		 WHERE escrow_id = ? AND rowid > ?
		 ORDER BY rowid LIMIT ?`,
		escrowID, afterRowID, perfImportPageSize,
	)
	if err != nil {
		if isSQLiteMissingTable(err) {
			return nil, afterRowID, nil
		}
		return nil, afterRowID, fmt.Errorf("read request accounting attempts source: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var page []importedAccountingAttempt
	lastRowID := afterRowID
	for rows.Next() {
		var imported importedAccountingAttempt
		attempt := &imported.attempt
		if err := rows.Scan(&lastRowID, &attempt.RequestID, &attempt.EscrowID, &attempt.Nonce, &attempt.HostIdx, &attempt.ParticipantKey, &imported.probe, &imported.winner, &imported.createdAt); err != nil {
			return nil, afterRowID, err
		}
		page = append(page, imported)
	}
	return page, lastRowID, rows.Err()
}

func isSQLiteMissingTable(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such table")
}

func (s *sqlitePerfStore) UpsertAccountingRequest(ctx context.Context, requestID, escrowID, model string, startedAt time.Time) error {
	if s == nil || requestID == "" || escrowID == "" {
		return nil
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO request_accounting (request_id, escrow_id, model, started_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(request_id, escrow_id) DO UPDATE SET
		   model = CASE WHEN excluded.model <> '' THEN excluded.model ELSE request_accounting.model END`,
		requestID,
		escrowID,
		model,
		startedAt.Format(time.RFC3339Nano),
	)
	return err
}

func (s *sqlitePerfStore) UpsertAccountingAttempt(ctx context.Context, attempt RequestAccountingAttempt) error {
	if s == nil || attempt.RequestID == "" || attempt.EscrowID == "" || attempt.Nonce == 0 {
		return nil
	}
	if attempt.CreatedAt.IsZero() {
		attempt.CreatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO request_accounting_attempts (
		   request_id, escrow_id, nonce, host_idx, participant_key, probe, winner, created_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(request_id, escrow_id, nonce) DO UPDATE SET
		   host_idx = excluded.host_idx,
		   participant_key = excluded.participant_key,
		   probe = excluded.probe,
		   winner = CASE WHEN excluded.winner = 1 THEN 1 ELSE request_accounting_attempts.winner END`,
		attempt.RequestID,
		attempt.EscrowID,
		attempt.Nonce,
		attempt.HostIdx,
		attempt.ParticipantKey,
		boolToInt(attempt.Probe),
		boolToInt(attempt.Winner),
		attempt.CreatedAt.Format(time.RFC3339Nano),
	)
	return err
}

func (s *sqlitePerfStore) CompleteAccountingRequest(ctx context.Context, requestID, escrowID string, winnerNonce uint64, decision, outcome string, completedAt time.Time) error {
	if s == nil || requestID == "" || escrowID == "" {
		return nil
	}
	if completedAt.IsZero() {
		completedAt = time.Now()
	}
	if outcome == "" {
		outcome = "settled"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE request_accounting
		 SET completed_at = ?, outcome = ?, decision = ?, winner_nonce = ?
		 WHERE request_id = ? AND escrow_id = ?`,
		completedAt.Format(time.RFC3339Nano),
		outcome,
		decision,
		winnerNonce,
		requestID,
		escrowID,
	); err != nil {
		return err
	}
	if winnerNonce != 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE request_accounting_attempts
			 SET winner = CASE WHEN nonce = ? THEN 1 ELSE 0 END
			 WHERE request_id = ? AND escrow_id = ?`,
			winnerNonce,
			requestID,
			escrowID,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *sqlitePerfStore) UpsertAccountingAlias(ctx context.Context, requestID, escrowID, sourceRequestID, sourceEscrowID, reason string, createdAt time.Time) error {
	if s == nil || !isAccountingAliasStorable(requestID, escrowID, sourceRequestID, sourceEscrowID) {
		return nil
	}
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO request_accounting_aliases (
		   request_id, escrow_id, source_request_id, source_escrow_id, reason, created_at
		 ) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(request_id, escrow_id) DO UPDATE SET
		   source_request_id = excluded.source_request_id,
		   source_escrow_id = excluded.source_escrow_id,
		   reason = excluded.reason,
		   created_at = excluded.created_at`,
		requestID,
		escrowID,
		sourceRequestID,
		sourceEscrowID,
		reason,
		createdAt.Format(time.RFC3339Nano),
	)
	return err
}

func (s *sqlitePerfStore) ImportRequestAccounting(ctx context.Context, sourcePath, escrowID string) (int64, int64, error) {
	if s == nil || s.db == nil || strings.TrimSpace(sourcePath) == "" || strings.TrimSpace(escrowID) == "" {
		return 0, 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	requests, attempts, err := forEachImportedAccountingPage(ctx, sourcePath, escrowID, importedAccountingPageVisitor{
		requests: func(page []importedAccountingRequest) error {
			for _, imported := range page {
				rec := imported.record
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO request_accounting (request_id, escrow_id, model, started_at, completed_at, outcome, decision, winner_nonce)
					 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
					 ON CONFLICT(request_id, escrow_id) DO UPDATE SET
					   model = excluded.model,
					   started_at = excluded.started_at,
					   completed_at = excluded.completed_at,
					   outcome = excluded.outcome,
					   decision = excluded.decision,
					   winner_nonce = excluded.winner_nonce`,
					rec.RequestID, rec.EscrowID, rec.Model, imported.startedAt, imported.completedAt, rec.Outcome, rec.Decision, rec.WinnerNonce,
				); err != nil {
					return err
				}
			}
			return nil
		},
		attempts: func(page []importedAccountingAttempt) error {
			for _, imported := range page {
				attempt := imported.attempt
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO request_accounting_attempts (
					   request_id, escrow_id, nonce, host_idx, participant_key, probe, winner, created_at
					 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
					 ON CONFLICT(request_id, escrow_id, nonce) DO UPDATE SET
					   host_idx = excluded.host_idx,
					   participant_key = excluded.participant_key,
					   probe = excluded.probe,
					   winner = excluded.winner,
					   created_at = excluded.created_at`,
					attempt.RequestID, attempt.EscrowID, attempt.Nonce, attempt.HostIdx, attempt.ParticipantKey, imported.probe, imported.winner, imported.createdAt,
				); err != nil {
					return err
				}
			}
			return nil
		},
	})
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return requests, attempts, nil
}

func (s *sqlitePerfStore) FindAccountingRequest(ctx context.Context, requestID, escrowID string) (RequestAccountingRecord, bool, error) {
	if s == nil {
		return RequestAccountingRecord{}, false, nil
	}
	return resolveAccountingRequest(ctx, s, requestID, escrowID)
}

func (s *sqlitePerfStore) findAccountingRequestDirect(ctx context.Context, requestID, escrowID string) (RequestAccountingRecord, bool, error) {
	var rec RequestAccountingRecord
	var startedAt, completedAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT request_id, escrow_id, model, started_at, completed_at, outcome, decision, winner_nonce
		 FROM request_accounting
		 WHERE request_id = ? AND escrow_id = ?`,
		requestID,
		escrowID,
	).Scan(&rec.RequestID, &rec.EscrowID, &rec.Model, &startedAt, &completedAt, &rec.Outcome, &rec.Decision, &rec.WinnerNonce)
	if err == sql.ErrNoRows {
		return RequestAccountingRecord{}, false, nil
	}
	if err != nil {
		return RequestAccountingRecord{}, false, err
	}
	rec.StartedAt = strToTime(startedAt)
	rec.CompletedAt = strToTime(completedAt)

	attempts, err := s.findAccountingAttempts(ctx, requestID, escrowID)
	if err != nil {
		return RequestAccountingRecord{}, false, err
	}
	rec.Attempts = attempts
	return rec, true, nil
}

func (s *sqlitePerfStore) findAccountingAlias(ctx context.Context, requestID, escrowID string) (requestAccountingAlias, bool, error) {
	var alias requestAccountingAlias
	err := s.db.QueryRowContext(ctx,
		`SELECT source_request_id, source_escrow_id, reason
		 FROM request_accounting_aliases
		 WHERE request_id = ? AND escrow_id = ?`,
		requestID,
		escrowID,
	).Scan(&alias.sourceRequestID, &alias.sourceEscrowID, &alias.reason)
	if err == sql.ErrNoRows {
		return requestAccountingAlias{}, false, nil
	}
	if err != nil {
		if isSQLiteMissingTable(err) {
			return requestAccountingAlias{}, false, nil
		}
		return requestAccountingAlias{}, false, err
	}
	return alias, true, nil
}

func (s *sqlitePerfStore) findAccountingAttempts(ctx context.Context, requestID, escrowID string) ([]RequestAccountingAttempt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT request_id, escrow_id, nonce, host_idx, participant_key, probe, winner, created_at
		 FROM request_accounting_attempts
		 WHERE request_id = ? AND escrow_id = ?
		 ORDER BY nonce ASC`,
		requestID,
		escrowID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var attempts []RequestAccountingAttempt
	for rows.Next() {
		var attempt RequestAccountingAttempt
		var probe, winner int
		var createdAt string
		if err := rows.Scan(
			&attempt.RequestID,
			&attempt.EscrowID,
			&attempt.Nonce,
			&attempt.HostIdx,
			&attempt.ParticipantKey,
			&probe,
			&winner,
			&createdAt,
		); err != nil {
			return nil, err
		}
		attempt.Probe = probe != 0
		attempt.Winner = winner != 0
		attempt.CreatedAt = strToTime(createdAt)
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return attempts, nil
}

// RecordAccountingRequestStart writes without the request context, so a client that disconnects cannot cancel it.
func (t *PerfTracker) RecordAccountingRequestStart(requestID, escrowID, model string, startedAt time.Time) {
	if t == nil || t.store == nil {
		return
	}
	if err := t.store.UpsertAccountingRequest(context.Background(), requestID, escrowID, model, startedAt); err != nil {
		log.Printf("perf: persist request accounting start: %v", err)
	}
}

func (t *PerfTracker) RecordAccountingAttempt(attempt RequestAccountingAttempt) {
	if t == nil || t.store == nil {
		return
	}
	if err := t.store.UpsertAccountingAttempt(context.Background(), attempt); err != nil {
		log.Printf("perf: persist request accounting attempt: %v", err)
	}
}

func (t *PerfTracker) CompleteAccountingRequest(requestID, escrowID string, winnerNonce uint64, decision, outcome string, completedAt time.Time) {
	if t == nil || t.store == nil {
		return
	}
	if err := t.store.CompleteAccountingRequest(context.Background(), requestID, escrowID, winnerNonce, decision, outcome, completedAt); err != nil {
		log.Printf("perf: persist request accounting completion: %v", err)
	}
}

func (t *PerfTracker) RecordAccountingAlias(requestID, escrowID, sourceRequestID, sourceEscrowID, reason string, createdAt time.Time) {
	if t == nil || t.store == nil {
		return
	}
	if err := t.store.UpsertAccountingAlias(context.Background(), requestID, escrowID, sourceRequestID, sourceEscrowID, reason, createdAt); err != nil {
		log.Printf("perf: persist request accounting alias: %v", err)
	}
}

func (t *PerfTracker) FindAccountingRequest(ctx context.Context, requestID, escrowID string) (RequestAccountingRecord, bool, error) {
	if t == nil || t.store == nil {
		return RequestAccountingRecord{}, false, nil
	}
	return t.store.FindAccountingRequest(ctx, requestID, escrowID)
}
