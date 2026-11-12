package state

import (
	"fmt"
	"testing"
	"time"

	"devshard/types"
)

func machineWithLiveRecords(b *testing.B, live, due int) *StateMachine {
	b.Helper()
	records := make(map[uint64]*types.InferenceRecord, live)
	for id := range uint64(live) {
		record := startedConfirmedAt(blockTimeNowMs)
		if int(id) < due {
			record = startedConfirmedAt(blockTimeNowMs - 2*time.Hour.Milliseconds())
		}
		records[id+1] = record
	}
	return newMachineAtBlockTime(b, blockTimeNowMs, records)
}

// The sweep scans on a fixed 15-second tick, so its cost must follow the live record count and not the
// request rate. Nothing is due in the ordinary case, and the scan must allocate nothing there.
func BenchmarkStartedInferencesPastDeadline(b *testing.B) {
	for _, live := range []int{1_000, 10_000, 100_000} {
		b.Run(fmt.Sprintf("live=%d/due=0", live), func(b *testing.B) {
			machine := machineWithLiveRecords(b, live, 0)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_ = machine.StartedInferencesPastDeadline(0, 0, 0, 8)
			}
		})
		b.Run(fmt.Sprintf("live=%d/due=all", live), func(b *testing.B) {
			machine := machineWithLiveRecords(b, live, live)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_ = machine.StartedInferencesPastDeadline(0, 0, 0, 8)
			}
		})
	}
}
