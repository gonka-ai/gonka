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

// TestPostgresAccountingDisjointWritersKeepEveryNonce covers two instances that
// each observe a different part of the diff stream: every nonce has to survive,
// so nothing may overwrite or drop a peer's observation.
func TestPostgresAccountingDisjointWritersKeepEveryNonce(t *testing.T) {
	setupAccountingPostgres(t)
	ctx := context.Background()

	a := openWriterTracker(t, "writer-a")
	registerEscrow(t, a, "e-ha", 11, "m")
	require.NoError(t, a.RecordDiff("e-ha", 1, false))
	require.NoError(t, a.Flush(ctx))

	// B loads the escrow A already published, then observes its own nonce.
	b := openWriterTracker(t, "writer-b")
	require.NoError(t, b.RecordDiff("e-ha", 3, false))
	require.NoError(t, b.Flush(ctx))
	require.Equal(t, uint64(2), protocolOnlyTotal(t, b, 11), "B should see A's nonce plus its own")

	// A flushes again after B. Its snapshot still knows only its own nonces, so a
	// rule that published a total instead of a set would drop B's nonce here.
	require.NoError(t, a.RecordDiff("e-ha", 5, false))
	require.NoError(t, a.Flush(ctx))
	require.NoError(t, a.Close())
	require.NoError(t, b.Close())

	merged := openWriterTracker(t, "writer-c")
	defer merged.Close()
	require.Equal(t, uint64(3), protocolOnlyTotal(t, merged, 11), "every observation must survive")
	require.Equal(t, []uint64{1, 3, 5}, protocolNonces(t, ctx, "e-ha"))
}

// TestPostgresAccountingOverlappingWritersCountNonceOnce is the other half: a
// protocol-only nonce is read off the committed diff, so two live instances both
// see the same one. It must be counted once, not once per instance.
func TestPostgresAccountingOverlappingWritersCountNonceOnce(t *testing.T) {
	setupAccountingPostgres(t)
	ctx := context.Background()

	a := openWriterTracker(t, "writer-a")
	registerEscrow(t, a, "e-overlap", 21, "m")
	require.NoError(t, a.RecordDiff("e-overlap", 1, false))
	require.NoError(t, a.Flush(ctx))

	// B picks the escrow up from the ledger rather than registering it again.
	b := openWriterTracker(t, "writer-b")

	// Nonce 3 commits on chain while both are live: each observes it once.
	require.NoError(t, a.RecordDiff("e-overlap", 3, false))
	require.NoError(t, b.RecordDiff("e-overlap", 3, false))
	require.NoError(t, a.Flush(ctx))
	require.NoError(t, b.Flush(ctx))
	require.NoError(t, a.Close())
	require.NoError(t, b.Close())

	merged := openWriterTracker(t, "writer-c")
	defer merged.Close()
	require.Equal(t, uint64(2), protocolOnlyTotal(t, merged, 21),
		"nonces 1 and 3 were consumed, so the total is 2 no matter how many instances saw them")
	require.Equal(t, []uint64{1, 3}, protocolNonces(t, ctx, "e-overlap"))
}

// TestPostgresAccountingRequestLocalCountersSum pins the other merge rule: a
// disposition needs a local dispatch, so two instances hold disjoint sets by
// construction and their per-writer rows are summed.
func TestPostgresAccountingRequestLocalCountersSum(t *testing.T) {
	setupAccountingPostgres(t)
	ctx := context.Background()

	a := openWriterTracker(t, "writer-a")
	registerEscrow(t, a, "e-local", 31, "m")
	require.NoError(t, a.RecordDiff("e-local", 1, true))
	require.NoError(t, a.RecordGhost("e-local", 1, PhaseNormal, QuarantineNone, NoSendParticipantThrottled, ""))
	require.NoError(t, a.Flush(ctx))

	b := openWriterTracker(t, "writer-b")
	require.NoError(t, b.RecordDiff("e-local", 3, true))
	require.NoError(t, b.RecordGhost("e-local", 3, PhaseNormal, QuarantineNone, NoSendParticipantThrottled, ""))
	require.NoError(t, b.Flush(ctx))
	require.NoError(t, a.Close())
	require.NoError(t, b.Close())

	merged := openWriterTracker(t, "writer-c")
	defer merged.Close()
	var ghosts uint64
	for _, record := range merged.Query(QueryFilter{EpochIndex: 31}) {
		ghosts += record.Dispositions[DispositionGhost]
	}
	require.Equal(t, uint64(2), ghosts, "each instance ghosted a different nonce")
	require.Equal(t, map[string]int64{"writer-a": 1, "writer-b": 1}, counterTotalsByWriter(t, ctx, "e-local"))
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

// protocolNonces returns the persisted protocol-only nonce set, which carries no
// writer id: the row is the observation, however many instances saw it.
func protocolNonces(t *testing.T, ctx context.Context, escrowID string) []uint64 {
	t.Helper()
	pool, err := pgxpool.New(ctx, "")
	require.NoError(t, err)
	defer pool.Close()
	rows, err := pool.Query(ctx,
		`SELECT nonce FROM accounting_escrow_protocol_nonces WHERE escrow_id = $1 ORDER BY nonce`, escrowID)
	require.NoError(t, err)
	defer rows.Close()
	var out []uint64
	for rows.Next() {
		var nonce int64
		require.NoError(t, rows.Scan(&nonce))
		out = append(out, uint64(nonce))
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
		"accounting_escrow_protocol_nonces",
		"accounting_escrow_challenges",
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
	// The blob's invalid nonces are kept for deduplication only: their count is
	// already in InvalidBySlot, so promoting them into the set would double it.
	require.Contains(t, escrow.InvalidLegacy, uint64(3))
	require.NotContains(t, escrow.Invalid, uint64(3))

	// A verdict repeating that invalidation after the conversion must not count
	// it a second time.
	require.NoError(t, imported.RecordProtocol("e-legacy", 3, 1, ProtocolInvalidated, types.HostStats{}))
	require.Empty(t, escrow.Invalid, "a legacy invalid nonce must not be re-counted into the set")
	require.Equal(t, uint64(1), invalidTotal(t, imported, 15))

	// The blob table is drained, and the writer that comes next adds to the
	// imported numbers instead of restating them.
	require.NoError(t, imported.RecordDiff("e-legacy", 9, false))
	require.NoError(t, imported.Flush(ctx))
	require.NoError(t, imported.Close())

	reopened := openWriterTracker(t, "writer-import")
	defer reopened.Close()
	require.Equal(t, uint64(5), protocolOnlyTotal(t, reopened, 15))
	require.Equal(t, uint64(1), invalidTotal(t, reopened, 15))
}

func invalidTotal(t *testing.T, tr *Tracker, epoch uint64) uint64 {
	t.Helper()
	var total uint64
	for _, record := range tr.Query(QueryFilter{EpochIndex: epoch}) {
		total += record.CrossChecks.RecordedInvalid
	}
	return total
}
