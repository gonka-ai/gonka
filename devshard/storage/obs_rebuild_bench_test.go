package storage

import (
	"fmt"
	"testing"
)

func benchObsEntries(n int) []ValidationObsEntry {
	entries := make([]ValidationObsEntry, n)
	for i := range n {
		entries[i] = ValidationObsEntry{InferenceID: uint64(i + 1), SlotID: 1}
	}
	return entries
}

func benchSealedIDs(n int) []uint64 {
	ids := make([]uint64, n)
	for i := range n {
		ids[i] = uint64(i + 1)
	}
	return ids
}

// The drain the rebuild used to do: one transaction per sealed id.
func BenchmarkValidationObsDrain_PerID(b *testing.B) {
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			db := benchSQLite(b)
			ids := benchSealedIDs(n)
			for range b.N {
				b.StopTimer()
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

// The batched form: chunked set-at-a-time move plus delete.
func BenchmarkValidationObsDrain_Batched(b *testing.B) {
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			db := benchSQLite(b)
			ids := benchSealedIDs(n)
			for range b.N {
				b.StopTimer()
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

// The record half: one call per journal record, which the rebuild used to do,
// versus the accumulated chunks it does now.
func BenchmarkValidationObsRecord_PerDiff(b *testing.B) {
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			db := benchSQLite(b)
			for range b.N {
				for j := range n {
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

func BenchmarkValidationObsRecord_Chunked(b *testing.B) {
	for _, n := range []int{2_000, 20_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			db := benchSQLite(b)
			entries := benchObsEntries(n)
			for range b.N {
				for start := 0; start < n; start += validationObsRebuildChunk {
					end := min(start+validationObsRebuildChunk, n)
					if err := db.RecordValidationsAppliedOnce("escrow-1", entries[start:end]); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
