package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const pgBoundFile = ".pg-bound"

// MetaDBPath returns the SQLite routing index path for a devshard store directory.
func MetaDBPath(storeDir string) string {
	return filepath.Join(storeDir, metaDBFile)
}

// PGBoundPath returns the Postgres-mode marker path for a devshard store directory.
func PGBoundPath(storeDir string) string {
	return filepath.Join(storeDir, pgBoundFile)
}

// HasSQLiteSessions reports whether storeDir still has SQLite-owned escrows.
// It returns false when _meta.db is missing or escrow_epoch has zero rows.
// Real I/O or schema errors are returned (not masked as false).
func HasSQLiteSessions(storeDir string) (bool, error) {
	path := MetaDBPath(storeDir)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return false, fmt.Errorf("open meta db: %w", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM escrow_epoch`).Scan(&count); err != nil {
		return false, fmt.Errorf("count escrow_epoch: %w", err)
	}
	return count > 0, nil
}

// HasSQLiteArtifacts reports whether storeDir contains files that could belong
// to a SQLite-backed store. Unlike NewSQLite, it never creates _meta.db.
func HasSQLiteArtifacts(storeDir string) (bool, error) {
	if _, err := os.Stat(MetaDBPath(storeDir)); err != nil {
		if !os.IsNotExist(err) {
			return false, err
		}
	} else {
		return true, nil
	}

	entries, err := os.ReadDir(storeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		if epochFileRegex.MatchString(ent.Name()) {
			return true, nil
		}
	}
	return false, nil
}

// ReadPGBound reports whether the Postgres-mode marker file exists.
func ReadPGBound(storeDir string) (bool, error) {
	_, err := os.Stat(PGBoundPath(storeDir))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// WritePGBound creates the Postgres-mode marker atomically.
func WritePGBound(storeDir string) error {
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return fmt.Errorf("mkdir store dir: %w", err)
	}
	target := PGBoundPath(storeDir)
	tmp := target + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open pg-bound tmp: %w", err)
	}
	if _, err := f.Write([]byte("1\n")); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write pg-bound tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync pg-bound tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close pg-bound tmp: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename pg-bound: %w", err)
	}
	dir, err := os.Open(storeDir)
	if err != nil {
		return fmt.Errorf("open store dir for sync: %w", err)
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return fmt.Errorf("sync store dir: %w", err)
	}
	if err := dir.Close(); err != nil {
		return fmt.Errorf("close store dir: %w", err)
	}
	return nil
}
