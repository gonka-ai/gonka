package payloads

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"common/storage/mode"
)

const defaultPGRetryInterval = 240 * time.Second

// OpenConfig selects the payload backend for a devshardd child.
// Dir is typically {data-dir}/payloads (created if missing).
type OpenConfig struct {
	Dir string
	// CompressFiles gates writing zstd payload files. See NewCompressingFileStorage.
	CompressFiles bool
}

// Open creates the payload Storage for devshardd.
//
// Selection is driven by common/storage/mode (DEVSHARD_STORAGE_MODE, default auto):
//   - postgres: PGHOST required; Postgres-only; boot fails if unreachable.
//     No file fallback (avoids multi-instance split-brain). Migrates local
//     file payloads into Postgres at boot.
//   - hybrid: PGHOST required; Postgres primary with file fallback (lazy reconnect).
//   - sqlite: file storage only under Dir (PGHOST ignored).
//
// See devshard/docs/storage-design.md#storage-mode-selection.
func Open(ctx context.Context, cfg OpenConfig) (Storage, func(), error) {
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil { //nolint:gosec // the payload directory is read by the host beside this process.
		return nil, nil, fmt.Errorf("payloads: mkdir %s: %w", cfg.Dir, err)
	}

	storageMode, err := mode.Resolve()
	if err != nil {
		return nil, nil, fmt.Errorf("payloads: %w", err)
	}

	pgHost := os.Getenv("PGHOST")
	if storageMode.RequiresPGHOST() && pgHost == "" {
		return nil, nil, ErrSharedPostgresRequired
	}

	fileStore := NewFileStorage(cfg.Dir)
	if cfg.CompressFiles {
		fileStore = NewCompressingFileStorage(cfg.Dir)
	}

	switch storageMode {
	case mode.SQLite:
		slog.Info("payload storage: using file only", "dir", cfg.Dir, "mode", storageMode)
		return fileStore, func() {}, nil

	case mode.Postgres:
		pgStore, err := newPostgresStorage(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("payloads: postgres mode requires postgres at %s: %w", pgHost, err)
		}
		n, err := MigrateFilePayloadsToPostgres(ctx, cfg.Dir, pgStore)
		if err != nil {
			pgStore.Close()
			return nil, nil, fmt.Errorf("payloads: postgres mode migrate file payloads: %w", err)
		}
		if n > 0 {
			slog.Info("payload storage: migrated file payloads to postgres", "host", pgHost, "files", n)
		}
		slog.Info("payload storage: postgres-only", "host", pgHost, "mode", storageMode)
		return pgStore, pgStore.Close, nil

	case mode.Hybrid:
		pgStore, err := newPostgresStorage(ctx)
		if err != nil {
			slog.Warn("payload storage: postgres unavailable at boot, will retry on store",
				"host", pgHost, "error", err)
			hybrid := NewHybridStorage(nil, fileStore, pgRetryInterval())
			return hybrid, hybrid.Close, nil
		}
		slog.Info("payload storage: using postgres with file fallback", "host", pgHost, "mode", storageMode)
		hybrid := NewHybridStorage(pgStore, fileStore, pgRetryInterval())
		return hybrid, hybrid.Close, nil

	default:
		return nil, nil, fmt.Errorf("payloads: unhandled storage mode %q", storageMode)
	}
}

func pgRetryInterval() time.Duration {
	retryIntervalStr := os.Getenv("PG_RETRY_INTERVAL")
	retryInterval, err := time.ParseDuration(retryIntervalStr)
	if err != nil || retryInterval <= 0 {
		return defaultPGRetryInterval
	}
	return retryInterval
}
