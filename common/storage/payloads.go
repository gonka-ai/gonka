package storage

import (
	"context"
	"fmt"
)

const payloadsSchema = `
CREATE TABLE IF NOT EXISTS inferences (
    epoch_id         BIGINT NOT NULL,
    inference_id     TEXT   NOT NULL,
    prompt_payload   BYTEA,
    response_payload BYTEA,
    prompt_hash      TEXT,
    response_hash    TEXT,
    created_at       TIMESTAMP DEFAULT NOW(),
    PRIMARY KEY (epoch_id, inference_id)
) PARTITION BY RANGE (epoch_id);
`

func (s *Storage) ensurePayloadsSchema(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, payloadsSchema); err != nil {
		return fmt.Errorf("storage: ensure inferences schema: %w", err)
	}
	return nil
}

// StorePayload persists the prompt and response payloads for an inference.
// The inferences table is partitioned by epoch_id; the partition is created lazily on first write.
func (s *Storage) StorePayload(ctx context.Context, inferenceId string, epochId uint64, prompt, response []byte) error {
	if err := s.ensurePartition(ctx, epochId); err != nil {
		return err
	}

	// TODO: impl hashes computing for debugging
	promptHash := ""
	// promptHash, err := ComputePromptHash(prompt)
	// if err != nil {
	// 	logging.Debug("Failed to compute prompt hash", types.PayloadStorage, "error", err)
	// 	promptHash = ""
	// }

	responseHash := ""
	// responseHash, err := ComputeResponseHash(response)
	// if err != nil {
	// 	logging.Debug("Failed to compute response hash", types.PayloadStorage, "error", err)
	// 	responseHash = ""
	// }

	query := `
		INSERT INTO inferences (epoch_id, inference_id, prompt_payload, response_payload, prompt_hash, response_hash)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (epoch_id, inference_id) DO NOTHING
	`
	_, err := s.pool.Exec(ctx, query, epochId, inferenceId, prompt, response, promptHash, responseHash)
	if err != nil {
		return fmt.Errorf("insert payload: %w", err)
	}

	// logging.Debug("Stored payload in PostgreSQL", types.PayloadStorage, "inferenceId", inferenceId, "epochId", epochId)
	return nil
}

// ensurePartition creates the partition for epochID if it does not already exist.
func (s *Storage) ensurePartition(ctx context.Context, epochID uint64) error {
	name := fmt.Sprintf("inferences_epoch_%d", epochID)
	_, err := s.pool.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s
		 PARTITION OF inferences
		 FOR VALUES FROM (%d) TO (%d)`,
		name, epochID, epochID+1,
	))
	if err != nil {
		return fmt.Errorf("storage: ensure partition epoch %d: %w", epochID, err)
	}
	return nil
}

// RetrievePayload fetches the stored prompt and response for an inference.
func (s *Storage) RetrievePayload(ctx context.Context, inferenceID string, epochID uint64) (prompt, response []byte, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT prompt_payload, response_payload
		 FROM inferences
		 WHERE inference_id = $1 AND epoch_id = $2`,
		inferenceID, epochID,
	).Scan(&prompt, &response)
	if err != nil {
		if isNoRows(err) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("storage: retrieve payload %s: %w", inferenceID, err)
	}
	return prompt, response, nil
}

// PrunePayloads removes all inference payloads for the given epoch.
func (s *Storage) PrunePayloads(ctx context.Context, epochID uint64) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM inferences WHERE epoch_id = $1`,
		epochID,
	)
	if err != nil {
		return fmt.Errorf("storage: prune payloads epoch %d: %w", epochID, err)
	}
	return nil
}
