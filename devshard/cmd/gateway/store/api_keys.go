package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrAPIKeyIDTaken reports an insert whose ID an earlier key already holds, revoked or not.
var ErrAPIKeyIDTaken = errors.New("an API key with this ID already exists")

const (
	everyAPIKey       = `WHERE 1 = 1`
	activeAPIKeyCheck = `revoked_at = ''`
)

// APIKeyRecord is one client key as stored; RevokedAt is the zero time while the key is active.
type APIKeyRecord struct {
	ID        string
	Key       string
	Label     string
	CreatedAt time.Time
	RevokedAt time.Time
}

// Revoked reports whether the key was revoked.
func (record APIKeyRecord) Revoked() bool {
	return !record.RevokedAt.IsZero()
}

// InsertAPIKey adds a key, and fails with ErrAPIKeyIDTaken when the ID is in use.
func (s *Store) InsertAPIKey(ctx context.Context, record APIKeyRecord) error {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO api_keys (id, key, label, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		record.ID, record.Key, record.Label, FormatTime(record.CreatedAt))
	if err != nil {
		return fmt.Errorf("saving API key %s: %w", record.ID, err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("saving API key %s: %w", record.ID, err)
	}
	if inserted == 0 {
		return fmt.Errorf("%w: %s", ErrAPIKeyIDTaken, record.ID)
	}
	return nil
}

// RevokeAPIKey marks a key revoked at the given time and keeps the row, so its ID is never reused; an already revoked key keeps its first time.
func (s *Store) RevokeAPIKey(ctx context.Context, id string, revokedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET revoked_at = ? WHERE id = ? AND `+activeAPIKeyCheck, FormatTime(revokedAt), id)
	if err != nil {
		return fmt.Errorf("revoking API key %s: %w", id, err)
	}
	return nil
}

// APIKey returns one key, found false when no row has the ID.
func (s *Store) APIKey(ctx context.Context, id string) (APIKeyRecord, bool, error) {
	records, err := s.queryAPIKeys(ctx, `WHERE id = ?`, id)
	if err != nil || len(records) == 0 {
		return APIKeyRecord{}, false, err
	}
	return records[0], true, nil
}

// ListAPIKeys returns every key, revoked ones included, oldest first.
func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKeyRecord, error) {
	return s.queryAPIKeys(ctx, everyAPIKey)
}

// ActiveAPIKeys returns the values of the keys not revoked, oldest first.
func (s *Store) ActiveAPIKeys(ctx context.Context) ([]string, error) {
	records, err := s.queryAPIKeys(ctx, `WHERE `+activeAPIKeyCheck)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(records))
	for _, record := range records {
		keys = append(keys, record.Key)
	}
	return keys, nil
}

func (s *Store) queryAPIKeys(ctx context.Context, condition string, arguments ...any) ([]APIKeyRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, key, label, created_at, revoked_at FROM api_keys `+condition+` ORDER BY created_at, id`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("reading API keys: %w", err)
	}
	defer rows.Close()
	var records []APIKeyRecord
	for rows.Next() {
		var record APIKeyRecord
		var createdAt, revokedAt string
		if err := rows.Scan(&record.ID, &record.Key, &record.Label, &createdAt, &revokedAt); err != nil {
			return nil, fmt.Errorf("scanning API key row: %w", err)
		}
		if record.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("API key %s created_at: %w", record.ID, err)
		}
		if record.RevokedAt, err = parseTime(revokedAt); err != nil {
			return nil, fmt.Errorf("API key %s revoked_at: %w", record.ID, err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating API keys: %w", err)
	}
	return records, nil
}
