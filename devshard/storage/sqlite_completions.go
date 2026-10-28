package storage

import (
	"database/sql"
	"errors"
	"fmt"

	"devshard/types"
)

// ErrCompletionConflict is returned when a journal row for a nonce that already has a diff records another
// obligation than the one being written.
var ErrCompletionConflict = errors.New("completion journal conflict")

// CompletionEntry is one row of an escrow's completion journal: the gateway's encoded obligation for the Start at
// Nonce. Storage keeps Data as given.
type CompletionEntry struct {
	Nonce uint64
	Data  []byte
}

// CompletionWrite is the journal side of a diff write.
type CompletionWrite struct {
	// Add, when set, is the obligation of the Start this diff carries.
	Add *CompletionEntry
	// SameAdd reports whether a stored row for Add.Nonce records the same obligation as Add.
	SameAdd func(stored []byte) bool
	// Resolved are the nonces whose Finish or Timeout this diff applies.
	Resolved []uint64
}

// CompletionJournal keeps the gateway's completion journal in the session storage, next to the diffs it guards, so
// a Start and its obligation are written by one commit.
type CompletionJournal interface {
	// SaveCompletion stores entry unless the nonce already has a row, and returns the stored row's data.
	SaveCompletion(escrowID string, entry CompletionEntry) ([]byte, error)
	ListCompletions(escrowID string) ([]CompletionEntry, error)
	DeleteCompletion(escrowID string, nonce uint64) error
	HasCompletion(escrowID string, nonce uint64) (bool, error)
	// AppendDiffWithCompletions is AppendDiff that also applies w in the same transaction. A stored row for
	// w.Add.Nonce is replaced while that nonce has no diff (nothing was dispatched under it); once the nonce has a
	// diff, a row SameAdd rejects fails the write with ErrCompletionConflict.
	AppendDiffWithCompletions(escrowID string, rec types.DiffRecord, w CompletionWrite) error
}

var _ CompletionJournal = (*SQLite)(nil)

func (s *SQLite) SaveCompletion(escrowID string, entry CompletionEntry) ([]byte, error) {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return nil, err
	}
	if _, err := p.writeDB.Exec(`INSERT INTO inference_completions (escrow_id, nonce, entry_json) VALUES (?, ?, ?)
		ON CONFLICT (escrow_id, nonce) DO NOTHING`, escrowID, entry.Nonce, entry.Data); err != nil {
		return nil, err
	}
	var stored []byte
	err = p.writeDB.QueryRow(`SELECT entry_json FROM inference_completions WHERE escrow_id = ? AND nonce = ?`,
		escrowID, entry.Nonce).Scan(&stored)
	return stored, err
}

func (s *SQLite) ListCompletions(escrowID string) ([]CompletionEntry, error) {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return nil, err
	}
	rows, err := p.readDB.Query(`SELECT nonce, entry_json FROM inference_completions WHERE escrow_id = ? ORDER BY nonce`, escrowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []CompletionEntry
	for rows.Next() {
		var entry CompletionEntry
		if err := rows.Scan(&entry.Nonce, &entry.Data); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *SQLite) DeleteCompletion(escrowID string, nonce uint64) error {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return err
	}
	_, err = p.writeDB.Exec(`DELETE FROM inference_completions WHERE escrow_id = ? AND nonce = ?`, escrowID, nonce)
	return err
}

func (s *SQLite) HasCompletion(escrowID string, nonce uint64) (bool, error) {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return false, err
	}
	var present int
	err = p.readDB.QueryRow(`SELECT 1 FROM inference_completions WHERE escrow_id = ? AND nonce = ?`, escrowID, nonce).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *SQLite) AppendDiffWithCompletions(escrowID string, rec types.DiffRecord, w CompletionWrite) error {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return err
	}
	tx, err := p.writeDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if w.Add != nil {
		if err := putCompletionSQLiteTx(tx, escrowID, *w.Add, w.SameAdd); err != nil {
			return err
		}
	}
	if err := appendDiffSQLiteTx(tx, escrowID, rec); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE sessions SET latest_nonce = MAX(latest_nonce, ?) WHERE escrow_id = ?`, rec.Nonce, escrowID); err != nil {
		return fmt.Errorf("update latest_nonce: %w", err)
	}
	for _, nonce := range w.Resolved {
		if _, err := tx.Exec(`DELETE FROM inference_completions WHERE escrow_id = ? AND nonce = ?`, escrowID, nonce); err != nil {
			return fmt.Errorf("delete resolved completion %d: %w", nonce, err)
		}
	}
	return tx.Commit()
}

func putCompletionSQLiteTx(tx *sql.Tx, escrowID string, entry CompletionEntry, same func([]byte) bool) error {
	var stored []byte
	err := tx.QueryRow(`SELECT entry_json FROM inference_completions WHERE escrow_id = ? AND nonce = ?`,
		escrowID, entry.Nonce).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.Exec(`INSERT INTO inference_completions (escrow_id, nonce, entry_json) VALUES (?, ?, ?)`,
			escrowID, entry.Nonce, entry.Data)
		return err
	}
	if err != nil {
		return err
	}
	var dispatched int
	err = tx.QueryRow(`SELECT 1 FROM diffs WHERE escrow_id = ? AND nonce = ?`, escrowID, entry.Nonce).Scan(&dispatched)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.Exec(`UPDATE inference_completions SET entry_json = ? WHERE escrow_id = ? AND nonce = ?`,
			entry.Data, escrowID, entry.Nonce)
		return err
	}
	if err != nil {
		return err
	}
	if same == nil || !same(stored) {
		return fmt.Errorf("%w: escrow %s nonce %d", ErrCompletionConflict, escrowID, entry.Nonce)
	}
	return nil
}
