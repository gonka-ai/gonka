package sqlite

import (
	"context"
	"fmt"
	"time"

	"devshard/cmd/gateway/store"
)

const (
	everyAPIKey       = `WHERE 1 = 1`
	activeAPIKeyCheck = `revoked_at = ''`
)

// InsertAPIKey adds a key, and fails with store.ErrAPIKeyIDTaken when the ID is in use.
func (s *Backend) InsertAPIKey(ctx context.Context, record store.APIKeyRecord) error {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO api_keys (id, key, label, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		record.ID, record.Key, record.Label, store.FormatTime(record.CreatedAt))
	if err != nil {
		return fmt.Errorf("saving API key %s: %w", record.ID, err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("saving API key %s: %w", record.ID, err)
	}
	if inserted == 0 {
		return fmt.Errorf("%w: %s", store.ErrAPIKeyIDTaken, record.ID)
	}
	return nil
}

// RevokeAPIKey marks a key revoked at the given time and keeps the row, so its ID is never reused; an already revoked key keeps its first time.
func (s *Backend) RevokeAPIKey(ctx context.Context, id string, revokedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET revoked_at = ? WHERE id = ? AND `+activeAPIKeyCheck, store.FormatTime(revokedAt), id)
	if err != nil {
		return fmt.Errorf("revoking API key %s: %w", id, err)
	}
	return nil
}

// APIKey returns one key, found false when no row has the ID.
func (s *Backend) APIKey(ctx context.Context, id string) (store.APIKeyRecord, bool, error) {
	records, err := s.queryAPIKeys(ctx, `WHERE id = ?`, id)
	if err != nil || len(records) == 0 {
		return store.APIKeyRecord{}, false, err
	}
	return records[0], true, nil
}

// ListAPIKeys returns every key, revoked ones included, oldest first.
func (s *Backend) ListAPIKeys(ctx context.Context) ([]store.APIKeyRecord, error) {
	return s.queryAPIKeys(ctx, everyAPIKey)
}

// ActiveAPIKeys returns the values of the keys not revoked, oldest first.
func (s *Backend) ActiveAPIKeys(ctx context.Context) ([]string, error) {
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

func (s *Backend) queryAPIKeys(ctx context.Context, condition string, arguments ...any) ([]store.APIKeyRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, key, label, created_at, revoked_at FROM api_keys `+condition+` ORDER BY created_at, id`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("reading API keys: %w", err)
	}
	defer rows.Close()
	var records []store.APIKeyRecord
	for rows.Next() {
		var record store.APIKeyRecord
		var createdAt, revokedAt string
		if err := rows.Scan(&record.ID, &record.Key, &record.Label, &createdAt, &revokedAt); err != nil {
			return nil, fmt.Errorf("scanning API key row: %w", err)
		}
		if record.CreatedAt, err = store.ParseTime(createdAt); err != nil {
			return nil, fmt.Errorf("API key %s created_at: %w", record.ID, err)
		}
		if record.RevokedAt, err = store.ParseTime(revokedAt); err != nil {
			return nil, fmt.Errorf("API key %s revoked_at: %w", record.ID, err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating API keys: %w", err)
	}
	return records, nil
}
