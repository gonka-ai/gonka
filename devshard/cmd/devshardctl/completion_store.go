package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"

	"devshard/user"
)

var _ user.InferenceCompletionStore = (*GatewayStore)(nil)

func (s *GatewayStore) SaveInferenceCompletion(escrowID string, entry user.InferenceCompletion) error {
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

func (s *GatewayStore) ListInferenceCompletions(escrowID string) ([]user.InferenceCompletion, error) {
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
		var entry user.InferenceCompletion
		if err := json.Unmarshal(data, &entry); err != nil {
			return nil, err
		}
		if entry.Nonce != nonce || entry.PreparedAt <= 0 {
			return nil, fmt.Errorf("invalid completion record for escrow %s nonce %d", escrowID, nonce)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *GatewayStore) DeleteInferenceCompletion(escrowID string, nonce uint64) error {
	_, err := s.db.Exec(`DELETE FROM gateway_inference_completions WHERE escrow_id = ? AND nonce = ?`, escrowID, nonce)
	return err
}

func (s *GatewayStore) HasInferenceCompletion(escrowID string, nonce uint64) (bool, error) {
	var present int
	err := s.db.QueryRow(`SELECT 1 FROM gateway_inference_completions WHERE escrow_id = ? AND nonce = ?`, escrowID, nonce).Scan(&present)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}
