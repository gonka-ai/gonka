package store

import (
	"context"
	"fmt"
)

// AddSuspiciousHost pins a participant as never-to-be-trusted; re-pinning keeps the original timestamp.
func (s *Store) AddSuspiciousHost(ctx context.Context, participantKey string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO suspicious_hosts (participant_key) VALUES (?)
		ON CONFLICT(participant_key) DO NOTHING`, participantKey)
	if err != nil {
		return fmt.Errorf("pinning suspicious host %s: %w", participantKey, err)
	}
	return nil
}

// RemoveSuspiciousHost unpins a participant, and succeeds when it was never pinned.
func (s *Store) RemoveSuspiciousHost(ctx context.Context, participantKey string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM suspicious_hosts WHERE participant_key = ?`, participantKey); err != nil {
		return fmt.Errorf("unpinning suspicious host %s: %w", participantKey, err)
	}
	return nil
}

// ListSuspiciousHosts returns every pinned participant key, ordered by key.
func (s *Store) ListSuspiciousHosts(ctx context.Context) ([]string, error) {
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
