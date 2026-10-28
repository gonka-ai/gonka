package registry

import (
	"testing"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
)

const (
	benchmarkUnsealedRecords = 10_000
	benchmarkOpenRecords     = 64
	benchmarkNewPerTick      = 50
	benchmarkFleetEscrows    = 96
)

// benchmarkHandle builds one escrow whose live window holds ten thousand unsealed records, the last sixty-four still Pending.
func benchmarkHandle(b *testing.B) sessionHandle {
	b.Helper()
	signers := make([]*signing.Secp256k1Signer, 4)
	for index := range signers {
		signers[index] = testutil.MustGenerateKey(b)
	}
	group := testutil.MakeMultiSlotGroup(signers, []int{1, 1, 1, 1})
	config := testutil.DefaultConfig(len(group))
	creator := testutil.MustGenerateKey(b)
	store := testutil.MustMemoryStore(b, liveEscrowID, creator.Address(), config, group, 1<<40)
	machine, err := state.NewStateMachine(liveEscrowID, config, group, 1<<40, creator.Address(), signing.NewSecp256k1Verifier(), store)
	if err != nil {
		b.Fatalf("NewStateMachine() = %v, want nil", err)
	}
	escrowState := machine.SnapshotState()
	escrowState.Inferences = make(map[uint64]*types.InferenceRecord, benchmarkUnsealedRecords)
	for id := uint64(1); id <= benchmarkUnsealedRecords; id++ {
		status := types.StatusFinished
		if id > benchmarkUnsealedRecords-benchmarkOpenRecords {
			status = types.StatusPending
		}
		escrowState.Inferences[id] = &types.InferenceRecord{
			Status: status, ExecutorSlot: uint32(id % 4), InputLength: 2_000, MaxTokens: 512, ReservedCost: 2_512, StartedAt: 946_684_800,
		}
	}
	escrowState.LatestNonce = benchmarkUnsealedRecords
	if err := machine.RestoreState(&escrowState); err != nil {
		b.Fatalf("RestoreState() = %v, want nil", err)
	}
	return sessionHandle{machine: machine, finalizing: newFinalizeLock()}
}

// BenchmarkLiveInferencesOfOneEscrowWithTenThousandUnsealedRecords benchmarks today's read: every unsealed record copied, then filtered to the open ones.
func BenchmarkLiveInferencesOfOneEscrowWithTenThousandUnsealedRecords(b *testing.B) {
	handle := benchmarkHandle(b)

	b.ReportAllocs()
	for b.Loop() {
		_, records := handle.LiveInferences()
		open := 0
		for _, record := range records {
			if record.Status == types.StatusPending || record.Status == types.StatusStarted || record.Status == types.StatusChallenged {
				open++
			}
		}
		if open != benchmarkOpenRecords {
			b.Fatalf("LiveInferences() open records = %d, want %d", open, benchmarkOpenRecords)
		}
	}
}

// BenchmarkOpenRecordIndexOfOneEscrowWithTenThousandUnsealedRecords benchmarks one tick's read after the first: fifty new nonces, the open records and one Finished slice.
func BenchmarkOpenRecordIndexOfOneEscrowWithTenThousandUnsealedRecords(b *testing.B) {
	handle := benchmarkHandle(b)
	var index openRecordIndex
	index.read(handle)

	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		index.examined = benchmarkUnsealedRecords - benchmarkNewPerTick
		b.StartTimer()
		if records := index.read(handle); len(records) != benchmarkOpenRecords {
			b.Fatalf("read() = %d records, want %d", len(records), benchmarkOpenRecords)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)*benchmarkFleetEscrows/1e6, "ms/tick-of-96")
}
