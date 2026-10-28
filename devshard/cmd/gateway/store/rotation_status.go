package store

import (
	"context"
	"fmt"
	"time"
)

// RotationStatus is the latest outcome of one (Model, Role) rotation stage, kept for admin-debug visibility only.
type RotationStatus struct {
	Model       string    `json:"model"`
	Role        string    `json:"role"`
	Stage       string    `json:"stage"`
	Epoch       uint64    `json:"epoch"`
	Completed   bool      `json:"completed"`
	CreateError string    `json:"create_error"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (s *Store) SaveRotationStatus(ctx context.Context, st RotationStatus) error {
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
		FormatTime(st.UpdatedAt))
	if err != nil {
		return fmt.Errorf("saving rotation status %s/%s: %w", st.Model, st.Role, err)
	}
	return nil
}

// LoadRotationStatuses returns every status ordered by (model, role).
func (s *Store) LoadRotationStatuses(ctx context.Context) ([]RotationStatus, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT model, role, stage, epoch, completed, create_error, updated_at
		FROM gateway_rotation_status ORDER BY model, role`)
	if err != nil {
		return nil, fmt.Errorf("loading rotation statuses: %w", err)
	}
	defer rows.Close()
	var statuses []RotationStatus
	for rows.Next() {
		var st RotationStatus
		var updatedAt string
		if err := rows.Scan(&st.Model, &st.Role, &st.Stage, &st.Epoch, &st.Completed, &st.CreateError, &updatedAt); err != nil {
			return nil, fmt.Errorf("scanning rotation status row: %w", err)
		}
		parsedUpdatedAt, err := parseTime(updatedAt)
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
