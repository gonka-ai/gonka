package accounting

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"devshard/types"
)

// openWriterTracker opens a tracker that publishes its ledger rows under
// writerID, i.e. one HA instance.
func openWriterTracker(t *testing.T, writerID string) *Tracker {
	t.Helper()
	t.Setenv(accountingWriterIDEnv, writerID)
	tr, err := OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour)
	require.NoError(t, err)
	tr.now = func() time.Time { return accountingTestNow }
	return tr
}

func protocolOnlyTotal(t *testing.T, tr *Tracker, epoch uint64) uint64 {
	t.Helper()
	var total uint64
	for _, record := range tr.Query(QueryFilter{EpochIndex: epoch}) {
		total += record.Dispositions[DispositionProtocolOnly]
	}
	return total
}

// TestPostgresAccountingConcurrentWritersMergeCounters is the regression test
// for the last-writer-wins hazard: two instances holding the same escrow must
// add up in Postgres instead of overwriting each other's counts.
func TestPostgresAccountingConcurrentWritersMergeCounters(t *testing.T) {
	setupAccountingPostgres(t)
	ctx := context.Background()

	a := openWriterTracker(t, "writer-a")
	registerEscrow(t, a, "e-ha", 11, "m")
	require.NoError(t, a.RecordDiff("e-ha", 1, false))
	require.NoError(t, a.Flush(ctx))

	// B loads the escrow A already published, then counts its own nonce.
	b := openWriterTracker(t, "writer-b")
	require.NoError(t, b.RecordDiff("e-ha", 3, false))
	require.NoError(t, b.Flush(ctx))
	require.Equal(t, uint64(2), protocolOnlyTotal(t, b, 11), "B should see A's count plus its own")

	// A flushes again after B: its snapshot still only knows its own nonce, and
	// it must not restate the escrow over B's rows.
	require.NoError(t, a.RecordDiff("e-ha", 5, false))
	require.NoError(t, a.Flush(ctx))
	require.NoError(t, a.Close())
	require.NoError(t, b.Close())

	merged := openWriterTracker(t, "writer-c")
	defer merged.Close()
	require.Equal(t, uint64(3), protocolOnlyTotal(t, merged, 11), "every writer's counts must survive")

	// The totals survive because each instance owns its own rows, which is what
	// a reader sums; assert the partitioning itself, not just the sum.
	require.Equal(t, map[string]int64{"writer-a": 2, "writer-b": 1}, counterTotalsByWriter(t, ctx, "e-ha"))
}

func counterTotalsByWriter(t *testing.T, ctx context.Context, escrowID string) map[string]int64 {
	t.Helper()
	pool, err := pgxpool.New(ctx, "")
	require.NoError(t, err)
	defer pool.Close()
	rows, err := pool.Query(ctx, `
		SELECT writer_id, SUM(count) FROM accounting_escrow_counters
		WHERE escrow_id = $1 GROUP BY writer_id`, escrowID)
	require.NoError(t, err)
	defer rows.Close()
	out := make(map[string]int64)
	for rows.Next() {
		var (
			writerID string
			total    int64
		)
		require.NoError(t, rows.Scan(&writerID, &total))
		out[writerID] = total
	}
	require.NoError(t, rows.Err())
	return out
}

// TestPostgresAccountingFlushIsIdempotent covers the retry path: a flush that
// timed out after committing is replayed, and must not double count.
func TestPostgresAccountingFlushIsIdempotent(t *testing.T) {
	setupAccountingPostgres(t)
	ctx := context.Background()

	tr := openWriterTracker(t, "writer-retry")
	registerEscrow(t, tr, "e-retry", 12, "m")
	require.NoError(t, tr.RecordDiff("e-retry", 1, false))
	require.NoError(t, tr.Flush(ctx))

	// Replay the same state as if the first commit had been reported as failed.
	tr.mu.Lock()
	tr.dirty = map[string]struct{}{"e-retry": {}}
	tr.mu.Unlock()
	require.NoError(t, tr.Flush(ctx))
	require.NoError(t, tr.Close())

	reopened := openWriterTracker(t, "writer-retry")
	defer reopened.Close()
	require.Equal(t, uint64(1), protocolOnlyTotal(t, reopened, 12))
}

// TestPostgresAccountingWriterRestartKeepsOwnRows checks that a writer reading
// back its own rows does not re-publish them as new contributions.
func TestPostgresAccountingWriterRestartKeepsOwnRows(t *testing.T) {
	setupAccountingPostgres(t)
	ctx := context.Background()

	first := openWriterTracker(t, "writer-restart")
	registerEscrow(t, first, "e-restart", 13, "m")
	require.NoError(t, first.RecordDiff("e-restart", 1, false))
	require.NoError(t, first.Close())

	second := openWriterTracker(t, "writer-restart")
	require.NoError(t, second.RecordDiff("e-restart", 3, false))
	require.NoError(t, second.Flush(ctx))
	require.NoError(t, second.Close())

	reopened := openWriterTracker(t, "writer-restart")
	defer reopened.Close()
	require.Equal(t, uint64(2), protocolOnlyTotal(t, reopened, 13))
}

// TestPostgresAccountingHostStatsMergeByMax pins the non-additive rule: host
// stats mirror absolute chain numbers, so concurrent writers merge with max.
func TestPostgresAccountingHostStatsMergeByMax(t *testing.T) {
	setupAccountingPostgres(t)
	ctx := context.Background()

	a := openWriterTracker(t, "writer-a")
	registerEscrow(t, a, "e-stats", 14, "m")
	require.NoError(t, a.RecordHostStats("e-stats", 0, types.HostStats{Missed: 1, Cost: 10}))
	require.NoError(t, a.Flush(ctx))

	b := openWriterTracker(t, "writer-b")
	require.NoError(t, b.RecordHostStats("e-stats", 0, types.HostStats{Missed: 3, Cost: 4}))
	require.NoError(t, b.Flush(ctx))
	require.NoError(t, a.Close())
	require.NoError(t, b.Close())

	merged := openWriterTracker(t, "writer-c")
	defer merged.Close()
	stats := merged.escrows["e-stats"].HostStats[0]
	require.Equal(t, uint32(3), stats.Missed)
	require.Equal(t, uint64(10), stats.Cost)
}

// TestPostgresAccountingPruneRemovesEveryWriterRow checks that retention pruning
// drops the escrow from the ledger, peer rows included.
func TestPostgresAccountingPruneRemovesEveryWriterRow(t *testing.T) {
	setupAccountingPostgres(t)
	ctx := context.Background()

	peer := openWriterTracker(t, "writer-peer")
	registerEscrow(t, peer, "e-old", 1, "m")
	require.NoError(t, peer.RecordDiff("e-old", 1, false))
	require.NoError(t, peer.RecordPhase("e-old", EscrowSettled))
	require.NoError(t, peer.Flush(ctx))
	require.NoError(t, peer.Close())

	// retention=1 keeps only the newest epoch; the settled epoch-1 escrow goes.
	t.Setenv(accountingWriterIDEnv, "writer-pruner")
	pruner, err := OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 1, time.Hour)
	require.NoError(t, err)
	pruner.now = func() time.Time { return accountingTestNow }
	registerEscrow(t, pruner, "e-new", 2, "m")
	require.NoError(t, pruner.Flush(ctx))
	require.NoError(t, pruner.Close())

	pool, err := pgxpool.New(ctx, "")
	require.NoError(t, err)
	defer pool.Close()
	for _, table := range []string{
		"accounting_escrow_state",
		"accounting_escrow_counters",
		"accounting_escrow_slot_counts",
		"accounting_escrow_host_stats",
		"accounting_escrow_invalid_nonces",
	} {
		var n int
		require.NoError(t, pool.QueryRow(ctx,
			"SELECT COUNT(*) FROM "+table+" WHERE escrow_id = $1", "e-old").Scan(&n))
		require.Zerof(t, n, "%s still holds rows for the pruned escrow", table)
	}
}

// TestPostgresAccountingImportsLegacyBlobLedger covers the one-shot conversion
// of the previous one-blob-per-escrow layout.
func TestPostgresAccountingImportsLegacyBlobLedger(t *testing.T) {
	setupAccountingPostgres(t)
	ctx := context.Background()

	blob := escrowBlob{
		Meta: EscrowMetadata{
			EscrowID:             "e-legacy",
			CreationEpoch:        15,
			Model:                "m",
			Phase:                EscrowActive,
			RefusalTimeout:       60,
			ExecutionTimeout:     1200,
			TimeoutBufferSeconds: 5,
			Slots: []types.SlotAssignment{
				{SlotID: 0, ValidatorAddress: "p0"},
				{SlotID: 1, ValidatorAddress: "p1"},
			},
		},
		Latest:          7,
		HostStats:       map[uint32]types.HostStats{0: {Missed: 2}},
		Counters:        []counterBlob{{Key: CounterKey{SlotID: 0, Disposition: DispositionProtocolOnly}, Count: 4}},
		ChallengeBySlot: map[uint32]uint64{1: 1},
		InvalidBySlot:   map[uint32]uint64{1: 1},
		InvalidNonces:   []uint64{3},
	}
	raw, err := encodeEscrowBlob(blob)
	require.NoError(t, err)

	seed, err := pgxpool.New(ctx, "")
	require.NoError(t, err)
	_, err = seed.Exec(ctx, `CREATE TABLE IF NOT EXISTS accounting_escrows (
		escrow_id TEXT PRIMARY KEY,
		creation_epoch BIGINT NOT NULL,
		model TEXT NOT NULL,
		payload BYTEA NOT NULL
	)`)
	require.NoError(t, err)
	_, err = seed.Exec(ctx,
		`INSERT INTO accounting_escrows (escrow_id, creation_epoch, model, payload) VALUES ($1, $2, $3, $4)`,
		"e-legacy", 15, "m", raw)
	require.NoError(t, err)
	seed.Close()

	imported := openWriterTracker(t, "writer-import")
	require.Equal(t, uint64(4), protocolOnlyTotal(t, imported, 15))
	escrow := imported.escrows["e-legacy"]
	require.Equal(t, uint64(7), escrow.Latest)
	require.Equal(t, uint32(2), escrow.HostStats[0].Missed)
	require.Equal(t, uint64(1), escrow.ChallengeBySlot[1])
	require.Contains(t, escrow.InvalidNonce, uint64(3))

	// The blob table is drained, and the writer that comes next adds to the
	// imported numbers instead of restating them.
	require.NoError(t, imported.RecordDiff("e-legacy", 9, false))
	require.NoError(t, imported.Flush(ctx))
	require.NoError(t, imported.Close())

	reopened := openWriterTracker(t, "writer-import")
	defer reopened.Close()
	require.Equal(t, uint64(5), protocolOnlyTotal(t, reopened, 15))
}
