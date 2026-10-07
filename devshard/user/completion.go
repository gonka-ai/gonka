package user

import (
	"context"
	"errors"
	"fmt"
	"time"

	"devshard/host"
	"devshard/types"
)

var ErrInferenceCompletionPending = errors.New("inference completion pending")

var ErrInferenceCompletionConflict = errors.New("inference completion payload conflict")

type InferenceCompletion struct {
	Nonce      uint64                `json:"nonce"`
	PreparedAt int64                 `json:"prepared_at"`
	Payload    host.InferencePayload `json:"payload"`
}

type InferenceCompletionStore interface {
	SaveInferenceCompletion(escrowID string, entry InferenceCompletion) error
	ListInferenceCompletions(escrowID string) ([]InferenceCompletion, error)
	DeleteInferenceCompletion(escrowID string, nonce uint64) error
}

func (s *Session) SetInferenceCompletionStore(store InferenceCompletionStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completionStore = store
	s.completionDurabilityErr = nil
	if store != nil {
		if durable, ok := s.store.(interface{ RequireDurableCommits(string) error }); ok {
			s.completionDurabilityErr = durable.RequireDurableCommits(s.escrowID)
		}
	}
}

// Caller holds s.mu.
func (s *Session) registerCompletionLocked(nonce uint64, params InferenceParams) error {
	if s.completionStore == nil {
		return nil
	}
	if s.completionDurabilityErr != nil {
		return fmt.Errorf("enable durable session commits: %w", s.completionDurabilityErr)
	}
	entry := InferenceCompletion{
		Nonce: nonce, PreparedAt: time.Now().UnixNano(),
		Payload: host.InferencePayload{Prompt: params.Prompt, Model: params.Model,
			InputLength: params.InputLength, MaxTokens: params.MaxTokens, StartedAt: params.StartedAt},
	}
	err := s.completionStore.SaveInferenceCompletion(s.escrowID, entry)
	if errors.Is(err, ErrInferenceCompletionConflict) && s.uncommittedCompletionLocked(nonce) {
		if err := s.completionStore.DeleteInferenceCompletion(s.escrowID, nonce); err != nil {
			return err
		}
		return s.completionStore.SaveInferenceCompletion(s.escrowID, entry)
	}
	return err
}

// Caller holds s.mu.
func (s *Session) uncommittedCompletionLocked(nonce uint64) bool {
	if nonce <= s.nonce {
		return false
	}
	if _, exists := s.sm.Inference(nonce); exists {
		return false
	}
	if s.store != nil {
		diffs, err := s.store.GetDiffs(s.escrowID, nonce, nonce)
		return err == nil && len(diffs) == 0
	}
	return true
}

// Caller holds s.mu.
func (s *Session) discardNextCompletionOrphanLocked(nonce uint64) error {
	if s.completionStore == nil {
		return nil
	}
	var exists bool
	var err error
	if lookup, ok := s.completionStore.(interface {
		HasInferenceCompletion(string, uint64) (bool, error)
	}); ok {
		exists, err = lookup.HasInferenceCompletion(s.escrowID, nonce)
	} else {
		var entries []InferenceCompletion
		entries, err = s.completionStore.ListInferenceCompletions(s.escrowID)
		for _, entry := range entries {
			if entry.Nonce == nonce {
				exists = true
				break
			}
		}
	}
	if err != nil {
		return fmt.Errorf("%w: look up orphan %d before nonce reuse: %v", ErrInferenceCompletionPending, nonce, err)
	}
	if !exists {
		return nil
	}
	if !s.uncommittedCompletionLocked(nonce) {
		return fmt.Errorf("%w: cannot prove nonce %d is unused before reuse", ErrInferenceCompletionPending, nonce)
	}
	if err := s.completionStore.DeleteInferenceCompletion(s.escrowID, nonce); err != nil {
		return fmt.Errorf("%w: clear orphan %d before nonce reuse: %v", ErrInferenceCompletionPending, nonce, err)
	}
	return nil
}

// Caller holds s.mu.
func (s *Session) completionOrphanLocked(nonce uint64) bool {
	if s.uncommittedCompletionLocked(nonce) {
		return true
	}
	if nonce > s.nonce || s.sm.InferenceSealed(nonce) {
		return false
	}
	if _, exists := s.sm.Inference(nonce); exists {
		return false
	}
	var diff *types.Diff
	if s.store != nil {
		recs, err := s.store.GetDiffs(s.escrowID, nonce, nonce)
		if err != nil || len(recs) != 1 || recs[0].Nonce != nonce {
			return false
		}
		diff = &recs[0].Diff
	} else {
		for i := range s.diffs {
			if s.diffs[i].Nonce == nonce {
				diff = &s.diffs[i]
				break
			}
		}
	}
	if diff == nil {
		return false
	}
	for _, tx := range diff.Txs {
		if tx.GetStartInference() != nil {
			return false
		}
	}
	return true
}

// Caller holds s.mu.
func (s *Session) removeUncommittedCompletionLocked(nonce uint64) {
	if s.completionStore == nil {
		return
	}
	if !s.uncommittedCompletionLocked(nonce) {
		return
	}
	_ = s.completionStore.DeleteInferenceCompletion(s.escrowID, nonce)
}

// Caller holds s.mu.
func (s *Session) resolveAppliedCompletionsLocked(txs []*types.DevshardTx) {
	if s.completionStore == nil {
		return
	}
	for _, tx := range txs {
		var id uint64
		if fi := tx.GetFinishInference(); fi != nil {
			id = fi.InferenceId
		} else if timeout := tx.GetTimeoutInference(); timeout != nil {
			id = timeout.InferenceId
		}
		if id != 0 {
			_ = s.completionStore.DeleteInferenceCompletion(s.escrowID, id)
		}
	}
}

func (s *Session) guardCompletion(ctx context.Context) error {
	s.mu.Lock()
	store, active, durabilityErr := s.completionStore, s.activeInferenceSends, s.completionDurabilityErr
	s.mu.Unlock()
	if store == nil {
		return nil
	}
	if durabilityErr != nil {
		return fmt.Errorf("%w: enable durable session commits: %v", ErrInferenceCompletionPending, durabilityErr)
	}
	if active > 0 {
		return fmt.Errorf("%w: escrow %s has %d active inference sends", ErrInferenceCompletionPending, s.escrowID, active)
	}
	entries, err := store.ListInferenceCompletions(s.escrowID)
	if err != nil {
		return fmt.Errorf("%w: load escrow %s completion journal: %v", ErrInferenceCompletionPending, s.escrowID, err)
	}
	retryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, entry := range entries {
		if s.completionResolved(entry.Nonce) {
			if err := store.DeleteInferenceCompletion(s.escrowID, entry.Nonce); err != nil {
				return fmt.Errorf("%w: clear nonce %d: %v", ErrInferenceCompletionPending, entry.Nonce, err)
			}
			continue
		}
		rec, ok := s.sm.Inference(entry.Nonce)
		if !ok {
			s.mu.Lock()
			orphan := s.completionOrphanLocked(entry.Nonce)
			s.mu.Unlock()
			if orphan {
				if err := store.DeleteInferenceCompletion(s.escrowID, entry.Nonce); err != nil {
					return fmt.Errorf("%w: clear orphan %d: %v", ErrInferenceCompletionPending, entry.Nonce, err)
				}
				continue
			}
			return fmt.Errorf("%w: nonce %d has no completion evidence", ErrInferenceCompletionPending, entry.Nonce)
		}
		if err := host.VerifyPayload(&entry.Payload, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, rec.StartedAt); err != nil {
			return fmt.Errorf("%w: nonce %d retry payload: %v", ErrInferenceCompletionPending, entry.Nonce, err)
		}
		result, retryErr := s.HandleTimeout(retryCtx, entry.Nonce, time.Unix(0, entry.PreparedAt), &entry.Payload)
		if !s.completionResolved(entry.Nonce) {
			return fmt.Errorf("%w: nonce %d timeout %s/%s: %v", ErrInferenceCompletionPending, entry.Nonce, result.Outcome, result.DetailReason, retryErr)
		}
		if err := store.DeleteInferenceCompletion(s.escrowID, entry.Nonce); err != nil {
			return fmt.Errorf("%w: clear nonce %d: %v", ErrInferenceCompletionPending, entry.Nonce, err)
		}
	}
	return nil
}

func (s *Session) completionResolved(nonce uint64) bool {
	if rec, ok := s.sm.Inference(nonce); ok {
		return finishAppliedStatus(rec.Status) || rec.Status == types.StatusTimedOut
	}
	// Settlement-generated seals do not prove execution completed.
	return s.sm.Phase() == types.PhaseActive && s.sm.InferenceSealed(nonce)
}
