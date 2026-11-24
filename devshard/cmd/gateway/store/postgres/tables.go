package postgres

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
	if err := s.database.scanRow(ctx, `SELECT overrides_json FROM config_overrides WHERE id = 1`, nil, &raw); err != nil {
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

// SaveOverrides replaces the persisted overrides.
func (s *Backend) SaveOverrides(ctx context.Context, overrides config.Overrides) error {
	encoded, err := overrides.EncodeOverrides()
	if err != nil {
		return fmt.Errorf("saving overrides: %w", err)
	}
	_, err = s.database.exec(ctx, `
		INSERT INTO config_overrides (id, overrides_json, updated_at) VALUES (1, $1, `+nowExpression+`)
		ON CONFLICT (id) DO UPDATE SET overrides_json = excluded.overrides_json, updated_at = excluded.updated_at`,
		string(encoded))
	if err != nil {
		return fmt.Errorf("saving overrides: %w", err)
	}
	return nil
}

// UpsertModelFilter replaces the model's setting, or adds it.
func (s *Backend) UpsertModelFilter(ctx context.Context, row store.ModelFilterRow) error {
	_, err := s.database.exec(ctx, `
		INSERT INTO model_filters (model, preset, filters_json, updated_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (model) DO UPDATE SET preset = excluded.preset, filters_json = excluded.filters_json, updated_at = excluded.updated_at`,
		row.Model, row.Preset, row.FiltersJSON, row.UpdatedAt)
	if err != nil {
		return fmt.Errorf("saving filter setting for %s: %w", row.Model, err)
	}
	return nil
}

// DeleteModelFilter succeeds when the model had no setting.
func (s *Backend) DeleteModelFilter(ctx context.Context, model string) error {
	if _, err := s.database.exec(ctx, `DELETE FROM model_filters WHERE model = $1`, model); err != nil {
		return fmt.Errorf("deleting filter setting for %s: %w", model, err)
	}
	return nil
}

// ListModelFilters returns every stored setting, ordered by model.
func (s *Backend) ListModelFilters(ctx context.Context) ([]store.ModelFilterRow, error) {
	var settings []store.ModelFilterRow
	err := s.database.forEachRow(ctx, `SELECT model, preset, filters_json, updated_at FROM model_filters ORDER BY model`, nil, func(result *sql.Rows) error {
		var row store.ModelFilterRow
		if err := result.Scan(&row.Model, &row.Preset, &row.FiltersJSON, &row.UpdatedAt); err != nil {
			return fmt.Errorf("scanning filter setting row: %w", err)
		}
		settings = append(settings, row)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing filter settings: %w", err)
	}
	return settings, nil
}

// SaveRotationStatus replaces the latest outcome of the status's (model, role) stage.
func (s *Backend) SaveRotationStatus(ctx context.Context, status store.RotationStatus) error {
	_, err := s.database.exec(ctx, `
		INSERT INTO gateway_rotation_status (model, role, stage, epoch, completed, create_error, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (model, role) DO UPDATE SET
			stage = excluded.stage,
			epoch = excluded.epoch,
			completed = excluded.completed,
			create_error = excluded.create_error,
			updated_at = excluded.updated_at`,
		status.Model, string(status.Role), status.Stage, status.Epoch, flag(status.Completed), status.CreateError,
		store.FormatTime(status.UpdatedAt))
	if err != nil {
		return fmt.Errorf("saving rotation status %s/%s: %w", status.Model, status.Role, err)
	}
	return nil
}

// LoadRotationStatuses returns every status ordered by (model, role).
func (s *Backend) LoadRotationStatuses(ctx context.Context) ([]store.RotationStatus, error) {
	var statuses []store.RotationStatus
	err := s.database.forEachRow(ctx, `
		SELECT model, role, stage, epoch, completed, create_error, updated_at
		FROM gateway_rotation_status ORDER BY model, role`, nil, func(result *sql.Rows) error {
		var status store.RotationStatus
		var updatedAt string
		if err := result.Scan(&status.Model, &status.Role, &status.Stage, &status.Epoch, &status.Completed, &status.CreateError, &updatedAt); err != nil {
			return fmt.Errorf("scanning rotation status row: %w", err)
		}
		parsed, err := store.ParseTime(updatedAt)
		if err != nil {
			return fmt.Errorf("parsing rotation status updated_at %q: %w", updatedAt, err)
		}
		status.UpdatedAt = parsed
		statuses = append(statuses, status)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("loading rotation statuses: %w", err)
	}
	return statuses, nil
}

// AddSuspiciousHost pins a participant as never-to-be-trusted; re-pinning keeps the original timestamp.
func (s *Backend) AddSuspiciousHost(ctx context.Context, participantKey string) error {
	_, err := s.database.exec(ctx, `
		INSERT INTO suspicious_hosts (participant_key) VALUES ($1)
		ON CONFLICT (participant_key) DO NOTHING`, participantKey)
	if err != nil {
		return fmt.Errorf("pinning suspicious host %s: %w", participantKey, err)
	}
	return nil
}

// RemoveSuspiciousHost unpins a participant, and succeeds when it was never pinned.
func (s *Backend) RemoveSuspiciousHost(ctx context.Context, participantKey string) error {
	if _, err := s.database.exec(ctx, `DELETE FROM suspicious_hosts WHERE participant_key = $1`, participantKey); err != nil {
		return fmt.Errorf("unpinning suspicious host %s: %w", participantKey, err)
	}
	return nil
}

// ListSuspiciousHosts returns every pinned participant key, ordered by key.
func (s *Backend) ListSuspiciousHosts(ctx context.Context) ([]string, error) {
	var participantKeys []string
	err := s.database.forEachRow(ctx, `SELECT participant_key FROM suspicious_hosts ORDER BY participant_key`, nil, func(result *sql.Rows) error {
		var participantKey string
		if err := result.Scan(&participantKey); err != nil {
			return fmt.Errorf("scanning suspicious host row: %w", err)
		}
		participantKeys = append(participantKeys, participantKey)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing suspicious hosts: %w", err)
	}
	return participantKeys, nil
}
