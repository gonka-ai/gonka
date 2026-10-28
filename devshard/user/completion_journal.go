package user

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"devshard/storage"
	"devshard/types"
)

// sessionCompletionJournal is the completion journal kept in the session storage.
type sessionCompletionJournal struct {
	journal storage.CompletionJournal
}

var _ InferenceCompletionStore = sessionCompletionJournal{}

func (j sessionCompletionJournal) SaveInferenceCompletion(escrowID string, entry InferenceCompletion) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	stored, err := j.journal.SaveCompletion(escrowID, storage.CompletionEntry{Nonce: entry.Nonce, Data: data})
	if err != nil {
		return err
	}
	same, err := sameCompletionObligation(entry, stored)
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("%w: escrow %s nonce %d", ErrInferenceCompletionConflict, escrowID, entry.Nonce)
	}
	return nil
}

func (j sessionCompletionJournal) ListInferenceCompletions(escrowID string) ([]InferenceCompletion, error) {
	rows, err := j.journal.ListCompletions(escrowID)
	if err != nil {
		return nil, err
	}
	entries := make([]InferenceCompletion, 0, len(rows))
	for _, row := range rows {
		var entry InferenceCompletion
		if err := json.Unmarshal(row.Data, &entry); err != nil {
			return nil, err
		}
		if entry.Nonce != row.Nonce || entry.PreparedAt <= 0 {
			return nil, fmt.Errorf("invalid completion record for escrow %s nonce %d", escrowID, row.Nonce)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (j sessionCompletionJournal) DeleteInferenceCompletion(escrowID string, nonce uint64) error {
	return j.journal.DeleteCompletion(escrowID, nonce)
}

func (j sessionCompletionJournal) HasInferenceCompletion(escrowID string, nonce uint64) (bool, error) {
	return j.journal.HasCompletion(escrowID, nonce)
}

// sameCompletionObligation reports whether stored records the obligation entry does: the same retry payload. The
// time it was prepared does not take part.
func sameCompletionObligation(entry InferenceCompletion, stored []byte) (bool, error) {
	var previous InferenceCompletion
	if err := json.Unmarshal(stored, &previous); err != nil {
		return false, err
	}
	want, err := json.Marshal(entry.Payload)
	if err != nil {
		return false, err
	}
	got, err := json.Marshal(previous.Payload)
	if err != nil {
		return false, err
	}
	return bytes.Equal(want, got), nil
}

// UseSessionCompletionJournal keeps the completion journal in the session storage when that storage can hold it, so
// a Start and its obligation are written by one commit, and moves this escrow's rows out of legacy first. A storage
// without a journal keeps legacy as the journal, as SetInferenceCompletionStore does. If the rows cannot be moved,
// the session refuses new Starts and Finalize, as when durable commits cannot be enabled.
func (s *Session) UseSessionCompletionJournal(legacy InferenceCompletionStore) {
	s.mu.Lock()
	journal, ok := s.store.(storage.CompletionJournal)
	s.mu.Unlock()
	if !ok {
		s.SetInferenceCompletionStore(legacy)
		return
	}
	store := sessionCompletionJournal{journal: journal}
	s.SetInferenceCompletionStore(store)
	moveErr := moveCompletions(s.escrowID, legacy, store)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completionJournal = journal
	if moveErr != nil && s.completionDurabilityErr == nil {
		s.completionDurabilityErr = fmt.Errorf("move completion journal into session storage: %w", moveErr)
	}
}

// moveCompletions copies every row of escrowID from legacy into the session journal and then drops it from legacy.
// A row already in the session journal stays as it is, so a move that stopped half way can run again.
func moveCompletions(escrowID string, legacy InferenceCompletionStore, store sessionCompletionJournal) error {
	if legacy == nil {
		return nil
	}
	entries, err := legacy.ListInferenceCompletions(escrowID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := store.SaveInferenceCompletion(escrowID, entry); err != nil {
			return fmt.Errorf("nonce %d: %w", entry.Nonce, err)
		}
		if err := legacy.DeleteInferenceCompletion(escrowID, entry.Nonce); err != nil {
			return fmt.Errorf("nonce %d: %w", entry.Nonce, err)
		}
	}
	return nil
}

// persistComposedDiffLocked writes a composed diff. With the journal in the session storage, the staged obligation of
// the Start at rec.Nonce and the removal of the obligations applied resolves are part of the same commit.
// Caller holds s.mu.
func (s *Session) persistComposedDiffLocked(ctx context.Context, rec types.DiffRecord, applied []*types.DevshardTx) error {
	if s.completionJournal == nil {
		return s.persistDiffRetryLocked(ctx, rec)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	w := storage.CompletionWrite{Resolved: resolvedCompletionIDs(applied)}
	staged := s.stagedCompletion
	if staged != nil && staged.Nonce == rec.Nonce {
		data, err := json.Marshal(staged)
		if err != nil {
			return err
		}
		w.Add = &storage.CompletionEntry{Nonce: staged.Nonce, Data: data}
		w.SameAdd = func(stored []byte) bool {
			same, err := sameCompletionObligation(*staged, stored)
			return err == nil && same
		}
	}
	err := storage.AppendDiffWithCompletionsRetry(context.WithoutCancel(ctx), s.completionJournal, s.escrowID, rec, w)
	if errors.Is(err, storage.ErrCompletionConflict) {
		return fmt.Errorf("%w: %v", ErrInferenceCompletionConflict, err)
	}
	if err == nil && w.Add != nil {
		s.stagedCompletion = nil
	}
	return err
}
