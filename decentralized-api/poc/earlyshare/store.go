// Package earlyshare provides DAPI-local persistence and helpers for the
// early PoC share guard. It captures early on-chain PoC v2 commitments near the
// first-third of the generation window, persists them locally, and tracks a
// per-(participant, model) miss streak so DAPI can later compare early vs final
// commitments during off-chain validation.
//
// This is intentionally DAPI-local: it does not create any consensus rule. See
// proposals/poc/early-share-guard-dapi.md for the design.
package earlyshare

import (
	"context"
	"database/sql"
	"errors"
)

// Checkpoint is one early commitment captured for a (stage, participant, model).
type Checkpoint struct {
	StageHeight           int64
	ParticipantAddress    string
	ModelID               string
	EarlyCount            uint32
	EarlyRootHash         []byte
	CheckpointBlockHeight int64
	CapturedAtBlockHeight int64
}

// GuardState is the cross-epoch miss-streak state for a (participant, model).
type GuardState struct {
	ParticipantAddress string
	ModelID            string
	ConsecutiveMisses  int
	UpdatedStageHeight int64
}

// Store persists early-share checkpoints, capture-run metadata, and guard state
// in the embedded SQLite database. It reuses the *sql.DB owned by the DAPI
// config manager so it does not open a second file.
type Store struct {
	db *sql.DB
}

// NewStore wraps an already-open *sql.DB.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// EnsureSchema creates the early-share tables if they do not exist.
func (s *Store) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("earlyshare: db is nil")
	}
	stmt := `
CREATE TABLE IF NOT EXISTS poc_early_checkpoints (
  stage_height INTEGER NOT NULL,
  participant_address TEXT NOT NULL,
  model_id TEXT NOT NULL,
  early_count INTEGER NOT NULL,
  early_root_hash BLOB NOT NULL,
  checkpoint_block_height INTEGER NOT NULL,
  captured_at_block_height INTEGER NOT NULL,
  PRIMARY KEY (stage_height, participant_address, model_id)
);

CREATE TABLE IF NOT EXISTS poc_early_guard_state (
  participant_address TEXT NOT NULL,
  model_id TEXT NOT NULL,
  consecutive_misses INTEGER NOT NULL DEFAULT 0,
  updated_stage_height INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (participant_address, model_id)
);

CREATE TABLE IF NOT EXISTS poc_early_capture_runs (
  stage_height INTEGER NOT NULL,
  model_id TEXT NOT NULL,
  target_block_height INTEGER NOT NULL,
  captured_at_block_height INTEGER NOT NULL,
  captured_commit_count INTEGER NOT NULL,
  status TEXT NOT NULL,
  PRIMARY KEY (stage_height, model_id)
);
CREATE INDEX IF NOT EXISTS idx_poc_early_checkpoints_stage ON poc_early_checkpoints(stage_height);
CREATE INDEX IF NOT EXISTS idx_poc_early_capture_runs_stage ON poc_early_capture_runs(stage_height);`
	_, err := s.db.ExecContext(ctx, stmt)
	return err
}

// capture-run status values.
const (
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	// stageMarkerModel is a sentinel model_id used to record a stage-level
	// capture run that is independent of any specific model.
	stageMarkerModel = ""
)

// UpsertCheckpoints inserts or replaces the given checkpoints atomically.
func (s *Store) UpsertCheckpoints(ctx context.Context, checkpoints []Checkpoint) error {
	if s == nil || s.db == nil {
		return errors.New("earlyshare: db is nil")
	}
	if len(checkpoints) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	q := `INSERT INTO poc_early_checkpoints (
  stage_height, participant_address, model_id, early_count, early_root_hash,
  checkpoint_block_height, captured_at_block_height
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(stage_height, participant_address, model_id) DO UPDATE SET
  early_count = excluded.early_count,
  early_root_hash = excluded.early_root_hash,
  checkpoint_block_height = excluded.checkpoint_block_height,
  captured_at_block_height = excluded.captured_at_block_height`
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, c := range checkpoints {
		if _, err := stmt.ExecContext(ctx,
			c.StageHeight,
			c.ParticipantAddress,
			c.ModelID,
			c.EarlyCount,
			c.EarlyRootHash,
			c.CheckpointBlockHeight,
			c.CapturedAtBlockHeight,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetCheckpoints returns all early checkpoints for a stage.
func (s *Store) GetCheckpoints(ctx context.Context, stageHeight int64) ([]Checkpoint, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("earlyshare: db is nil")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT stage_height, participant_address, model_id, early_count, early_root_hash,
       checkpoint_block_height, captured_at_block_height
FROM poc_early_checkpoints WHERE stage_height = ?`, stageHeight)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Checkpoint
	for rows.Next() {
		var c Checkpoint
		if err := rows.Scan(
			&c.StageHeight,
			&c.ParticipantAddress,
			&c.ModelID,
			&c.EarlyCount,
			&c.EarlyRootHash,
			&c.CheckpointBlockHeight,
			&c.CapturedAtBlockHeight,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// HasCompletedCapture reports whether a completed capture run exists for the
// stage. Used for capture idempotency and to decide if the guard may run.
func (s *Store) HasCompletedCapture(ctx context.Context, stageHeight int64) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("earlyshare: db is nil")
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM poc_early_capture_runs WHERE stage_height = ? AND status = ?`,
		stageHeight, StatusCompleted).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// MarkCaptureRun upserts a single capture-run row.
func (s *Store) MarkCaptureRun(ctx context.Context, stageHeight int64, modelID string, target, capturedAt int64, count int, status string) error {
	if s == nil || s.db == nil {
		return errors.New("earlyshare: db is nil")
	}
	q := `INSERT INTO poc_early_capture_runs (
  stage_height, model_id, target_block_height, captured_at_block_height, captured_commit_count, status
) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(stage_height, model_id) DO UPDATE SET
  target_block_height = excluded.target_block_height,
  captured_at_block_height = excluded.captured_at_block_height,
  captured_commit_count = excluded.captured_commit_count,
  status = excluded.status`
	_, err := s.db.ExecContext(ctx, q, stageHeight, modelID, target, capturedAt, count, status)
	return err
}

// MarkStageCaptured records per-model capture rows plus a stage-level marker so
// HasCompletedCapture is true after a successful capture.
func (s *Store) MarkStageCaptured(ctx context.Context, stageHeight int64, target, capturedAt int64, perModelCounts map[string]int) error {
	if err := s.MarkCaptureRun(ctx, stageHeight, stageMarkerModel, target, capturedAt, sumCounts(perModelCounts), StatusCompleted); err != nil {
		return err
	}
	for modelID, count := range perModelCounts {
		if modelID == stageMarkerModel {
			continue
		}
		if err := s.MarkCaptureRun(ctx, stageHeight, modelID, target, capturedAt, count, StatusCompleted); err != nil {
			return err
		}
	}
	return nil
}

func sumCounts(m map[string]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}

// GetGuardState returns the miss-streak state for a (participant, model).
// ok is false when no row exists yet.
func (s *Store) GetGuardState(ctx context.Context, participant, modelID string) (GuardState, bool, error) {
	if s == nil || s.db == nil {
		return GuardState{}, false, errors.New("earlyshare: db is nil")
	}
	row := s.db.QueryRowContext(ctx, `
SELECT consecutive_misses, updated_stage_height
FROM poc_early_guard_state WHERE participant_address = ? AND model_id = ?`, participant, modelID)
	st := GuardState{ParticipantAddress: participant, ModelID: modelID}
	if err := row.Scan(&st.ConsecutiveMisses, &st.UpdatedStageHeight); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return GuardState{ParticipantAddress: participant, ModelID: modelID}, false, nil
		}
		return GuardState{}, false, err
	}
	return st, true, nil
}

// UpsertGuardState writes the miss-streak state for a (participant, model).
func (s *Store) UpsertGuardState(ctx context.Context, st GuardState) error {
	if s == nil || s.db == nil {
		return errors.New("earlyshare: db is nil")
	}
	q := `INSERT INTO poc_early_guard_state (
  participant_address, model_id, consecutive_misses, updated_stage_height
) VALUES (?, ?, ?, ?)
ON CONFLICT(participant_address, model_id) DO UPDATE SET
  consecutive_misses = excluded.consecutive_misses,
  updated_stage_height = excluded.updated_stage_height`
	_, err := s.db.ExecContext(ctx, q, st.ParticipantAddress, st.ModelID, st.ConsecutiveMisses, st.UpdatedStageHeight)
	return err
}

// DeleteStage removes checkpoints and capture-run rows for a stage. Guard state
// is intentionally left intact (it carries the cross-epoch miss streak).
func (s *Store) DeleteStage(ctx context.Context, stageHeight int64) error {
	if s == nil || s.db == nil {
		return errors.New("earlyshare: db is nil")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM poc_early_checkpoints WHERE stage_height = ?`, stageHeight); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM poc_early_capture_runs WHERE stage_height = ?`, stageHeight); err != nil {
		return err
	}
	return tx.Commit()
}
