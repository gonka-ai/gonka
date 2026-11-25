package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"devshard/cmd/gateway/config"
)

// LoadOverrides returns the persisted admin overrides, or the zero value when none were ever saved.
func (s *Store) LoadOverrides(ctx context.Context) (config.Overrides, error) {
	var raw string
	row := s.db.QueryRowContext(ctx, `SELECT overrides_json FROM config_overrides WHERE id = 1`)
	if err := row.Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return config.Overrides{}, nil
		}
		return config.Overrides{}, fmt.Errorf("loading overrides: %w", err)
	}
	overrides, err := config.ParseStoredOverrides([]byte(raw))
	if err != nil {
		return config.Overrides{}, fmt.Errorf("loading overrides: %w", err)
	}
	return overrides, nil
}

func (s *Store) SaveOverrides(ctx context.Context, overrides config.Overrides) error {
	encoded, err := overrides.EncodeOverrides()
	if err != nil {
		return fmt.Errorf("saving overrides: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO config_overrides (id, overrides_json, updated_at) VALUES (1, ?, datetime('now'))
		ON CONFLICT(id) DO UPDATE SET overrides_json = excluded.overrides_json, updated_at = excluded.updated_at`,
		string(encoded))
	if err != nil {
		return fmt.Errorf("saving overrides: %w", err)
	}
	return nil
}
