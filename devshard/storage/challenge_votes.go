package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ChallengeVoteStore keeps a host's verdict on a challenged inference, so a
// repeated challenge, a restart and a sibling replica all answer with the same
// vote. An entry starts pending, carrying the end of its fetch window, and is
// settled once with the verdict. The data is opaque to the store.
type ChallengeVoteStore interface {
	// LoadOrStoreChallengeVote stores data as the pending entry unless an
	// entry exists, and returns the stored entry and whether it is settled.
	LoadOrStoreChallengeVote(escrowID string, inferenceID uint64, data []byte) ([]byte, bool, error)
	// SettleChallengeVote replaces a pending entry with the verdict unless a
	// verdict is already stored, and returns the stored verdict.
	SettleChallengeVote(escrowID string, inferenceID uint64, data []byte) ([]byte, error)
}

// AsChallengeVoteStore finds the challenge vote table behind a storage
// wrapper. A hybrid store routes each escrow to the backend that owns it.
func AsChallengeVoteStore(s Storage) (ChallengeVoteStore, bool) {
	if s == nil {
		return nil, false
	}
	switch v := s.(type) {
	case *ManagedStorage:
		return AsChallengeVoteStore(v.inner)
	case *ObsRepairGate:
		// devshardd hands its hosts the gate, not the store behind it.
		return AsChallengeVoteStore(v.Storage)
	case *HybridStorage:
		return hybridChallengeVotes{h: v}, true
	}
	c, ok := s.(ChallengeVoteStore)
	return c, ok
}

type hybridChallengeVotes struct {
	h *HybridStorage
}

func (r hybridChallengeVotes) backend(escrowID string) (ChallengeVoteStore, error) {
	b, err := r.h.routed(escrowID)
	if err != nil {
		return nil, err
	}
	c, ok := AsChallengeVoteStore(b)
	if !ok {
		return nil, fmt.Errorf("challenge votes: backend for %s has no vote table", escrowID)
	}
	return c, nil
}

func (r hybridChallengeVotes) LoadOrStoreChallengeVote(escrowID string, inferenceID uint64, data []byte) ([]byte, bool, error) {
	c, err := r.backend(escrowID)
	if err != nil {
		return nil, false, err
	}
	return c.LoadOrStoreChallengeVote(escrowID, inferenceID, data)
}

func (r hybridChallengeVotes) SettleChallengeVote(escrowID string, inferenceID uint64, data []byte) ([]byte, error) {
	c, err := r.backend(escrowID)
	if err != nil {
		return nil, err
	}
	return c.SettleChallengeVote(escrowID, inferenceID, data)
}

type memoryChallengeVote struct {
	data    []byte
	settled bool
}

func (m *Memory) LoadOrStoreChallengeVote(escrowID string, inferenceID uint64, data []byte) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.challengeVotes == nil {
		m.challengeVotes = make(map[string]map[uint64]memoryChallengeVote)
	}
	votes := m.challengeVotes[escrowID]
	if votes == nil {
		votes = make(map[uint64]memoryChallengeVote)
		m.challengeVotes[escrowID] = votes
	}
	if existing, ok := votes[inferenceID]; ok {
		return append([]byte(nil), existing.data...), existing.settled, nil
	}
	votes[inferenceID] = memoryChallengeVote{data: append([]byte(nil), data...)}
	return append([]byte(nil), data...), false, nil
}

func (m *Memory) SettleChallengeVote(escrowID string, inferenceID uint64, data []byte) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.challengeVotes == nil {
		m.challengeVotes = make(map[string]map[uint64]memoryChallengeVote)
	}
	votes := m.challengeVotes[escrowID]
	if votes == nil {
		votes = make(map[uint64]memoryChallengeVote)
		m.challengeVotes[escrowID] = votes
	}
	if existing, ok := votes[inferenceID]; ok && existing.settled {
		return append([]byte(nil), existing.data...), nil
	}
	votes[inferenceID] = memoryChallengeVote{data: append([]byte(nil), data...), settled: true}
	return append([]byte(nil), data...), nil
}

func (s *SQLite) LoadOrStoreChallengeVote(escrowID string, inferenceID uint64, data []byte) ([]byte, bool, error) {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return nil, false, err
	}
	if _, err := p.writeDB.Exec(
		`INSERT INTO challenge_votes (escrow_id, inference_id, settled, data) VALUES (?, ?, 0, ?)
		 ON CONFLICT (escrow_id, inference_id) DO NOTHING`,
		escrowID, inferenceID, data,
	); err != nil {
		return nil, false, fmt.Errorf("challenge votes: store %s/%d: %w", escrowID, inferenceID, err)
	}
	return sqliteReadChallengeVote(p.writeDB, escrowID, inferenceID)
}

func (s *SQLite) SettleChallengeVote(escrowID string, inferenceID uint64, data []byte) ([]byte, error) {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return nil, err
	}
	if _, err := p.writeDB.Exec(
		`INSERT INTO challenge_votes (escrow_id, inference_id, settled, data) VALUES (?, ?, 1, ?)
		 ON CONFLICT (escrow_id, inference_id) DO UPDATE SET settled = 1, data = excluded.data
		 WHERE challenge_votes.settled = 0`,
		escrowID, inferenceID, data,
	); err != nil {
		return nil, fmt.Errorf("challenge votes: settle %s/%d: %w", escrowID, inferenceID, err)
	}
	stored, _, err := sqliteReadChallengeVote(p.writeDB, escrowID, inferenceID)
	return stored, err
}

func sqliteReadChallengeVote(db *sql.DB, escrowID string, inferenceID uint64) ([]byte, bool, error) {
	var data []byte
	var settled int
	err := db.QueryRow(
		`SELECT data, settled FROM challenge_votes WHERE escrow_id = ? AND inference_id = ?`,
		escrowID, inferenceID,
	).Scan(&data, &settled)
	if err != nil {
		return nil, false, fmt.Errorf("challenge votes: read %s/%d: %w", escrowID, inferenceID, err)
	}
	return data, settled != 0, nil
}

func (s *Postgres) LoadOrStoreChallengeVote(escrowID string, inferenceID uint64, data []byte) ([]byte, bool, error) {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return nil, false, err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	err = s.execChallengeVote(ctx, epochID,
		`INSERT INTO devshard_challenge_votes (epoch_id, escrow_id, inference_id, settled, data)
		 VALUES ($1, $2, $3, FALSE, $4)
		 ON CONFLICT (epoch_id, escrow_id, inference_id) DO NOTHING`,
		epochID, escrowID, inferenceID, data,
	)
	if err != nil {
		return nil, false, fmt.Errorf("challenge votes: store %s/%d: %w", escrowID, inferenceID, err)
	}
	return s.readChallengeVote(ctx, epochID, escrowID, inferenceID)
}

func (s *Postgres) SettleChallengeVote(escrowID string, inferenceID uint64, data []byte) ([]byte, error) {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	err = s.execChallengeVote(ctx, epochID,
		`INSERT INTO devshard_challenge_votes (epoch_id, escrow_id, inference_id, settled, data)
		 VALUES ($1, $2, $3, TRUE, $4)
		 ON CONFLICT (epoch_id, escrow_id, inference_id) DO UPDATE SET settled = TRUE, data = excluded.data
		 WHERE devshard_challenge_votes.settled = FALSE`,
		epochID, escrowID, inferenceID, data,
	)
	if err != nil {
		return nil, fmt.Errorf("challenge votes: settle %s/%d: %w", escrowID, inferenceID, err)
	}
	stored, _, err := s.readChallengeVote(ctx, epochID, escrowID, inferenceID)
	return stored, err
}

// execChallengeVote runs a write and, when the epoch's partition predates the
// vote table, creates the missing partition and runs it once more. A process
// that indexed the epoch at startup marks it known without creating
// partitions for parents added later.
func (s *Postgres) execChallengeVote(ctx context.Context, epochID uint64, query string, args ...any) error {
	_, err := s.pool.Exec(ctx, query, args...)
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		return err
	}
	s.mu.Lock()
	delete(s.knownEpochs, epochID)
	s.mu.Unlock()
	if err := s.ensurePartition(ctx, epochID); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, query, args...)
	return err
}

func (s *Postgres) readChallengeVote(ctx context.Context, epochID uint64, escrowID string, inferenceID uint64) ([]byte, bool, error) {
	var data []byte
	var settled bool
	err := s.pool.QueryRow(ctx,
		`SELECT data, settled FROM devshard_challenge_votes
		  WHERE epoch_id = $1 AND escrow_id = $2 AND inference_id = $3`,
		epochID, escrowID, inferenceID,
	).Scan(&data, &settled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("challenge votes: %s/%d missing after write", escrowID, inferenceID)
	}
	if err != nil {
		return nil, false, fmt.Errorf("challenge votes: read %s/%d: %w", escrowID, inferenceID, err)
	}
	return data, settled, nil
}

var (
	_ ChallengeVoteStore = (*Memory)(nil)
	_ ChallengeVoteStore = (*SQLite)(nil)
	_ ChallengeVoteStore = (*Postgres)(nil)
)
