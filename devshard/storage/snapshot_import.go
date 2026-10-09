package storage

import (
	"fmt"

	"devshard/types"
)

func checkImportBlob(n uint64, data []byte) error {
	_, root, err := types.SnapshotData(data)
	if err != nil {
		return err
	}
	if n == 0 || len(root) != 32 {
		return fmt.Errorf("snapshot import requires nonce and root")
	}
	return nil
}

func (m *Memory) ImportSnapshot(id string, n uint64, data []byte) error {
	if err := checkImportBlob(n, data); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	if s.status != "active" {
		return ErrSessionNotActive
	}
	head := s.importedNonce
	if len(s.diffs) > 0 {
		head = max(head, s.diffs[len(s.diffs)-1].Nonce)
	}
	if head >= n {
		return ErrSnapshotAdvanced
	}
	s.snapshot = &snapshotData{nonce: n, data: append([]byte(nil), data...)}
	s.importedNonce = n
	return nil
}

func (s *SQLite) ImportSnapshot(id string, n uint64, data []byte) error {
	if err := checkImportBlob(n, data); err != nil {
		return err
	}
	p, _, err := s.poolFor(id)
	if err != nil {
		return err
	}
	tx, err := p.writeDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Take the write lock before reading the head.
	res, err := tx.Exec(`UPDATE sessions SET latest_nonce=?, imported_nonce=? WHERE escrow_id=? AND latest_nonce<? AND status='active'`, n, n, id, n)
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrSnapshotAdvanced
	}
	_, err = tx.Exec(`INSERT INTO snapshots(escrow_id,nonce,state_data,created_at) VALUES(?,?,?,strftime('%s','now')) ON CONFLICT(escrow_id) DO UPDATE SET nonce=excluded.nonce,state_data=excluded.state_data,created_at=excluded.created_at`, id, n, data)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Postgres) ImportSnapshot(id string, n uint64, data []byte) error {
	if err := checkImportBlob(n, data); err != nil {
		return err
	}
	epoch, err := s.lookupEpoch(id)
	if err != nil {
		return err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE devshard_sessions SET latest_nonce=$1,imported_nonce=$1 WHERE epoch_id=$2 AND escrow_id=$3 AND latest_nonce<$1 AND status='active'`, n, epoch, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSnapshotAdvanced
	}
	_, err = tx.Exec(ctx, `INSERT INTO devshard_snapshots(epoch_id,escrow_id,nonce,state_data,created_at) VALUES($1,$2,$3,$4,EXTRACT(EPOCH FROM now())::bigint) ON CONFLICT(epoch_id,escrow_id) DO UPDATE SET nonce=EXCLUDED.nonce,state_data=EXCLUDED.state_data,created_at=EXCLUDED.created_at`, epoch, id, n, data)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
