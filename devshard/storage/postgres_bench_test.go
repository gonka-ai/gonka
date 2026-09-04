package storage

import (
	"context"
	"fmt"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// benchPostgres starts one container for the whole benchmark function and
// returns a ready store with escrow-1 created. Sub-benchmarks share it and
// clean up their own rows, so the container cost is paid once.
//
// The container is reached over the Docker bridge, so its round trip is closer
// to a same-host TCP hop than to a production network. Numbers that turn on
// round trips are therefore optimistic here, not pessimistic.
func benchPostgres(b *testing.B) *Postgres {
	b.Helper()
	if testing.Short() {
		b.Skip("skipping postgres benchmarks in -short mode (requires Docker)")
	}
	ctx := context.Background()
	container, err := postgres.Run(ctx,
		"postgres:18.1-bookworm",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(postgresContainerWaitStrategy()),
	)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = container.Terminate(context.Background()) })

	host, err := container.Host(ctx)
	if err != nil {
		b.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		b.Fatal(err)
	}
	b.Setenv("PGHOST", host)
	b.Setenv("PGPORT", port.Port())
	b.Setenv("PGDATABASE", "testdb")
	b.Setenv("PGUSER", "testuser")
	b.Setenv("PGPASSWORD", "testpass")

	pg, err := NewPostgres(ctx)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = pg.Close() })
	if err := pg.WaitReady(ctx); err != nil {
		b.Fatal(err)
	}
	if err := pg.CreateSession(defaultParams()); err != nil {
		b.Fatal(err)
	}
	return pg
}

// postgresInsertSealedChunkedPerRow is the shape InsertSealedInferences had
// before the unnest upsert: a transaction per chunk, but still one statement —
// and so one round trip — per row. Kept here to price that difference.
func postgresInsertSealedChunkedPerRow(b *testing.B, s *Postgres, escrowID string, rows []InferenceRow) {
	b.Helper()
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		b.Fatal(err)
	}
	for start := 0; start < len(rows); start += sealedInferenceInsertChunk {
		end := start + sealedInferenceInsertChunk
		if end > len(rows) {
			end = len(rows)
		}
		ctx, cancel := s.opCtx()
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			cancel()
			b.Fatal(err)
		}
		for i := start; i < end; i++ {
			if err := postgresExecInsertSealedInference(ctx, tx, epochID, escrowID, rows[i]); err != nil {
				_ = tx.Rollback(ctx)
				cancel()
				b.Fatal(err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			cancel()
			b.Fatal(err)
		}
		cancel()
	}
}

func BenchmarkPostgresSealedInferenceIDs(b *testing.B) {
	db := benchPostgres(b)
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			if err := db.DeleteSealedInferences("escrow-1"); err != nil {
				b.Fatal(err)
			}
			if err := db.InsertSealedInferences("escrow-1", benchSealedRows(n, true)); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ids, err := db.SealedInferenceIDs("escrow-1")
				if err != nil {
					b.Fatal(err)
				}
				if len(ids) != n {
					b.Fatalf("got %d ids, want %d", len(ids), n)
				}
			}
		})
	}
}

// Pre-fix recovery: one autocommit statement per sealed id.
func BenchmarkPostgresSealedInferenceIndex_UnbatchedInsert(b *testing.B) {
	db := benchPostgres(b)
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			rows := benchSealedRows(n, false)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if err := db.DeleteSealedInferences("escrow-1"); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				for _, row := range rows {
					if err := db.InsertSealedInference("escrow-1", row); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

// The intermediate: chunked transactions, still a round trip per row.
func BenchmarkPostgresSealedInferenceIndex_ChunkedTxPerRow(b *testing.B) {
	db := benchPostgres(b)
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			rows := benchSealedRows(n, true)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if err := db.DeleteSealedInferences("escrow-1"); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				postgresInsertSealedChunkedPerRow(b, db, "escrow-1", rows)
			}
		})
	}
}

// Current: one unnest upsert per chunk.
func BenchmarkPostgresSealedInferenceIndex_BatchedInsert(b *testing.B) {
	db := benchPostgres(b)
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			rows := benchSealedRows(n, true)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if err := db.DeleteSealedInferences("escrow-1"); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if err := db.InsertSealedInferences("escrow-1", rows); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// The post-wipe load: COPY, no per-row conflict probe. This is the path the
// full-replay rebuild takes, and the only reason the upsert form above still
// exists is gap fill, where rows may already be present.
func BenchmarkPostgresSealedInferenceIndex_BulkInsert(b *testing.B) {
	db := benchPostgres(b)
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			rows := benchSealedRows(n, true)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if err := db.DeleteSealedInferences("escrow-1"); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if err := db.BulkInsertSealedInferences("escrow-1", rows); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkPostgresValidationObsDrain_PerID(b *testing.B) {
	db := benchPostgres(b)
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			ids := benchSealedIDs(n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if err := db.ClearValidationObs("escrow-1"); err != nil {
					b.Fatal(err)
				}
				if err := db.RecordValidationsAppliedOnce("escrow-1", benchObsEntries(n)); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				for _, id := range ids {
					if err := db.DrainInferenceValidationObs("escrow-1", id); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

func BenchmarkPostgresValidationObsDrain_Batched(b *testing.B) {
	db := benchPostgres(b)
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			ids := benchSealedIDs(n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if err := db.ClearValidationObs("escrow-1"); err != nil {
					b.Fatal(err)
				}
				if err := db.RecordValidationsAppliedOnce("escrow-1", benchObsEntries(n)); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if err := db.DrainInferenceValidationObsBatch("escrow-1", ids); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkPostgresValidationObsRecord_PerDiff(b *testing.B) {
	db := benchPostgres(b)
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if err := db.ClearValidationObs("escrow-1"); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				for j := 0; j < n; j++ {
					if err := db.RecordValidationsAppliedOnce("escrow-1", []ValidationObsEntry{
						{InferenceID: uint64(j + 1), SlotID: 1},
					}); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

func BenchmarkPostgresValidationObsRecord_Chunked(b *testing.B) {
	db := benchPostgres(b)
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			entries := benchObsEntries(n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if err := db.ClearValidationObs("escrow-1"); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				for start := 0; start < n; start += validationObsRebuildChunk {
					end := start + validationObsRebuildChunk
					if end > n {
						end = n
					}
					if err := db.RecordValidationsAppliedOnce("escrow-1", entries[start:end]); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
