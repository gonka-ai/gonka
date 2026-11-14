package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"devshard/types"
)

// pgDiffBatchBeforeCommit, when set by a test, runs inside the batch
// transaction just before COMMIT.
var pgDiffBatchBeforeCommit func(ctx context.Context, tx pgx.Tx) error

type preparedDiffBatch struct {
	nonces    []int64
	txs       [][]byte
	userSigs  [][]byte
	posts     [][]byte
	hashes    [][]byte
	warm      []*string
	created   []int64
	sigNonces []int64
	sigSlots  []int64
	sigs      [][]byte
	maxNonce  uint64
}

func prepareDiffBatch(recs []types.DiffRecord) (*preparedDiffBatch, error) {
	b := &preparedDiffBatch{
		nonces:   make([]int64, 0, len(recs)),
		txs:      make([][]byte, 0, len(recs)),
		userSigs: make([][]byte, 0, len(recs)),
		posts:    make([][]byte, 0, len(recs)),
		hashes:   make([][]byte, 0, len(recs)),
		warm:     make([]*string, 0, len(recs)),
		created:  make([]int64, 0, len(recs)),
	}
	for i, rec := range recs {
		if i > 0 && rec.Nonce <= recs[i-1].Nonce {
			return nil, fmt.Errorf("diff batch nonces must ascend: %d after %d", rec.Nonce, recs[i-1].Nonce)
		}
		txsProto, err := marshalTxs(rec.Txs)
		if err != nil {
			return nil, err
		}
		// warm_keys_json is kept for the schema only: since v7 the warm-key
		// history is consensus state carried by the diffs themselves.
		var warmJSON *string
		b.nonces = append(b.nonces, int64(rec.Nonce))
		b.txs = append(b.txs, txsProto)
		b.userSigs = append(b.userSigs, rec.UserSig)
		b.posts = append(b.posts, rec.PostStateRoot)
		b.hashes = append(b.hashes, rec.StateHash)
		b.warm = append(b.warm, warmJSON)
		b.created = append(b.created, rec.CreatedAt)
		for slotID, sig := range rec.Signatures {
			b.sigNonces = append(b.sigNonces, int64(rec.Nonce))
			b.sigSlots = append(b.sigSlots, int64(slotID))
			b.sigs = append(b.sigs, sig)
		}
		b.maxNonce = rec.Nonce
	}
	return b, nil
}

// AppendDiffBatch implements DiffBatchWriter: one connection, one transaction,
// a fixed number of statements however many diffs the batch carries.
func (s *Postgres) AppendDiffBatch(ctx context.Context, escrowID string, recs []types.DiffRecord) error {
	if len(recs) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return err
	}
	b, err := prepareDiffBatch(recs)
	if err != nil {
		return err
	}

	waitCtx, cancelWait := context.WithTimeout(ctx, postgresOpTimeout)
	defer cancelWait()
	conn, err := s.pool.Acquire(waitCtx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrConnWait, err)
	}
	defer conn.Release()

	txCtx, cancelTx := context.WithTimeout(context.WithoutCancel(ctx), postgresOpTimeout)
	defer cancelTx()
	tx, err := conn.Begin(txCtx)
	if err != nil {
		return err
	}
	defer tx.Rollback(txCtx)

	if err := writeDiffBatchTx(txCtx, tx, epochID, escrowID, b); err != nil {
		return err
	}
	if hook := pgDiffBatchBeforeCommit; hook != nil {
		if err := hook(txCtx, tx); err != nil {
			return err
		}
	}
	return tx.Commit(txCtx)
}

func writeDiffBatchTx(ctx context.Context, tx pgx.Tx, epochID uint64, escrowID string, b *preparedDiffBatch) error {
	rows, err := tx.Query(ctx,
		`INSERT INTO devshard_diffs
		    (epoch_id, escrow_id, nonce, txs_proto, user_sig, post_state_root, state_hash, warm_keys_json, created_at)
		 SELECT $1, $2, u.nonce, u.txs, u.usig, u.post, u.hash, u.warm, u.created
		 FROM unnest($3::bigint[], $4::bytea[], $5::bytea[], $6::bytea[], $7::bytea[], $8::text[], $9::bigint[])
		      AS u(nonce, txs, usig, post, hash, warm, created)
		 ON CONFLICT (epoch_id, escrow_id, nonce) DO NOTHING
		 RETURNING nonce`,
		epochID, escrowID, b.nonces, b.txs, b.userSigs, b.posts, b.hashes, b.warm, b.created,
	)
	if err != nil {
		return fmt.Errorf("insert diff batch: %w", err)
	}
	inserted := make(map[int64]struct{}, len(b.nonces))
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return fmt.Errorf("scan inserted nonce: %w", err)
		}
		inserted[n] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("insert diff batch: %w", err)
	}

	if len(inserted) < len(b.nonces) {
		if err := checkDiffBatchConflicts(ctx, tx, epochID, escrowID, b, inserted); err != nil {
			return err
		}
	}

	if len(b.sigs) > 0 {
		if _, err := tx.Exec(ctx,
			`INSERT INTO devshard_signatures (epoch_id, escrow_id, nonce, slot_id, sig)
			 SELECT $1, $2, u.nonce, u.slot, u.sig
			 FROM unnest($3::bigint[], $4::bigint[], $5::bytea[]) AS u(nonce, slot, sig)
			 ON CONFLICT (epoch_id, escrow_id, nonce, slot_id) DO UPDATE SET sig = EXCLUDED.sig`,
			epochID, escrowID, b.sigNonces, b.sigSlots, b.sigs,
		); err != nil {
			return fmt.Errorf("insert sig batch: %w", err)
		}
	}

	if _, err := tx.Exec(ctx,
		`UPDATE devshard_sessions SET latest_nonce = GREATEST(latest_nonce, $1)
		 WHERE epoch_id = $2 AND escrow_id = $3`,
		b.maxNonce, epochID, escrowID,
	); err != nil {
		return fmt.Errorf("update latest_nonce: %w", err)
	}
	return nil
}

// checkDiffBatchConflicts compares every nonce the insert skipped against the
// durable row, so an identical replay passes and a fork fails the batch.
func checkDiffBatchConflicts(ctx context.Context, tx pgx.Tx, epochID uint64, escrowID string, b *preparedDiffBatch, inserted map[int64]struct{}) error {
	idx := make(map[int64]int, len(b.nonces)-len(inserted))
	conflicts := make([]int64, 0, len(b.nonces)-len(inserted))
	for i, n := range b.nonces {
		if _, ok := inserted[n]; !ok {
			idx[n] = i
			conflicts = append(conflicts, n)
		}
	}
	rows, err := tx.Query(ctx,
		`SELECT nonce, txs_proto, user_sig, post_state_root, state_hash, warm_keys_json
		 FROM devshard_diffs
		 WHERE epoch_id = $1 AND escrow_id = $2 AND nonce = ANY($3::bigint[])`,
		epochID, escrowID, conflicts,
	)
	if err != nil {
		return fmt.Errorf("read existing diffs after conflict: %w", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var n int64
		var haveTxs, haveUserSig, havePost, haveHash []byte
		var haveWarm *string
		if err := rows.Scan(&n, &haveTxs, &haveUserSig, &havePost, &haveHash, &haveWarm); err != nil {
			return fmt.Errorf("scan existing diff after conflict: %w", err)
		}
		i, ok := idx[n]
		if !ok {
			continue
		}
		seen++
		if err := checkDiffReplayIdentityRaw(
			escrowID, uint64(n),
			b.txs[i], b.userSigs[i], b.posts[i], b.hashes[i], b.warm[i],
			haveTxs, haveUserSig, havePost, haveHash, haveWarm,
		); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read existing diffs after conflict: %w", err)
	}
	if seen != len(conflicts) {
		return fmt.Errorf("diff batch conflict: %d of %d skipped rows not found", len(conflicts)-seen, len(conflicts))
	}
	return nil
}
