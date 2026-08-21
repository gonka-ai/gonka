package accounting

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newModeTestTracker() *Tracker {
	return &Tracker{
		escrows: make(map[string]*escrowState),
		updated: accountingTestNow,
		now:     func() time.Time { return accountingTestNow },
	}
}

// TestAccountingHybridModeIsPostgresOnly verifies hybrid mode does not attach a
// SQLite runtime fallback — Postgres is required, same as postgres mode.
func TestAccountingHybridModeIsPostgresOnly(t *testing.T) {
	setupAccountingPostgres(t)
	t.Setenv("DEVSHARD_STORAGE_MODE", "hybrid")

	store, err := OpenStoreContext(context.Background(), filepath.Join(t.TempDir(), "accounting.db"), 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	_, ok := store.backend.(*postgresBackend)
	require.True(t, ok, "hybrid mode must use postgres-only backend")
}

// TestAccountingImportsSQLiteOnOpen covers the pre-existing SQLite ledger:
// a gateway that ran in sqlite mode and is restarted with PGHOST must find its
// escrows in Postgres.
func TestAccountingImportsSQLiteOnOpen(t *testing.T) {
	setupAccountingPostgres(t)
	sqlitePath := filepath.Join(t.TempDir(), "accounting.db")

	t.Setenv("DEVSHARD_STORAGE_MODE", "sqlite")
	seeded, err := OpenTracker(sqlitePath, 0, time.Hour)
	require.NoError(t, err)
	seeded.now = func() time.Time { return accountingTestNow }
	registerEscrow(t, seeded, "e-imported", 12, "m")
	require.NoError(t, seeded.RecordDiff("e-imported", 1, true))
	require.NoError(t, seeded.Close())

	t.Setenv("DEVSHARD_STORAGE_MODE", "hybrid")
	store, err := OpenStoreContext(context.Background(), sqlitePath, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	pg, ok := store.backend.(*postgresBackend)
	require.True(t, ok)
	loaded := newModeTestTracker()
	require.NoError(t, pg.Load(context.Background(), loaded))
	require.NotNil(t, loaded.escrows["e-imported"])
}

func TestAccountingModeRequiresPGHOST(t *testing.T) {
	for _, storageMode := range []string{"hybrid", "postgres"} {
		t.Run(storageMode, func(t *testing.T) {
			t.Setenv("DEVSHARD_STORAGE_MODE", storageMode)
			t.Setenv("PGHOST", "")

			store, err := OpenStoreContext(context.Background(), filepath.Join(t.TempDir(), "accounting.db"), 0)
			require.Error(t, err)
			require.Nil(t, store)
			require.Contains(t, err.Error(), "requires PGHOST")
		})
	}
}

func TestAccountingModeFailsClosedWhenPGDown(t *testing.T) {
	for _, storageMode := range []string{"hybrid", "postgres", ""} {
		t.Run("mode_"+storageMode, func(t *testing.T) {
			t.Setenv("DEVSHARD_STORAGE_MODE", storageMode)
			t.Setenv("PGHOST", "127.0.0.1")
			t.Setenv("PGPORT", "1")
			t.Setenv("PG_CONNECT_TIMEOUT", "100ms")

			store, err := OpenStoreContext(context.Background(), filepath.Join(t.TempDir(), "accounting.db"), 0)
			require.Error(t, err)
			require.Nil(t, store)
			require.Contains(t, err.Error(), "postgres required")
		})
	}
}

// TestAccountingFailedSaveKeepsDirtyState makes sure a rejected persist retries
// the same escrows on the next snapshot instead of dropping them.
func TestAccountingFailedSaveKeepsDirtyState(t *testing.T) {
	failing := &failingBackend{fail: true}
	store := &Store{backend: failing}
	tr := newModeTestTracker()
	registerEscrow(t, tr, "e-retry", 13, "m")

	require.Error(t, store.Save(context.Background(), tr))
	require.Equal(t, []string{"e-retry"}, failing.lastDirty)

	failing.fail = false
	require.NoError(t, store.Save(context.Background(), tr))
	require.Equal(t, []string{"e-retry"}, failing.lastDirty)
}

type failingBackend struct {
	fail        bool
	lastDirty   []string
	lastDeleted []string
}

func (f *failingBackend) Load(context.Context, *Tracker) error { return nil }

func (f *failingBackend) Save(_ context.Context, _ storeSnapshot, dirtyIDs, deletedIDs []string) error {
	f.lastDirty, f.lastDeleted = dirtyIDs, deletedIDs
	if f.fail {
		return errors.New("save rejected")
	}
	return nil
}

func (f *failingBackend) Close() error { return nil }
