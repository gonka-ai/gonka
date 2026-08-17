package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"

	_ "modernc.org/sqlite"
)

// writeLegacyDB builds a legacy single-file SQLite database with the pre-epoch
// schema and a couple of sessions+diffs. Returns the file path.
func writeLegacyDB(t *testing.T, sessions []legacyTestSession) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "devshard.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()

	const schema = `
	CREATE TABLE sessions (
		escrow_id       TEXT PRIMARY KEY,
		version         TEXT,
		creator_addr    TEXT NOT NULL,
		config_json     TEXT NOT NULL,
		group_json      TEXT NOT NULL,
		initial_balance INTEGER NOT NULL,
		latest_nonce    INTEGER NOT NULL DEFAULT 0,
		last_finalized  INTEGER NOT NULL DEFAULT 0,
		status          TEXT NOT NULL DEFAULT 'active',
		settled_at      INTEGER
	);
	CREATE TABLE diffs (
		escrow_id       TEXT NOT NULL,
		nonce           INTEGER NOT NULL,
		txs_proto       BLOB NOT NULL,
		user_sig        BLOB,
		post_state_root BLOB,
		state_hash      BLOB,
		warm_keys_json  TEXT,
		created_at      INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (escrow_id, nonce)
	);
	CREATE TABLE signatures (
		escrow_id TEXT NOT NULL,
		nonce     INTEGER NOT NULL,
		slot_id   INTEGER NOT NULL,
		sig       BLOB NOT NULL,
		PRIMARY KEY (escrow_id, nonce, slot_id)
	);`
	_, err = db.Exec(schema)
	require.NoError(t, err)

	for _, s := range sessions {
		_, err := db.Exec(
			`INSERT INTO sessions (escrow_id, version, creator_addr, config_json, group_json, initial_balance, latest_nonce, last_finalized, status)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.escrowID, s.version, "creator", "{}", "[{\"slot_id\":0,\"validator_address\":\"a\"},{\"slot_id\":1,\"validator_address\":\"b\"}]",
			s.balance, s.latestNonce, s.lastFinalized, s.status,
		)
		require.NoError(t, err)

		for n := uint64(1); n <= s.latestNonce; n++ {
			proto, err := marshalTxs(nil)
			require.NoError(t, err)
			_, err = db.Exec(
				`INSERT INTO diffs (escrow_id, nonce, txs_proto, state_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
				s.escrowID, n, proto, []byte{byte(n)}, int64(n),
			)
			require.NoError(t, err)
			_, err = db.Exec(
				`INSERT INTO signatures (escrow_id, nonce, slot_id, sig) VALUES (?, ?, ?, ?)`,
				s.escrowID, n, 0, []byte{byte(n)},
			)
			require.NoError(t, err)
		}
	}
	return path
}

type legacyTestSession struct {
	escrowID, version, status string
	balance                   uint64
	latestNonce               uint64
	lastFinalized             uint64
}

// failAfterAppendStorage simulates the crash window where the destination write
// committed but migration returned an error before renaming the legacy file.
type failAfterAppendStorage struct {
	Storage
	failAfter int
	appends   int
}

func (s *failAfterAppendStorage) AppendDiff(escrowID string, rec types.DiffRecord) error {
	if err := s.Storage.AppendDiff(escrowID, rec); err != nil {
		return err
	}
	s.appends++
	if s.appends == s.failAfter {
		return fmt.Errorf("forced append failure after write")
	}
	return nil
}

func TestMigrateLegacy_RoundTrip(t *testing.T) {
	legacyPath := writeLegacyDB(t, []legacyTestSession{
		{escrowID: "esc-a", version: types.DevshardStateRootAndProtocolVersion, status: "active", balance: 1000, latestNonce: 3, lastFinalized: 1},
		{escrowID: "esc-b", version: types.DevshardStateRootAndProtocolVersion, status: "active", balance: 2000, latestNonce: 5, lastFinalized: 2},
		{escrowID: "esc-settled", version: types.DevshardStateRootAndProtocolVersion, status: "settled", balance: 500, latestNonce: 1, lastFinalized: 1},
	})

	dest := NewMemory()

	resolve := func(escrowID string) (uint64, error) {
		switch escrowID {
		case "esc-a":
			return 11, nil
		case "esc-b":
			return 12, nil
		case "esc-settled":
			return 11, nil
		}
		return 0, fmt.Errorf("unknown escrow %s", escrowID)
	}

	n, err := MigrateLegacySQLite(legacyPath, dest, resolve)
	require.NoError(t, err)
	require.Equal(t, 3, n)

	// Legacy file is gone, replaced by stamped backup.
	_, err = os.Stat(legacyPath)
	require.True(t, os.IsNotExist(err), "legacy file should be moved")
	matches, err := filepath.Glob(legacyPath + ".migrated.*")
	require.NoError(t, err)
	require.Len(t, matches, 1)

	// esc-a moved to epoch 11 with diffs + finalized state.
	metaA, err := dest.GetSessionMeta("esc-a")
	require.NoError(t, err)
	require.Equal(t, uint64(11), metaA.EpochID)
	require.Equal(t, uint64(3), metaA.LatestNonce)
	require.Equal(t, uint64(1), metaA.LastFinalized)
	require.Equal(t, "active", metaA.Status)

	diffsA, err := dest.GetDiffs("esc-a", 1, 3)
	require.NoError(t, err)
	require.Len(t, diffsA, 3)
	for i, d := range diffsA {
		require.Equal(t, uint64(i+1), d.Nonce)
		require.Len(t, d.Signatures, 1)
	}

	// esc-b in a different epoch -- partition isolation works.
	metaB, err := dest.GetSessionMeta("esc-b")
	require.NoError(t, err)
	require.Equal(t, uint64(12), metaB.EpochID)
	require.Equal(t, uint64(5), metaB.LatestNonce)

	// Settled session preserved its status.
	metaS, err := dest.GetSessionMeta("esc-settled")
	require.NoError(t, err)
	require.Equal(t, "settled", metaS.Status)

	// Pruning epoch 11 must drop both esc-a and esc-settled but leave esc-b.
	require.NoError(t, dest.PruneEpoch(11))
	_, err = dest.GetSessionMeta("esc-a")
	require.Error(t, err)
	_, err = dest.GetSessionMeta("esc-settled")
	require.Error(t, err)
	_, err = dest.GetSessionMeta("esc-b")
	require.NoError(t, err)
}

// TestMigrateLegacy_NormalizesEmptyVersion exercises legacy migration: a SQLite
// row with an empty version column is stamped with DefaultStateRootVersion.
// before CreateSession so the destination store carries an explicit tag.
func TestMigrateLegacy_NormalizesEmptyVersion(t *testing.T) {
	legacyPath := writeLegacyDB(t, []legacyTestSession{
		{escrowID: "no-ver", version: "", status: "active", balance: 1000, latestNonce: 2, lastFinalized: 1},
	})

	dest := NewMemory()
	resolve := func(string) (uint64, error) { return 9, nil }

	n, err := MigrateLegacySQLite(legacyPath, dest, resolve)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	meta, err := dest.GetSessionMeta("no-ver")
	require.NoError(t, err)
	require.Equal(t, types.DefaultStateRootVersion, meta.Version,
		"legacy empty version must be stamped with DefaultStateRootVersion")
	require.Equal(t, uint64(9), meta.EpochID)
	require.Equal(t, uint64(2), meta.LatestNonce)
	require.Equal(t, uint64(1), meta.LastFinalized)

	diffs, err := dest.GetDiffs("no-ver", 1, 2)
	require.NoError(t, err)
	require.Len(t, diffs, 2)
}

func TestMigrateLegacy_NoFile_NoOp(t *testing.T) {
	dest := NewMemory()
	n, err := MigrateLegacySQLite(filepath.Join(t.TempDir(), "missing.db"), dest, func(string) (uint64, error) { return 0, nil })
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

func TestMigrateLegacy_DirPath_NoOp(t *testing.T) {
	// If the path is already a directory (e.g. caller passed the new layout
	// path by accident), migration silently skips.
	dir := t.TempDir()
	dest := NewMemory()
	n, err := MigrateLegacySQLite(dir, dest, func(string) (uint64, error) { return 0, nil })
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

func TestMigrateLegacy_SkipsUnknownEscrow(t *testing.T) {
	legacyPath := writeLegacyDB(t, []legacyTestSession{
		{escrowID: "good", version: types.DevshardStateRootAndProtocolVersion, status: "active", balance: 1, latestNonce: 1},
		{escrowID: "stale", version: types.DevshardStateRootAndProtocolVersion, status: "active", balance: 1, latestNonce: 1},
	})

	dest := NewMemory()
	resolve := func(escrowID string) (uint64, error) {
		if escrowID == "good" {
			return 5, nil
		}
		return 0, fmt.Errorf("%w: escrow %s no longer on chain", ErrSkipLegacySession, escrowID)
	}

	n, err := MigrateLegacySQLite(legacyPath, dest, resolve)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	_, err = dest.GetSessionMeta("good")
	require.NoError(t, err)
	_, err = dest.GetSessionMeta("stale")
	require.Error(t, err)

	_, err = os.Stat(legacyPath)
	require.True(t, os.IsNotExist(err), "legacy file should be moved after successful skip")
}

func TestMigrateLegacy_ResolverErrorKeepsLegacyFile(t *testing.T) {
	legacyPath := writeLegacyDB(t, []legacyTestSession{
		{escrowID: "good", version: types.DevshardStateRootAndProtocolVersion, status: "active", balance: 1, latestNonce: 1},
		{escrowID: "rpc-fails", version: types.DevshardStateRootAndProtocolVersion, status: "active", balance: 1, latestNonce: 1},
	})

	dest := NewMemory()
	resolve := func(escrowID string) (uint64, error) {
		if escrowID == "good" {
			return 5, nil
		}
		return 0, fmt.Errorf("temporary chain query failure")
	}

	n, err := MigrateLegacySQLite(legacyPath, dest, resolve)
	require.Error(t, err)
	require.Equal(t, 0, n)

	_, statErr := os.Stat(legacyPath)
	require.NoError(t, statErr, "legacy file must remain for retry")
	matches, globErr := filepath.Glob(legacyPath + ".migrated.*")
	require.NoError(t, globErr)
	require.Empty(t, matches)

	_, err = dest.GetSessionMeta("good")
	require.ErrorIs(t, err, ErrSessionNotFound)
}

func TestMigrateLegacy_RetryAfterPartialDiffCopy(t *testing.T) {
	legacyPath := writeLegacyDB(t, []legacyTestSession{
		{escrowID: "retry", version: types.DevshardStateRootAndProtocolVersion, status: "active", balance: 1, latestNonce: 3, lastFinalized: 2},
	})

	dest := NewMemory()
	failing := &failAfterAppendStorage{Storage: dest, failAfter: 1}
	resolve := func(string) (uint64, error) { return 5, nil }

	n, err := MigrateLegacySQLite(legacyPath, failing, resolve)
	require.Error(t, err)
	require.Equal(t, 0, n)

	_, statErr := os.Stat(legacyPath)
	require.NoError(t, statErr, "legacy file must remain for retry")

	n, err = MigrateLegacySQLite(legacyPath, dest, resolve)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	meta, err := dest.GetSessionMeta("retry")
	require.NoError(t, err)
	require.Equal(t, uint64(3), meta.LatestNonce)
	require.Equal(t, uint64(2), meta.LastFinalized)
	diffs, err := dest.GetDiffs("retry", 1, 3)
	require.NoError(t, err)
	require.Len(t, diffs, 3)
}

func TestMigrateLegacy_RetryAfterFullCopyBeforeRename(t *testing.T) {
	legacyPath := writeLegacyDB(t, []legacyTestSession{
		{escrowID: "renamed-late", version: types.DevshardStateRootAndProtocolVersion, status: "settled", balance: 1, latestNonce: 2, lastFinalized: 1},
	})

	dest := NewMemory()
	resolve := func(string) (uint64, error) { return 6, nil }

	renamedLate := paramsForEpoch("renamed-late", 6)
	renamedLate.Version = types.DevshardStateRootAndProtocolVersion
	require.NoError(t, dest.CreateSession(renamedLate))
	require.NoError(t, dest.AppendDiff("renamed-late", types.DiffRecord{
		Diff:       types.Diff{Nonce: 1},
		StateHash:  []byte{1},
		CreatedAt:  1,
		Signatures: map[uint32][]byte{0: {1}},
	}))
	require.NoError(t, dest.AppendDiff("renamed-late", types.DiffRecord{
		Diff:       types.Diff{Nonce: 2},
		StateHash:  []byte{2},
		CreatedAt:  2,
		Signatures: map[uint32][]byte{0: {2}},
	}))
	require.NoError(t, dest.MarkFinalized("renamed-late", 1))

	n, err := MigrateLegacySQLite(legacyPath, dest, resolve)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	meta, err := dest.GetSessionMeta("renamed-late")
	require.NoError(t, err)
	require.Equal(t, "settled", meta.Status)
	_, err = os.Stat(legacyPath)
	require.True(t, os.IsNotExist(err), "retry should move legacy file after verifying copied data")
}

func TestMigrateLegacy_DetectsConflictingCopiedDiff(t *testing.T) {
	legacyPath := writeLegacyDB(t, []legacyTestSession{
		{escrowID: "conflict", version: types.DevshardStateRootAndProtocolVersion, status: "active", balance: 1, latestNonce: 1},
	})

	dest := NewMemory()
	conflictParams := paramsForEpoch("conflict", 7)
	conflictParams.Version = types.DevshardStateRootAndProtocolVersion
	require.NoError(t, dest.CreateSession(conflictParams))
	conflicting := makeDiffRecord(1)
	conflicting.StateHash = []byte("different")
	require.NoError(t, dest.AppendDiff("conflict", conflicting))

	n, err := MigrateLegacySQLite(legacyPath, dest, func(string) (uint64, error) { return 7, nil })
	require.ErrorContains(t, err, "migrated diff conflict")
	require.Equal(t, 0, n)
}
