//go:build stress

package storage

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	stressEpochs       = 12
	stressSessions     = 6
	stressDiffsPerSess = 20
	stressCutoff       = 8
)

func TestStressStorageMultiEpochPruning(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		dir := t.TempDir()
		db, err := NewSQLite(dir)
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })

		populatePruneStressStore(t, db)
		require.NoError(t, db.pruneBefore(stressCutoff))

		verifyPruneStressStore(t, db)
		verifySQLiteStressFiles(t, dir)
	})

	t.Run("postgres", func(t *testing.T) {
		pg := newTestPostgres(t)

		populatePruneStressStore(t, pg)
		require.NoError(t, pg.pruneBefore(stressCutoff))

		verifyPruneStressStore(t, pg)
		verifyPostgresStressPartitions(t, pg)
	})
}

func populatePruneStressStore(t *testing.T, store Storage) {
	t.Helper()

	for epoch := uint64(1); epoch <= stressEpochs; epoch++ {
		for session := 0; session < stressSessions; session++ {
			escrowID := stressEscrowID(epoch, session)
			require.NoError(t, store.CreateSession(paramsForEpoch(escrowID, epoch)))
			for nonce := uint64(1); nonce <= stressDiffsPerSess; nonce++ {
				require.NoError(t, store.AppendDiff(escrowID, makeDiffRecord(nonce)))
				require.NoError(t, store.AddSignature(escrowID, nonce, 1, []byte{byte(epoch), byte(session), byte(nonce)}))
			}
			require.NoError(t, store.MarkFinalized(escrowID, stressDiffsPerSess/2))
		}
	}
}

func verifyPruneStressStore(t *testing.T, store Storage) {
	t.Helper()

	for epoch := uint64(1); epoch <= stressEpochs; epoch++ {
		for session := 0; session < stressSessions; session++ {
			escrowID := stressEscrowID(epoch, session)
			meta, err := store.GetSessionMeta(escrowID)
			if epoch < stressCutoff {
				require.ErrorIs(t, err, ErrSessionNotFound, "old escrow %s should be pruned", escrowID)
				continue
			}
			require.NoError(t, err, "recent escrow %s should survive", escrowID)
			require.Equal(t, epoch, meta.EpochID)
			require.Equal(t, uint64(stressDiffsPerSess), meta.LatestNonce)
			require.Equal(t, uint64(stressDiffsPerSess/2), meta.LastFinalized)

			diffs, err := store.GetDiffs(escrowID, 1, stressDiffsPerSess)
			require.NoError(t, err)
			require.Len(t, diffs, stressDiffsPerSess)
			for _, diff := range diffs {
				require.Len(t, diff.Signatures, 2)
			}
		}
	}
}

func verifySQLiteStressFiles(t *testing.T, dir string) {
	t.Helper()

	for epoch := uint64(1); epoch <= stressEpochs; epoch++ {
		dbPath := filepath.Join(dir, fmt.Sprintf("epoch_%d.db", epoch))
		if epoch < stressCutoff {
			require.NoFileExists(t, dbPath)
		} else {
			require.FileExists(t, dbPath)
		}
	}
}

func verifyPostgresStressPartitions(t *testing.T, pg *Postgres) {
	t.Helper()

	partitions := make(map[string]struct{})
	for _, name := range listDevshardPartitions(t, pg.pool) {
		partitions[name] = struct{}{}
	}

	for epoch := uint64(1); epoch <= stressEpochs; epoch++ {
		for _, name := range []string{
			pgSessionsPartition(epoch),
			pgDiffsPartition(epoch),
			pgSignaturesPartition(epoch),
		} {
			_, exists := partitions[name]
			if epoch < stressCutoff {
				require.False(t, exists, "old partition %s should be dropped", name)
			} else {
				require.True(t, exists, "recent partition %s should survive", name)
			}
		}
		expectedIndexRows := stressSessions
		if epoch < stressCutoff {
			expectedIndexRows = 0
		}
		require.Equal(t, expectedIndexRows, countSessionIndexRows(t, pg.pool, epoch))
	}
}

func stressEscrowID(epoch uint64, session int) string {
	return fmt.Sprintf("stress-e%02d-s%02d", epoch, session)
}
