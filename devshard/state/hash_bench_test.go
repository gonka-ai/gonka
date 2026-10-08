package state

import (
	"fmt"
	"testing"

	"devshard/types"
)

// benchHashSink keeps the hasher result alive so the compiler cannot drop the call.
var benchHashSink []byte

// BenchmarkLiveInferenceHash compares marshaling records with cached entries.
func BenchmarkLiveInferenceHash(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 30 * 60 * 32 / 2} {
		inferences, entries := benchFinishedSet(b, n)
		sm := &StateMachine{
			state:            &types.EscrowState{Inferences: inferences},
			committedEntries: entries,
		}
		fromStructs, err := computeInferencesHash(inferences)
		if err != nil {
			b.Fatal(err)
		}
		fromEntries, err := sm.liveInferencesHashLocked()
		if err != nil {
			b.Fatal(err)
		}
		if string(fromStructs) != string(fromEntries) {
			b.Fatal("cached hash diverged from struct marshal")
		}

		b.Run(fmt.Sprintf("recompute/n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				sum, err := computeInferencesHash(inferences)
				if err != nil {
					b.Fatal(err)
				}
				benchHashSink = sum
			}
		})
		b.Run(fmt.Sprintf("cached/n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			var total int
			for _, entry := range entries {
				total += len(entry)
			}
			b.ReportMetric(float64(total)/float64(n), "B/entry")
			for i := 0; i < b.N; i++ {
				sum, err := sm.liveInferencesHashLocked()
				if err != nil {
					b.Fatal(err)
				}
				benchHashSink = sum
			}
		})
	}
}

func benchFinishedSet(b *testing.B, n int) (map[uint64]*types.InferenceRecord, map[uint64][]byte) {
	b.Helper()
	inferences := make(map[uint64]*types.InferenceRecord, n)
	entries := make(map[uint64][]byte, n)
	prompt := make([]byte, 32)
	response := make([]byte, 32)
	for i := range prompt {
		prompt[i] = byte(i)
		response[i] = byte(255 - i)
	}
	for i := 0; i < n; i++ {
		id := uint64(i + 1)
		rec := &types.InferenceRecord{
			Status:       types.StatusFinished,
			ExecutorSlot: uint32(i % 5),
			Model:        "Qwen/Qwen2.5-7B-Instruct",
			PromptHash:   append([]byte(nil), prompt...),
			ResponseHash: append([]byte(nil), response...),
			InputLength:  100,
			MaxTokens:    64,
			InputTokens:  80,
			OutputTokens: 40,
			ReservedCost: 164,
			ActualCost:   120,
			StartedAt:    1_700_000_000,
			ConfirmedAt:  1_700_000_001,
		}
		entry, err := marshalInferenceEntry(id, rec)
		if err != nil {
			b.Fatal(err)
		}
		inferences[id] = rec
		entries[id] = entry
	}
	return inferences, entries
}
