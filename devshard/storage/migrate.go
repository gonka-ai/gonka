package storage

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"devshard/types"

	"google.golang.org/protobuf/proto"

	_ "modernc.org/sqlite"
)

// EpochResolver returns the epoch_index for an escrow id. The migration
// helper uses it to stamp legacy sessions (which had no epoch_id column)
// with the right partition key so they land in the new layout.
type EpochResolver func(escrowID string) (uint64, error)

// ErrSkipLegacySession tells migration to skip a stale legacy session while
// still treating the migration as successful. Other resolver errors abort the
// migration and leave the legacy DB in place.
var ErrSkipLegacySession = errors.New("skip legacy session")

// MigrateLegacySQLite copies sessions, diffs and signatures from the legacy
// single-file SQLite store at legacyPath into dest. After a successful
// migration the legacy file is renamed to legacyPath + ".migrated.<ts>" so
// repeated startups are idempotent.
//
// No-op if legacyPath does not exist. Returns the number of sessions moved.
//
// Migration runs through the public Storage API of dest, so it works against
// both the per-epoch SQLite backend and the Postgres backend. The copy path is
// resumable: already-copied diffs are verified, missing signatures are replayed,
// and conflicting destination rows stop the migration. Sessions whose escrow is
// explicitly marked with ErrSkipLegacySession are skipped with a warning. Other
// resolver errors abort before copying and leave legacyPath in place for a retry.
func MigrateLegacySQLite(legacyPath string, dest Storage, resolveEpoch EpochResolver) (int, error) {
	info, err := os.Stat(legacyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("stat legacy %s: %w", legacyPath, err)
	}
	if info.IsDir() {
		// Path was already promoted to the new directory layout. Nothing to do.
		return 0, nil
	}

	src, err := sql.Open("sqlite", legacyPath)
	if err != nil {
		return 0, fmt.Errorf("open legacy %s: %w", legacyPath, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = src.Close()
		}
	}()

	for _, p := range []string{
		"PRAGMA query_only=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := src.Exec(p); err != nil {
			return 0, fmt.Errorf("legacy pragma %s: %w", p, err)
		}
	}

	rows, err := src.Query(
		`SELECT escrow_id, version, creator_addr, config_json, group_json, initial_balance,
		        latest_nonce, last_finalized, status
		 FROM sessions`,
	)
	if err != nil {
		return 0, fmt.Errorf("query legacy sessions: %w", err)
	}

	type legacySession struct {
		escrowID, version, creatorAddr, configJSON, groupJSON, status string
		initialBalance, latestNonce, lastFinalized                    uint64
	}
	var sessions []legacySession
	for rows.Next() {
		var ls legacySession
		var version sql.NullString
		if err := rows.Scan(
			&ls.escrowID, &version, &ls.creatorAddr, &ls.configJSON, &ls.groupJSON,
			&ls.initialBalance, &ls.latestNonce, &ls.lastFinalized, &ls.status,
		); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan legacy session: %w", err)
		}
		if version.Valid {
			ls.version = version.String
		}
		sessions = append(sessions, ls)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iter legacy sessions: %w", err)
	}

	resolved := make(map[string]uint64, len(sessions))
	for _, ls := range sessions {
		epochID, err := resolveEpoch(ls.escrowID)
		if err != nil {
			if errors.Is(err, ErrSkipLegacySession) {
				slog.Warn("devshard migrate: skipping stale legacy session",
					"escrow_id", ls.escrowID, "error", err)
				continue
			}
			return 0, fmt.Errorf("resolve epoch for %s: %w", ls.escrowID, err)
		}
		resolved[ls.escrowID] = epochID
	}

	migrated := 0
	for _, ls := range sessions {
		epochID, ok := resolved[ls.escrowID]
		if !ok {
			continue
		}

		var cfg types.SessionConfig
		if err := json.Unmarshal([]byte(ls.configJSON), &cfg); err != nil {
			return migrated, fmt.Errorf("unmarshal config for %s: %w", ls.escrowID, err)
		}
		var group []types.SlotAssignment
		if err := json.Unmarshal([]byte(ls.groupJSON), &group); err != nil {
			return migrated, fmt.Errorf("unmarshal group for %s: %w", ls.escrowID, err)
		}

		if err := dest.CreateSession(CreateSessionParams{
			EscrowID:       ls.escrowID,
			EpochID:        epochID,
			Version:        ls.version,
			CreatorAddr:    ls.creatorAddr,
			Config:         cfg,
			Group:          group,
			InitialBalance: ls.initialBalance,
		}); err != nil {
			return migrated, fmt.Errorf("create session %s: %w", ls.escrowID, err)
		}
		meta, err := dest.GetSessionMeta(ls.escrowID)
		if err != nil {
			return migrated, fmt.Errorf("read migrated session %s: %w", ls.escrowID, err)
		}
		if meta.EpochID != epochID {
			return migrated, fmt.Errorf("%w: escrow %s migrated epoch %d, expected %d",
				ErrSessionEpochConflict, ls.escrowID, meta.EpochID, epochID)
		}

		if err := migrateLegacyDiffs(src, dest, ls.escrowID, meta.LatestNonce); err != nil {
			return migrated, fmt.Errorf("migrate diffs for %s: %w", ls.escrowID, err)
		}

		if ls.lastFinalized > 0 {
			if err := dest.MarkFinalized(ls.escrowID, ls.lastFinalized); err != nil {
				return migrated, fmt.Errorf("mark finalized %s: %w", ls.escrowID, err)
			}
		}
		if ls.status == "settled" {
			if err := dest.MarkSettled(ls.escrowID); err != nil {
				return migrated, fmt.Errorf("mark settled %s: %w", ls.escrowID, err)
			}
		}
		migrated++
	}

	if err := src.Close(); err != nil {
		return migrated, fmt.Errorf("close legacy: %w", err)
	}
	closed = true

	stamped := fmt.Sprintf("%s.migrated.%d", legacyPath, time.Now().Unix())
	if err := os.Rename(legacyPath, stamped); err != nil {
		return migrated, fmt.Errorf("rename legacy file: %w", err)
	}
	// Best-effort cleanup of WAL/SHM sidecars from the renamed legacy file.
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecar := legacyPath + suffix
		if _, err := os.Stat(sidecar); err == nil {
			if err := os.Rename(sidecar, stamped+suffix); err != nil && !strings.Contains(err.Error(), "no such file") {
				slog.Warn("devshard migrate: failed to rename sidecar", "path", sidecar, "error", err)
			}
		}
	}
	return migrated, nil
}

func migrateLegacyDiffs(src *sql.DB, dest Storage, escrowID string, copiedThrough uint64) error {
	rows, err := src.Query(
		`SELECT nonce, txs_proto, user_sig, post_state_root, state_hash, warm_keys_json, created_at
		 FROM diffs WHERE escrow_id = ? ORDER BY nonce`,
		escrowID,
	)
	if err != nil {
		return fmt.Errorf("query diffs: %w", err)
	}
	type legacyDiff struct {
		nonce         uint64
		txsProto      []byte
		userSig       []byte
		postStateRoot []byte
		stateHash     []byte
		warmJSON      *string
		createdAt     int64
	}
	var diffs []legacyDiff
	for rows.Next() {
		var d legacyDiff
		if err := rows.Scan(&d.nonce, &d.txsProto, &d.userSig, &d.postStateRoot, &d.stateHash, &d.warmJSON, &d.createdAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan diff: %w", err)
		}
		diffs = append(diffs, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iter diffs: %w", err)
	}

	for _, d := range diffs {
		txs, err := unmarshalTxs(d.txsProto)
		if err != nil {
			return fmt.Errorf("unmarshal txs nonce %d: %w", d.nonce, err)
		}
		rec := types.DiffRecord{
			Diff: types.Diff{
				Nonce:         d.nonce,
				Txs:           txs,
				UserSig:       d.userSig,
				PostStateRoot: d.postStateRoot,
			},
			StateHash: d.stateHash,
			CreatedAt: d.createdAt,
		}
		if d.warmJSON != nil {
			wk := make(map[uint32]string)
			if err := json.Unmarshal([]byte(*d.warmJSON), &wk); err != nil {
				return fmt.Errorf("unmarshal warm keys nonce %d: %w", d.nonce, err)
			}
			rec.WarmKeyDelta = wk
		}

		// Pull signatures into the diff record before insert so the dest
		// store sees them as part of the same AppendDiff transaction.
		sigRows, err := src.Query(
			`SELECT slot_id, sig FROM signatures WHERE escrow_id = ? AND nonce = ?`,
			escrowID, d.nonce,
		)
		if err != nil {
			return fmt.Errorf("query sigs nonce %d: %w", d.nonce, err)
		}
		sigs := map[uint32][]byte{}
		for sigRows.Next() {
			var slotID uint32
			var sig []byte
			if err := sigRows.Scan(&slotID, &sig); err != nil {
				sigRows.Close()
				return fmt.Errorf("scan sig nonce %d: %w", d.nonce, err)
			}
			sigs[slotID] = sig
		}
		sigRows.Close()
		if err := sigRows.Err(); err != nil {
			return fmt.Errorf("iter sigs nonce %d: %w", d.nonce, err)
		}
		rec.Signatures = sigs

		if d.nonce > copiedThrough {
			if err := dest.AppendDiff(escrowID, rec); err != nil {
				return fmt.Errorf("append diff nonce %d: %w", d.nonce, err)
			}
			continue
		}

		existing, err := dest.GetDiffs(escrowID, d.nonce, d.nonce)
		if err != nil {
			return fmt.Errorf("read existing diff nonce %d: %w", d.nonce, err)
		}
		if len(existing) != 1 {
			return fmt.Errorf("expected one copied diff for %s nonce %d, got %d", escrowID, d.nonce, len(existing))
		}
		if err := verifyMigratedDiff(escrowID, rec, existing[0]); err != nil {
			return err
		}
		for slotID, sig := range sigs {
			if err := dest.AddSignature(escrowID, d.nonce, slotID, sig); err != nil {
				return fmt.Errorf("replay sig nonce %d slot %d: %w", d.nonce, slotID, err)
			}
		}
	}
	return nil
}

func verifyMigratedDiff(escrowID string, expected, actual types.DiffRecord) error {
	if expected.Nonce != actual.Nonce ||
		!bytes.Equal(expected.UserSig, actual.UserSig) ||
		!bytes.Equal(expected.PostStateRoot, actual.PostStateRoot) ||
		!bytes.Equal(expected.StateHash, actual.StateHash) ||
		expected.CreatedAt != actual.CreatedAt {
		return fmt.Errorf("migrated diff conflict for %s nonce %d", escrowID, expected.Nonce)
	}
	if !equalWarmKeyDelta(expected.WarmKeyDelta, actual.WarmKeyDelta) {
		return fmt.Errorf("migrated warm-key conflict for %s nonce %d", escrowID, expected.Nonce)
	}
	if !equalTxs(expected.Txs, actual.Txs) {
		return fmt.Errorf("migrated tx conflict for %s nonce %d", escrowID, expected.Nonce)
	}
	for slotID, sig := range expected.Signatures {
		if actualSig, ok := actual.Signatures[slotID]; ok && !bytes.Equal(sig, actualSig) {
			return fmt.Errorf("migrated signature conflict for %s nonce %d slot %d", escrowID, expected.Nonce, slotID)
		}
	}
	return nil
}

func equalWarmKeyDelta(a, b map[uint32]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		if b[k] != av {
			return false
		}
	}
	return true
}

func equalTxs(a, b []*types.DevshardTx) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !proto.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}
