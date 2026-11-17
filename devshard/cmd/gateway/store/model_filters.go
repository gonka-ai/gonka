package store

import (
	"context"
	"fmt"
)

// ModelFilterRow is one operator filter setting; the store keeps its knobs as the JSON the API decoded.
type ModelFilterRow struct {
	Model       string
	Preset      string
	FiltersJSON string
	UpdatedAt   string
}

// UpsertModelFilter replaces the model's setting, or adds it.
func (s *Store) UpsertModelFilter(ctx context.Context, row ModelFilterRow) error {
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
func (s *Store) DeleteModelFilter(ctx context.Context, model string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM model_filters WHERE model = ?`, model); err != nil {
		return fmt.Errorf("deleting filter setting for %s: %w", model, err)
	}
	return nil
}

// ListModelFilters returns every stored setting, ordered by model.
func (s *Store) ListModelFilters(ctx context.Context) ([]ModelFilterRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT model, preset, filters_json, updated_at FROM model_filters ORDER BY model`)
	if err != nil {
		return nil, fmt.Errorf("listing filter settings: %w", err)
	}
	defer rows.Close()
	var settings []ModelFilterRow
	for rows.Next() {
		var row ModelFilterRow
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
