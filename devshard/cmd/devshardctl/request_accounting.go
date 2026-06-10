package main

import (
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

func (s *PerfStore) UpsertAccountingRequest(requestID, escrowID, model string, startedAt time.Time) error {
	if s == nil || requestID == "" || escrowID == "" {
		return nil
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	_, err := s.db.Exec(
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

func (s *PerfStore) UpsertAccountingAttempt(attempt RequestAccountingAttempt) error {
	if s == nil || attempt.RequestID == "" || attempt.EscrowID == "" || attempt.Nonce == 0 {
		return nil
	}
	if attempt.CreatedAt.IsZero() {
		attempt.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(
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

func (s *PerfStore) CompleteAccountingRequest(requestID, escrowID string, winnerNonce uint64, decision, outcome string, completedAt time.Time) error {
	if s == nil || requestID == "" || escrowID == "" {
		return nil
	}
	if completedAt.IsZero() {
		completedAt = time.Now()
	}
	if outcome == "" {
		outcome = "settled"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
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
		if _, err := tx.Exec(
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

func (s *PerfStore) UpsertAccountingAlias(requestID, escrowID, sourceRequestID, sourceEscrowID, reason string, createdAt time.Time) error {
	if s == nil || requestID == "" || escrowID == "" || sourceRequestID == "" || sourceEscrowID == "" {
		return nil
	}
	if requestID == sourceRequestID && escrowID == sourceEscrowID {
		return nil
	}
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	_, err := s.db.Exec(
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

func (s *PerfStore) ImportRequestAccounting(sourcePath, escrowID string) (int64, int64, error) {
	if s == nil || s.db == nil || strings.TrimSpace(sourcePath) == "" || strings.TrimSpace(escrowID) == "" {
		return 0, 0, nil
	}
	sourceDB, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		return 0, 0, fmt.Errorf("open request accounting source: %w", err)
	}
	defer sourceDB.Close()
	sourceDB.SetMaxOpenConns(1)

	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	records, err := importAccountingRequestsTx(tx, sourceDB, escrowID)
	if err != nil {
		return 0, 0, err
	}
	attempts, err := importAccountingAttemptsTx(tx, sourceDB, escrowID)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return records, attempts, nil
}

func importAccountingRequestsTx(tx *sql.Tx, sourceDB *sql.DB, escrowID string) (int64, error) {
	rows, err := sourceDB.Query(
		`SELECT request_id, escrow_id, model, started_at, completed_at, outcome, decision, winner_nonce
		 FROM request_accounting
		 WHERE escrow_id = ?`,
		escrowID,
	)
	if err != nil {
		if isSQLiteMissingTable(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read request accounting source: %w", err)
	}
	defer rows.Close()

	stmt, err := tx.Prepare(
		`INSERT INTO request_accounting (request_id, escrow_id, model, started_at, completed_at, outcome, decision, winner_nonce)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(request_id, escrow_id) DO UPDATE SET
		   model = excluded.model,
		   started_at = excluded.started_at,
		   completed_at = excluded.completed_at,
		   outcome = excluded.outcome,
		   decision = excluded.decision,
		   winner_nonce = excluded.winner_nonce`,
	)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	var count int64
	for rows.Next() {
		var rec RequestAccountingRecord
		var startedAt, completedAt string
		if err := rows.Scan(&rec.RequestID, &rec.EscrowID, &rec.Model, &startedAt, &completedAt, &rec.Outcome, &rec.Decision, &rec.WinnerNonce); err != nil {
			return 0, err
		}
		if _, err := stmt.Exec(rec.RequestID, rec.EscrowID, rec.Model, startedAt, completedAt, rec.Outcome, rec.Decision, rec.WinnerNonce); err != nil {
			return 0, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

func importAccountingAttemptsTx(tx *sql.Tx, sourceDB *sql.DB, escrowID string) (int64, error) {
	rows, err := sourceDB.Query(
		`SELECT request_id, escrow_id, nonce, host_idx, participant_key, probe, winner, created_at
		 FROM request_accounting_attempts
		 WHERE escrow_id = ?`,
		escrowID,
	)
	if err != nil {
		if isSQLiteMissingTable(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read request accounting attempts source: %w", err)
	}
	defer rows.Close()

	stmt, err := tx.Prepare(
		`INSERT INTO request_accounting_attempts (
		   request_id, escrow_id, nonce, host_idx, participant_key, probe, winner, created_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(request_id, escrow_id, nonce) DO UPDATE SET
		   host_idx = excluded.host_idx,
		   participant_key = excluded.participant_key,
		   probe = excluded.probe,
		   winner = excluded.winner,
		   created_at = excluded.created_at`,
	)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	var count int64
	for rows.Next() {
		var attempt RequestAccountingAttempt
		var probe, winner int
		var createdAt string
		if err := rows.Scan(&attempt.RequestID, &attempt.EscrowID, &attempt.Nonce, &attempt.HostIdx, &attempt.ParticipantKey, &probe, &winner, &createdAt); err != nil {
			return 0, err
		}
		if _, err := stmt.Exec(attempt.RequestID, attempt.EscrowID, attempt.Nonce, attempt.HostIdx, attempt.ParticipantKey, probe, winner, createdAt); err != nil {
			return 0, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

func isSQLiteMissingTable(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such table")
}

func (s *PerfStore) FindAccountingRequest(requestID, escrowID string) (RequestAccountingRecord, bool, error) {
	if s == nil || requestID == "" || escrowID == "" {
		return RequestAccountingRecord{}, false, nil
	}
	rec, ok, err := s.findAccountingRequestDirect(requestID, escrowID)
	if err != nil || ok {
		return rec, ok, err
	}

	alias, aliasOK, err := s.findAccountingAlias(requestID, escrowID)
	if err != nil || !aliasOK {
		return RequestAccountingRecord{}, false, err
	}
	rec, ok, err = s.findAccountingRequestDirect(alias.sourceRequestID, alias.sourceEscrowID)
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

func (s *PerfStore) findAccountingRequestDirect(requestID, escrowID string) (RequestAccountingRecord, bool, error) {
	var rec RequestAccountingRecord
	var startedAt, completedAt string
	err := s.db.QueryRow(
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

	attempts, err := s.findAccountingAttempts(requestID, escrowID)
	if err != nil {
		return RequestAccountingRecord{}, false, err
	}
	rec.Attempts = attempts
	return rec, true, nil
}

type requestAccountingAlias struct {
	sourceRequestID string
	sourceEscrowID  string
	reason          string
}

func (s *PerfStore) findAccountingAlias(requestID, escrowID string) (requestAccountingAlias, bool, error) {
	var alias requestAccountingAlias
	err := s.db.QueryRow(
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

func (s *PerfStore) findAccountingAttempts(requestID, escrowID string) ([]RequestAccountingAttempt, error) {
	rows, err := s.db.Query(
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

func (t *PerfTracker) RecordAccountingRequestStart(requestID, escrowID, model string, startedAt time.Time) {
	if t == nil || t.store == nil {
		return
	}
	if err := t.store.UpsertAccountingRequest(requestID, escrowID, model, startedAt); err != nil {
		log.Printf("perf: persist request accounting start: %v", err)
	}
}

func (t *PerfTracker) RecordAccountingAttempt(attempt RequestAccountingAttempt) {
	if t == nil || t.store == nil {
		return
	}
	if err := t.store.UpsertAccountingAttempt(attempt); err != nil {
		log.Printf("perf: persist request accounting attempt: %v", err)
	}
}

func (t *PerfTracker) CompleteAccountingRequest(requestID, escrowID string, winnerNonce uint64, decision, outcome string, completedAt time.Time) {
	if t == nil || t.store == nil {
		return
	}
	if err := t.store.CompleteAccountingRequest(requestID, escrowID, winnerNonce, decision, outcome, completedAt); err != nil {
		log.Printf("perf: persist request accounting completion: %v", err)
	}
}

func (t *PerfTracker) RecordAccountingAlias(requestID, escrowID, sourceRequestID, sourceEscrowID, reason string, createdAt time.Time) {
	if t == nil || t.store == nil {
		return
	}
	if err := t.store.UpsertAccountingAlias(requestID, escrowID, sourceRequestID, sourceEscrowID, reason, createdAt); err != nil {
		log.Printf("perf: persist request accounting alias: %v", err)
	}
}

func (t *PerfTracker) FindAccountingRequest(requestID, escrowID string) (RequestAccountingRecord, bool, error) {
	if t == nil || t.store == nil {
		return RequestAccountingRecord{}, false, nil
	}
	return t.store.FindAccountingRequest(requestID, escrowID)
}
