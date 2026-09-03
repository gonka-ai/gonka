package storage

import (
	"fmt"
	"testing"
)

func benchSQLite(b *testing.B) *SQLite {
	b.Helper()
	db, err := NewSQLite(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	if err := db.CreateSession(defaultParams()); err != nil {
		b.Fatal(err)
	}
	return db
}

func benchSealedRows(n int, obsPresent bool) []InferenceRow {
	rows := make([]InferenceRow, n)
	for i := range n {
		rows[i] = InferenceRow{
			InferenceID:        uint64(i + 1),
			SealedNonce:        uint64(i + 1),
			ObsPresent:         obsPresent,
			SealedModel:        "llama",
			SealedInputTokens:  10,
			SealedOutputTokens: 20,
		}
	}
	return rows
}

// Snapshot restart after steps 1–3: list existing ids, write nothing.
func BenchmarkSealedInferenceIDs(b *testing.B) {
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			db := benchSQLite(b)
			if err := db.InsertSealedInferences("escrow-1", benchSealedRows(n, true)); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for range b.N {
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

// Pre-fix recovery: one Exec per sealed id (plus a delete).
func BenchmarkSealedInferenceIndex_UnbatchedInsert(b *testing.B) {
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			db := benchSQLite(b)
			rows := benchSealedRows(n, false)
			b.ResetTimer()
			for range b.N {
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

// Full-replay repair after steps 1–3: chunked transactions.
func BenchmarkSealedInferenceIndex_BatchedInsert(b *testing.B) {
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			db := benchSQLite(b)
			rows := benchSealedRows(n, true)
			b.ResetTimer()
			for range b.N {
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
