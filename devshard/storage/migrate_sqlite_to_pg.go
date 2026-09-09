package storage

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"devshard/types"

	"golang.org/x/sync/errgroup"
)

const (
	defaultMigrateWorkers   = 4
	defaultMigrateDiffChunk = 5000
)

// diffBatchAppender is implemented by backends that can insert many diffs in
// one round-trip (Postgres COPY; Memory under one lock).
type diffBatchAppender interface {
	AppendDiffs(escrowID string, diffs []types.DiffRecord) error
}

// MigrateSQLiteSessions copies every escrow from an epoch-layout SQLite store
// into dest (typically Postgres) through the public Storage API. Idempotent:
// already-copied sessions are verified and missing diffs/signatures are
// replayed; conflicting destination rows abort the migration.
//
// Escrows are migrated concurrently with a small worker pool
// (DEVSHARD_MIGRATE_WORKERS, default 4). Diffs are read and written in chunks
// (DEVSHARD_MIGRATE_DIFF_CHUNK, default 5000) to bound memory. Sealed inferences
// and validation obs are copied after the journal.
//
// Returns the number of escrows successfully migrated (including ones that
// were already fully present on dest). Boot must wait for this to finish
// before serving traffic.
func MigrateSQLiteSessions(src *SQLite, dest Storage) (int, error) {
	if src == nil {
		return 0, fmt.Errorf("migrate sqlite sessions: src is nil")
	}
	if dest == nil {
		return 0, fmt.Errorf("migrate sqlite sessions: dest is nil")
	}

	ids := src.EscrowIDs()
	sort.Strings(ids)
	if len(ids) == 0 {
		return 0, nil
	}

	workers := migrateWorkerCount()
	if workers > len(ids) {
		workers = len(ids)
	}

	var migrated atomic.Int64
	g := new(errgroup.Group)
	g.SetLimit(workers)
	for _, escrowID := range ids {
		escrowID := escrowID
		g.Go(func() error {
			if err := migrateOneSQLiteSession(src, dest, escrowID); err != nil {
				return fmt.Errorf("migrate escrow %s: %w", escrowID, err)
			}
			migrated.Add(1)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return int(migrated.Load()), err
	}
	return int(migrated.Load()), nil
}

func migrateWorkerCount() int {
	return positiveIntEnv("DEVSHARD_MIGRATE_WORKERS", defaultMigrateWorkers)
}

func migrateDiffChunkSize() uint64 {
	n := positiveIntEnv("DEVSHARD_MIGRATE_DIFF_CHUNK", defaultMigrateDiffChunk)
	return uint64(n)
}

func positiveIntEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func migrateOneSQLiteSession(src *SQLite, dest Storage, escrowID string) error {
	meta, err := src.GetSessionMeta(escrowID)
	if err != nil {
		return fmt.Errorf("read sqlite meta: %w", err)
	}

	version := meta.Version
	if version == "" {
		version = types.DefaultStateRootVersion
	}

	destMeta, destErr := dest.GetSessionMeta(escrowID)
	copiedThrough := uint64(0)
	switch {
	case destErr == nil:
		if destMeta.EpochID != meta.EpochID {
			return fmt.Errorf("%w: dest epoch %d, sqlite epoch %d",
				ErrSessionEpochConflict, destMeta.EpochID, meta.EpochID)
		}
		if destMeta.Version != "" && meta.Version != "" && destMeta.Version != meta.Version {
			return fmt.Errorf("%w: dest version %s, sqlite version %s",
				ErrSessionVersionConflict, destMeta.Version, meta.Version)
		}
		copiedThrough = destMeta.LatestNonce
	case errors.Is(destErr, ErrSessionNotFound):
		if err := dest.CreateSession(CreateSessionParams{
			EscrowID:       meta.EscrowID,
			EpochID:        meta.EpochID,
			Version:        version,
			CreatorAddr:    meta.CreatorAddr,
			Config:         meta.Config,
			Group:          meta.Group,
			InitialBalance: meta.InitialBalance,
		}); err != nil {
			return fmt.Errorf("create session: %w", err)
		}
	default:
		return fmt.Errorf("read dest meta: %w", destErr)
	}

	if err := migrateDiffsChunked(src, dest, escrowID, meta.LatestNonce, copiedThrough); err != nil {
		return err
	}

	if meta.LastFinalized > 0 {
		destFinalized, err := dest.LastFinalized(escrowID)
		if err != nil {
			return fmt.Errorf("read dest finalized: %w", err)
		}
		if destFinalized < meta.LastFinalized {
			if err := dest.MarkFinalized(escrowID, meta.LastFinalized); err != nil {
				return fmt.Errorf("mark finalized: %w", err)
			}
		}
	}

	if meta.Status == "settled" {
		destMeta, err := dest.GetSessionMeta(escrowID)
		if err != nil {
			return fmt.Errorf("re-read dest meta: %w", err)
		}
		if destMeta.Status != "settled" {
			if err := dest.MarkSettled(escrowID); err != nil {
				return fmt.Errorf("mark settled: %w", err)
			}
		}
	}

	if snapNonce, snapData, err := src.LoadSnapshot(escrowID); err == nil {
		destNonce, destData, destErr := dest.LoadSnapshot(escrowID)
		switch {
		case errors.Is(destErr, ErrSnapshotNotFound):
			if err := dest.SaveSnapshot(escrowID, snapNonce, snapData); err != nil {
				return fmt.Errorf("save snapshot: %w", err)
			}
		case destErr == nil:
			if destNonce != snapNonce {
				return fmt.Errorf("snapshot nonce conflict for %s: dest %d sqlite %d", escrowID, destNonce, snapNonce)
			}
			if string(destData) != string(snapData) {
				return fmt.Errorf("snapshot data conflict for %s nonce %d", escrowID, snapNonce)
			}
		default:
			return fmt.Errorf("read dest snapshot: %w", destErr)
		}
	} else if !errors.Is(err, ErrSnapshotNotFound) {
		return fmt.Errorf("read sqlite snapshot: %w", err)
	}

	if err := migrateSealedAndObs(src, dest, escrowID); err != nil {
		return err
	}
	return nil
}

func migrateDiffsChunked(src *SQLite, dest Storage, escrowID string, latestNonce, copiedThrough uint64) error {
	if latestNonce == 0 {
		return nil
	}
	chunk := migrateDiffChunkSize()
	if chunk == 0 {
		chunk = defaultMigrateDiffChunk
	}

	// Verify already-copied prefix in chunks.
	for from := uint64(1); from <= copiedThrough && from <= latestNonce; from += chunk {
		to := from + chunk - 1
		if to > copiedThrough {
			to = copiedThrough
		}
		if to > latestNonce {
			to = latestNonce
		}
		diffs, err := src.GetDiffs(escrowID, from, to)
		if err != nil {
			return fmt.Errorf("read sqlite diffs [%d,%d]: %w", from, to, err)
		}
		for _, rec := range diffs {
			existing, err := dest.GetDiffs(escrowID, rec.Nonce, rec.Nonce)
			if err != nil {
				return fmt.Errorf("read dest diff nonce %d: %w", rec.Nonce, err)
			}
			if len(existing) != 1 {
				return fmt.Errorf("expected one copied diff for %s nonce %d, got %d", escrowID, rec.Nonce, len(existing))
			}
			if err := verifyMigratedDiff(escrowID, rec, existing[0]); err != nil {
				return err
			}
			for slotID, sig := range rec.Signatures {
				if err := dest.AddSignature(escrowID, rec.Nonce, slotID, sig); err != nil {
					return fmt.Errorf("replay sig nonce %d slot %d: %w", rec.Nonce, slotID, err)
				}
			}
		}
	}

	// Append remaining diffs in chunks.
	start := copiedThrough + 1
	if start < 1 {
		start = 1
	}
	for from := start; from <= latestNonce; from += chunk {
		to := from + chunk - 1
		if to > latestNonce {
			to = latestNonce
		}
		diffs, err := src.GetDiffs(escrowID, from, to)
		if err != nil {
			return fmt.Errorf("read sqlite diffs [%d,%d]: %w", from, to, err)
		}
		if err := appendMigratedDiffs(dest, escrowID, diffs); err != nil {
			return err
		}
	}
	return nil
}

func migrateSealedAndObs(src *SQLite, dest Storage, escrowID string) error {
	sealed, err := src.listSealedInferences(escrowID)
	if err != nil {
		return fmt.Errorf("list sealed inferences: %w", err)
	}
	for _, row := range sealed {
		existing, ok, err := dest.GetSealedInference(escrowID, row.InferenceID)
		if err != nil {
			return fmt.Errorf("read dest sealed %d: %w", row.InferenceID, err)
		}
		if ok {
			if existing.SealedNonce != row.SealedNonce {
				return fmt.Errorf("sealed inference conflict %s/%d: dest nonce %d sqlite %d",
					escrowID, row.InferenceID, existing.SealedNonce, row.SealedNonce)
			}
			continue
		}
		if err := dest.InsertSealedInference(escrowID, row); err != nil {
			return fmt.Errorf("insert sealed %d: %w", row.InferenceID, err)
		}
	}

	live, err := src.listValidationObs(escrowID, "inference_validation_obs")
	if err != nil {
		return fmt.Errorf("list live validation obs: %w", err)
	}
	sealedObs, err := src.listValidationObs(escrowID, "sealed_validation_obs")
	if err != nil {
		return fmt.Errorf("list sealed validation obs: %w", err)
	}
	if len(live) == 0 && len(sealedObs) == 0 {
		return nil
	}
	importer, ok := dest.(validationObsImporter)
	if !ok {
		return fmt.Errorf("dest %T does not support validation obs import", dest)
	}
	if err := importer.ImportValidationObs(escrowID, live, sealedObs); err != nil {
		return fmt.Errorf("import validation obs: %w", err)
	}
	return nil
}

func appendMigratedDiffs(dest Storage, escrowID string, diffs []types.DiffRecord) error {
	if len(diffs) == 0 {
		return nil
	}
	if batch, ok := dest.(diffBatchAppender); ok {
		if err := batch.AppendDiffs(escrowID, diffs); err != nil {
			return fmt.Errorf("batch append diffs: %w", err)
		}
		return nil
	}
	for _, rec := range diffs {
		if err := dest.AppendDiff(escrowID, rec); err != nil {
			return fmt.Errorf("append diff nonce %d: %w", rec.Nonce, err)
		}
	}
	return nil
}

func quarantineSQLiteArtifacts(storeDir string) error {
	stamp := time.Now().Unix()
	meta := MetaDBPath(storeDir)
	if _, err := os.Stat(meta); err == nil {
		target := fmt.Sprintf("%s.migrated.%d", meta, stamp)
		if err := os.Rename(meta, target); err != nil {
			return fmt.Errorf("rename meta: %w", err)
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			sidecar := meta + suffix
			if _, err := os.Stat(sidecar); err == nil {
				_ = os.Rename(sidecar, target+suffix)
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	entries, err := os.ReadDir(storeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, ent := range entries {
		if ent.IsDir() || !epochFileRegex.MatchString(ent.Name()) {
			continue
		}
		path := filepath.Join(storeDir, ent.Name())
		target := fmt.Sprintf("%s.migrated.%d", path, stamp)
		if err := os.Rename(path, target); err != nil {
			return fmt.Errorf("rename %s: %w", ent.Name(), err)
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			sidecar := path + suffix
			if _, err := os.Stat(sidecar); err == nil {
				_ = os.Rename(sidecar, target+suffix)
			}
		}
	}
	slog.Info("devshard storage: quarantined sqlite artifacts after HA migrate", "dir", storeDir)
	return nil
}
