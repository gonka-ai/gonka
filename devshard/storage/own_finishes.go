package storage

import (
	"fmt"
	"strings"
	"time"
)

type OwnFinishStore interface {
	SaveOwnFinish(escrowID string, inferenceID uint64, tx []byte) error
	LoadOwnFinishes(escrowID string, inferenceIDs []uint64) (map[uint64][]byte, error)
}

const ownFinishLoadChunk = 500

func AsOwnFinishStore(s Storage, escrowID string) (OwnFinishStore, bool) {
	if s == nil {
		return nil, false
	}
	switch v := s.(type) {
	case *ObsRepairGate:
		return AsOwnFinishStore(v.Storage, escrowID)
	case *ManagedStorage:
		return AsOwnFinishStore(v.inner, escrowID)
	case *HybridStorage:
		b, err := v.routed(escrowID)
		if err != nil {
			return nil, false
		}
		return AsOwnFinishStore(b, escrowID)
	}
	if f, ok := s.(OwnFinishStore); ok {
		return f, true
	}
	return nil, false
}

func (s *SQLite) SaveOwnFinish(escrowID string, inferenceID uint64, tx []byte) error {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return err
	}
	_, err = p.writeDB.Exec(
		`INSERT INTO own_finishes (escrow_id, inference_id, tx_proto, created_at)
		 VALUES (?, ?, ?, strftime('%s','now'))
		 ON CONFLICT(escrow_id, inference_id) DO NOTHING`,
		escrowID, inferenceID, tx,
	)
	if err != nil {
		return fmt.Errorf("save own finish: %w", err)
	}
	return nil
}

func (s *SQLite) LoadOwnFinishes(escrowID string, inferenceIDs []uint64) (map[uint64][]byte, error) {
	out := make(map[uint64][]byte)
	if len(inferenceIDs) == 0 {
		return out, nil
	}
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(inferenceIDs); start += ownFinishLoadChunk {
		chunk := inferenceIDs[start:min(start+ownFinishLoadChunk, len(inferenceIDs))]
		args := make([]any, 0, len(chunk)+1)
		args = append(args, escrowID)
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := p.readDB.Query(
			`SELECT inference_id, tx_proto FROM own_finishes
			  WHERE escrow_id = ? AND inference_id IN (?`+strings.Repeat(",?", len(chunk)-1)+`)`,
			args...,
		)
		if err != nil {
			return nil, fmt.Errorf("load own finishes: %w", err)
		}
		for rows.Next() {
			var id uint64
			var tx []byte
			if err := rows.Scan(&id, &tx); err != nil {
				rows.Close()
				return nil, fmt.Errorf("load own finishes: %w", err)
			}
			out[id] = tx
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("load own finishes: %w", err)
		}
	}
	return out, nil
}

func (s *SQLite) listOwnFinishes(escrowID string) (map[uint64][]byte, error) {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return nil, err
	}
	rows, err := p.readDB.Query(`SELECT inference_id, tx_proto FROM own_finishes WHERE escrow_id = ?`, escrowID)
	if err != nil {
		return nil, fmt.Errorf("list own finishes: %w", err)
	}
	defer rows.Close()
	out := make(map[uint64][]byte)
	for rows.Next() {
		var id uint64
		var tx []byte
		if err := rows.Scan(&id, &tx); err != nil {
			return nil, fmt.Errorf("list own finishes: %w", err)
		}
		out[id] = tx
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list own finishes: %w", err)
	}
	return out, nil
}

func (s *Postgres) SaveOwnFinish(escrowID string, inferenceID uint64, tx []byte) error {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	if err := s.ensurePartition(ctx, epochID); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO devshard_own_finishes (epoch_id, escrow_id, inference_id, tx_proto, created_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (epoch_id, escrow_id, inference_id) DO NOTHING`,
		epochID, escrowID, int64(inferenceID), tx, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("save own finish: %w", err)
	}
	return nil
}

func (s *Postgres) LoadOwnFinishes(escrowID string, inferenceIDs []uint64) (map[uint64][]byte, error) {
	out := make(map[uint64][]byte)
	if len(inferenceIDs) == 0 {
		return out, nil
	}
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(inferenceIDs))
	for i, id := range inferenceIDs {
		ids[i] = int64(id)
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx,
		`SELECT inference_id, tx_proto FROM devshard_own_finishes
		  WHERE epoch_id = $1 AND escrow_id = $2 AND inference_id = ANY($3)`,
		epochID, escrowID, ids,
	)
	if err != nil {
		return nil, fmt.Errorf("load own finishes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var tx []byte
		if err := rows.Scan(&id, &tx); err != nil {
			return nil, fmt.Errorf("load own finishes: %w", err)
		}
		out[uint64(id)] = tx
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load own finishes: %w", err)
	}
	return out, nil
}

func (m *Memory) SaveOwnFinish(escrowID string, inferenceID uint64, tx []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[escrowID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrSessionNotFound, escrowID)
	}
	if s.ownFinishes == nil {
		s.ownFinishes = make(map[uint64][]byte)
	}
	if _, exists := s.ownFinishes[inferenceID]; !exists {
		s.ownFinishes[inferenceID] = append([]byte(nil), tx...)
	}
	return nil
}

func (m *Memory) LoadOwnFinishes(escrowID string, inferenceIDs []uint64) (map[uint64][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[escrowID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, escrowID)
	}
	out := make(map[uint64][]byte)
	for _, id := range inferenceIDs {
		if tx, ok := s.ownFinishes[id]; ok {
			out[id] = append([]byte(nil), tx...)
		}
	}
	return out, nil
}
