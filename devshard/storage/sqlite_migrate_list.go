package storage

import (
	"database/sql"
	"fmt"
)

// ValidationObsRow is one live or sealed validation-obs counter row.
type ValidationObsRow struct {
	InferenceID uint64
	SlotID      uint32
	Required    uint32
	Completed   uint32
}

// validationObsImporter copies obs rows with exact counters (HA migrate).
type validationObsImporter interface {
	ImportValidationObs(escrowID string, live, sealed []ValidationObsRow) error
}

func (s *SQLite) listSealedInferences(escrowID string) ([]InferenceRow, error) {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return nil, err
	}
	rows, err := p.readDB.Query(
		`SELECT inference_id, sealed_nonce, obs_present, sealed_status, sealed_executor_slot,
		        sealed_votes_valid, sealed_votes_invalid, sealed_validated_by,
		        sealed_model, sealed_prompt_hash, sealed_response_hash,
		        sealed_input_length, sealed_max_tokens,
		        sealed_input_tokens, sealed_output_tokens,
		        sealed_reserved_cost, sealed_actual_cost,
		        sealed_started_at, sealed_confirmed_at
		   FROM sealed_inferences
		  WHERE escrow_id = ?
		  ORDER BY inference_id`,
		escrowID,
	)
	if err != nil {
		return nil, fmt.Errorf("list sealed inferences: %w", err)
	}
	defer rows.Close()

	var out []InferenceRow
	for rows.Next() {
		var row InferenceRow
		var obsPresent int
		if err := rows.Scan(
			&row.InferenceID, &row.SealedNonce, &obsPresent, &row.SealedStatus, &row.SealedExecutorSlot,
			&row.SealedVotesValid, &row.SealedVotesInvalid, &row.SealedValidatedBy,
			&row.SealedModel, &row.SealedPromptHash, &row.SealedResponseHash,
			&row.SealedInputLength, &row.SealedMaxTokens,
			&row.SealedInputTokens, &row.SealedOutputTokens,
			&row.SealedReservedCost, &row.SealedActualCost,
			&row.SealedStartedAt, &row.SealedConfirmedAt,
		); err != nil {
			return nil, err
		}
		row.ObsPresent = obsPresent != 0
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *SQLite) listValidationObs(escrowID, table string) ([]ValidationObsRow, error) {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return nil, err
	}
	// table is a fixed identifier from callers, not user input.
	query := fmt.Sprintf(
		`SELECT inference_id, slot_id, required_validations, completed_validations
		   FROM %s WHERE escrow_id = ? ORDER BY inference_id, slot_id`, table)
	rows, err := p.readDB.Query(query, escrowID)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", table, err)
	}
	defer rows.Close()
	return scanValidationObsRows(rows)
}

func scanValidationObsRows(rows *sql.Rows) ([]ValidationObsRow, error) {
	var out []ValidationObsRow
	for rows.Next() {
		var r ValidationObsRow
		if err := rows.Scan(&r.InferenceID, &r.SlotID, &r.Required, &r.Completed); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
