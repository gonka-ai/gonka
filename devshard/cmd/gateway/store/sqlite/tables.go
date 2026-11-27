package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/store"
)

// LoadOverrides returns the persisted admin overrides, or the zero value when none were ever saved.
func (s *Backend) LoadOverrides(ctx context.Context) (config.Overrides, error) {
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

func (s *Backend) SaveOverrides(ctx context.Context, overrides config.Overrides) error {
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

// UpsertModelFilter replaces the model's setting, or adds it.
func (s *Backend) UpsertModelFilter(ctx context.Context, row store.ModelFilterRow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO model_filters (model, preset, filters_json, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(model) DO UPDATE SET preset = excluded.preset, filters_json = excluded.filters_json, updated_at = excluded.updated_at`,
		row.Model, row.Preset, row.FiltersJSON, row.UpdatedAt)
	if err != nil {
		return fmt.Errorf("saving filter setting for %s: %w", row.Model, err)
	}
	return nil
}

// DeleteModelFilter succeeds when the model had no setting.
func (s *Backend) DeleteModelFilter(ctx context.Context, model string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM model_filters WHERE model = ?`, model); err != nil {
		return fmt.Errorf("deleting filter setting for %s: %w", model, err)
	}
	return nil
}

// ListModelFilters returns every stored setting, ordered by model.
func (s *Backend) ListModelFilters(ctx context.Context) ([]store.ModelFilterRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT model, preset, filters_json, updated_at FROM model_filters ORDER BY model`)
	if err != nil {
		return nil, fmt.Errorf("listing filter settings: %w", err)
	}
	defer rows.Close()
	var settings []store.ModelFilterRow
	for rows.Next() {
		var row store.ModelFilterRow
		if err := rows.Scan(&row.Model, &row.Preset, &row.FiltersJSON, &row.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning filter setting row: %w", err)
		}
		settings = append(settings, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating filter settings: %w", err)
	}
	return settings, nil
}

func (s *Backend) SaveRotationStatus(ctx context.Context, st store.RotationStatus) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO gateway_rotation_status (model, role, stage, epoch, completed, create_error, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(model, role) DO UPDATE SET
			stage = excluded.stage,
			epoch = excluded.epoch,
			completed = excluded.completed,
			create_error = excluded.create_error,
			updated_at = excluded.updated_at`,
		st.Model, st.Role, st.Stage, st.Epoch, st.Completed, st.CreateError,
		store.FormatTime(st.UpdatedAt))
	if err != nil {
		return fmt.Errorf("saving rotation status %s/%s: %w", st.Model, st.Role, err)
	}
	return nil
}

// LoadRotationStatuses returns every status ordered by (model, role).
func (s *Backend) LoadRotationStatuses(ctx context.Context) ([]store.RotationStatus, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT model, role, stage, epoch, completed, create_error, updated_at
		FROM gateway_rotation_status ORDER BY model, role`)
	if err != nil {
		return nil, fmt.Errorf("loading rotation statuses: %w", err)
	}
	defer rows.Close()
	var statuses []store.RotationStatus
	for rows.Next() {
		var st store.RotationStatus
		var updatedAt string
		if err := rows.Scan(&st.Model, &st.Role, &st.Stage, &st.Epoch, &st.Completed, &st.CreateError, &updatedAt); err != nil {
			return nil, fmt.Errorf("scanning rotation status row: %w", err)
		}
		parsedUpdatedAt, err := store.ParseTime(updatedAt)
		if err != nil {
			return nil, fmt.Errorf("parsing rotation status updated_at %q: %w", updatedAt, err)
		}
		st.UpdatedAt = parsedUpdatedAt
		statuses = append(statuses, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rotation statuses: %w", err)
	}
	return statuses, nil
}

// AddSuspiciousHost pins a participant as never-to-be-trusted; re-pinning keeps the original timestamp.
func (s *Backend) AddSuspiciousHost(ctx context.Context, participantKey string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO suspicious_hosts (participant_key) VALUES (?)
		ON CONFLICT(participant_key) DO NOTHING`, participantKey)
	if err != nil {
		return fmt.Errorf("pinning suspicious host %s: %w", participantKey, err)
	}
	return nil
}

// RemoveSuspiciousHost unpins a participant, and succeeds when it was never pinned.
func (s *Backend) RemoveSuspiciousHost(ctx context.Context, participantKey string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM suspicious_hosts WHERE participant_key = ?`, participantKey); err != nil {
		return fmt.Errorf("unpinning suspicious host %s: %w", participantKey, err)
	}
	return nil
}

// ListSuspiciousHosts returns every pinned participant key, ordered by key.
func (s *Backend) ListSuspiciousHosts(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT participant_key FROM suspicious_hosts ORDER BY participant_key`)
	if err != nil {
		return nil, fmt.Errorf("listing suspicious hosts: %w", err)
	}
	defer rows.Close()
	var participantKeys []string
	for rows.Next() {
		var participantKey string
		if err := rows.Scan(&participantKey); err != nil {
			return nil, fmt.Errorf("scanning suspicious host row: %w", err)
		}
		participantKeys = append(participantKeys, participantKey)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating suspicious hosts: %w", err)
	}
	return participantKeys, nil
}
