package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"devshard/user"
)

var (
	_ user.InferenceCompletionStore = (*SQLiteGatewayStore)(nil)
	_ user.InferenceCompletionStore = (*PostgresGatewayStore)(nil)
)

func checkCompletionConflict(escrowID string, entry user.InferenceCompletion, existing []byte) error {
	var previous user.InferenceCompletion
	if err := json.Unmarshal(existing, &previous); err != nil {
		return err
	}
	want, _ := json.Marshal(entry.Payload)
	got, _ := json.Marshal(previous.Payload)
	if !bytes.Equal(want, got) {
		return fmt.Errorf("%w: escrow %s nonce %d", user.ErrInferenceCompletionConflict, escrowID, entry.Nonce)
	}
	return nil
}

func decodeCompletion(escrowID string, nonce uint64, data []byte) (user.InferenceCompletion, error) {
	var entry user.InferenceCompletion
	if err := json.Unmarshal(data, &entry); err != nil {
		return entry, err
	}
	if entry.Nonce != nonce || entry.PreparedAt <= 0 {
		return entry, fmt.Errorf("invalid completion record for escrow %s nonce %d", escrowID, nonce)
	}
	return entry, nil
}

func (s *SQLiteGatewayStore) SaveInferenceCompletion(escrowID string, entry user.InferenceCompletion) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO gateway_inference_completions (escrow_id, nonce, entry_json)
		VALUES (?, ?, ?) ON CONFLICT (escrow_id, nonce) DO NOTHING`, escrowID, entry.Nonce, data); err != nil {
		return err
	}
	var existing []byte
	if err := s.db.QueryRow(`SELECT entry_json FROM gateway_inference_completions WHERE escrow_id = ? AND nonce = ?`, escrowID, entry.Nonce).Scan(&existing); err != nil {
		return err
	}
	return checkCompletionConflict(escrowID, entry, existing)
}

func (s *SQLiteGatewayStore) ListInferenceCompletions(escrowID string) ([]user.InferenceCompletion, error) {
	rows, err := s.db.Query(`SELECT nonce, entry_json FROM gateway_inference_completions WHERE escrow_id = ? ORDER BY nonce`, escrowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []user.InferenceCompletion
	for rows.Next() {
		var nonce uint64
		var data []byte
		if err := rows.Scan(&nonce, &data); err != nil {
			return nil, err
		}
		entry, err := decodeCompletion(escrowID, nonce, data)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *SQLiteGatewayStore) AllInferenceCompletions() (map[string][]user.InferenceCompletion, error) {
	rows, err := s.db.Query(`SELECT escrow_id, nonce, entry_json FROM gateway_inference_completions ORDER BY escrow_id, nonce`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]user.InferenceCompletion)
	for rows.Next() {
		var escrowID string
		var nonce uint64
		var data []byte
		if err := rows.Scan(&escrowID, &nonce, &data); err != nil {
			return nil, err
		}
		entry, err := decodeCompletion(escrowID, nonce, data)
		if err != nil {
			return nil, err
		}
		out[escrowID] = append(out[escrowID], entry)
	}
	return out, rows.Err()
}

func applyGatewayCompletionToPG(ctx context.Context, exec gatewayMigrationExecer, escrowID string, entry user.InferenceCompletion) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, `INSERT INTO gateway_inference_completions (escrow_id, nonce, entry_json)
		VALUES ($1, $2, $3) ON CONFLICT (escrow_id, nonce) DO NOTHING`, escrowID, int64(entry.Nonce), data)
	return err
}

func (s *SQLiteGatewayStore) DeleteInferenceCompletion(escrowID string, nonce uint64) error {
	_, err := s.db.Exec(`DELETE FROM gateway_inference_completions WHERE escrow_id = ? AND nonce = ?`, escrowID, nonce)
	return err
}

func (s *SQLiteGatewayStore) HasInferenceCompletion(escrowID string, nonce uint64) (bool, error) {
	var present int
	err := s.db.QueryRow(`SELECT 1 FROM gateway_inference_completions WHERE escrow_id = ? AND nonce = ?`, escrowID, nonce).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *PostgresGatewayStore) SaveInferenceCompletion(escrowID string, entry user.InferenceCompletion) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	ctx, cancel := s.opCtx(context.Background())
	defer cancel()
	if _, err := s.pool.Exec(ctx, `INSERT INTO gateway_inference_completions (escrow_id, nonce, entry_json)
		VALUES ($1, $2, $3) ON CONFLICT (escrow_id, nonce) DO NOTHING`, escrowID, int64(entry.Nonce), data); err != nil {
		return err
	}
	var existing []byte
	if err := s.pool.QueryRow(ctx, `SELECT entry_json FROM gateway_inference_completions WHERE escrow_id = $1 AND nonce = $2`, escrowID, int64(entry.Nonce)).Scan(&existing); err != nil {
		return err
	}
	return checkCompletionConflict(escrowID, entry, existing)
}

func (s *PostgresGatewayStore) ListInferenceCompletions(escrowID string) ([]user.InferenceCompletion, error) {
	ctx, cancel := s.opCtx(context.Background())
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT nonce, entry_json FROM gateway_inference_completions WHERE escrow_id = $1 ORDER BY nonce`, escrowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []user.InferenceCompletion
	for rows.Next() {
		var nonce int64
		var data []byte
		if err := rows.Scan(&nonce, &data); err != nil {
			return nil, err
		}
		entry, err := decodeCompletion(escrowID, uint64(nonce), data)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *PostgresGatewayStore) DeleteInferenceCompletion(escrowID string, nonce uint64) error {
	ctx, cancel := s.opCtx(context.Background())
	defer cancel()
	_, err := s.pool.Exec(ctx, `DELETE FROM gateway_inference_completions WHERE escrow_id = $1 AND nonce = $2`, escrowID, int64(nonce))
	return err
}

func (s *PostgresGatewayStore) HasInferenceCompletion(escrowID string, nonce uint64) (bool, error) {
	ctx, cancel := s.opCtx(context.Background())
	defer cancel()
	var present int
	err := s.pool.QueryRow(ctx, `SELECT 1 FROM gateway_inference_completions WHERE escrow_id = $1 AND nonce = $2`, escrowID, int64(nonce)).Scan(&present)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
