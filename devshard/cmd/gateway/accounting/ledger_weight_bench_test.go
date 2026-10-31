package accounting

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/scheduler"
	"devshard/types"
)

const (
	weightLiveEscrows      = 10
	weightRetiredEscrows   = 20
	weightNoncesPerEscrow  = 100_000
	weightLiveWindow       = 6_400
	weightUnfinishedEvery  = 20
	weightGhostEvery       = 100
	weightSweepEvery       = 500
	weightNoncesPerMinute  = 70
	weightCurrentEpoch     = 102
	weightRetainedEpoch    = 101
	weightFeePerNonce      = 10
	weightBytesPerKibibyte = 1024
)

type weightEscrow struct {
	escrowID string
	latest   uint64
	live     map[uint64]*types.InferenceRecord
}

func weightEscrowID(epoch uint64, index int) string {
	return fmt.Sprintf("weight-%d-%02d", epoch, index)
}

func weightInference(nonce uint64, status types.InferenceStatus) *types.InferenceRecord {
	return &types.InferenceRecord{
		Status: status, ExecutorSlot: uint32(nonce % benchGroupSize), InputLength: 6000, MaxTokens: 2048,
		InputTokens: 1500, OutputTokens: 400, ReservedCost: 120000, ActualCost: 90000,
	}
}

func weightRecordNonce(book *Book, escrow *weightEscrow, nonce uint64) {
	requestID := fmt.Sprintf("request-%d-%d", escrow.latest, nonce)
	_ = book.RecordAssigned(escrow.escrowID, nonce, requestID)
	status := types.StatusFinished
	switch {
	case nonce%weightGhostEvery == 0:
		_ = book.RecordGhost(escrow.escrowID, nonce, scheduler.GhostReasonWindowFull)
		status = types.StatusTimedOut
	case nonce%weightUnfinishedEvery == 0:
		_ = book.RecordRace(escrow.escrowID, []Attempt{{
			Nonce: nonce, RequestID: requestID, Sent: true, Acknowledged: true,
			Usage: UsageUnknown, Terminal: TerminalUnnamed, Phase: PhaseNormal,
		}})
		_ = book.RecordTimeout(escrow.escrowID, nonce, engine.TimeoutKindExecution, engine.TimeoutActionCompleted, engine.TimeoutReasonNotApplied)
		_ = book.RecordAppliedTimeout(escrow.escrowID, nonce)
		status = types.StatusTimedOut
	default:
		attempt := Attempt{
			Nonce: nonce, RequestID: requestID, OutputTokens: 400, Sent: true, Acknowledged: true,
			Usage: UsageWinner, Terminal: engine.TerminalWon.String(), Phase: PhaseNormal,
			SlowReceipt: nonce%23 == 0, SlowChunk: nonce%29 == 0,
		}
		if nonce%3 == 0 {
			attempt.Usage, attempt.Terminal = UsageLoser, engine.TerminalLost.String()
		}
		_ = book.RecordRace(escrow.escrowID, []Attempt{attempt})
	}
	if nonce%10 == 0 {
		_ = book.RecordValidation(escrow.escrowID, uint32(nonce%benchGroupSize))
	}
	escrow.live[nonce] = weightInference(nonce, status)
	if nonce > weightLiveWindow {
		delete(escrow.live, nonce-weightLiveWindow)
	}
	escrow.latest = nonce
}

func weightSweep(book *Book, escrow *weightEscrow) {
	_ = book.ObserveLatestNonce(escrow.escrowID, escrow.latest)
	_ = book.ObserveCharging(escrow.escrowID, weightFeePerNonce, 0)
	for slotID := range uint32(benchGroupSize) {
		_ = book.ObserveHostStats(escrow.escrowID, slotID, benchHostStats())
	}
	_ = book.ObserveInferences(escrow.escrowID, escrow.live)
}

func weightFill(b *testing.B, book *Book, epoch uint64, index int, retired bool) *weightEscrow {
	b.Helper()
	escrow := &weightEscrow{escrowID: weightEscrowID(epoch, index), live: make(map[uint64]*types.InferenceRecord, weightLiveWindow)}
	slots := make([]types.SlotAssignment, 0, benchGroupSize)
	for slotID := range benchGroupSize {
		slots = append(slots, types.SlotAssignment{SlotID: uint32(slotID), ValidatorAddress: benchParticipant((index*5 + slotID) % benchParticipants)})
	}
	if err := book.OpenEscrow(EscrowMetadata{EscrowID: escrow.escrowID, Model: "model-a", CreationEpoch: epoch, Slots: slots}); err != nil {
		b.Fatalf("OpenEscrow(): %v", err)
	}
	for nonce := uint64(1); nonce <= weightNoncesPerEscrow; nonce++ {
		weightRecordNonce(book, escrow, nonce)
		if nonce%weightSweepEvery == 0 {
			weightSweep(book, escrow)
		}
	}
	weightSweep(book, escrow)
	if retired {
		clear(escrow.live)
		weightSweep(book, escrow)
		book.RetireEscrow(escrow.escrowID)
	}
	return escrow
}

func weightBook(b *testing.B) (*Book, []*weightEscrow, []*weightEscrow) {
	b.Helper()
	book := NewBook(func() time.Time { return time.Unix(1700000000, 0).UTC() })
	live := make([]*weightEscrow, 0, weightLiveEscrows)
	for index := range weightLiveEscrows {
		live = append(live, weightFill(b, book, weightCurrentEpoch, index, false))
	}
	retired := make([]*weightEscrow, 0, weightRetiredEscrows)
	for index := range weightRetiredEscrows {
		retired = append(retired, weightFill(b, book, weightRetainedEpoch, index, true))
	}
	return book, live, retired
}

func weightSweepAll(book *Book, live, retired []*weightEscrow) {
	for _, escrow := range live {
		weightSweep(book, escrow)
	}
	for _, escrow := range retired {
		book.RetireEscrow(escrow.escrowID)
	}
}

func weightUnsavedEscrows(book *Book) int {
	book.mu.RLock()
	defer book.mu.RUnlock()
	return len(book.unsaved)
}

type stallProbe struct {
	stop    chan struct{}
	done    chan struct{}
	longest time.Duration
}

func probeStalls(book *Book, escrowID string, nonce uint64) *stallProbe {
	probe := &stallProbe{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(probe.done)
		for {
			select {
			case <-probe.stop:
				return
			default:
			}
			started := time.Now()
			_ = book.RecordAssigned(escrowID, nonce, "probe")
			probe.longest = max(probe.longest, time.Since(started))
		}
	}()
	return probe
}

func (probe *stallProbe) finish() time.Duration {
	close(probe.stop)
	<-probe.done
	return probe.longest
}

func benchmarkSweepThenSave(b *testing.B, newNoncesPerLiveEscrow int) {
	book, live, retired := weightBook(b)
	store := benchStore(b)
	if err := store.Save(context.Background(), book); err != nil {
		b.Fatalf("Save(): %v", err)
	}
	var longest time.Duration
	var saved, rounds int
	b.ReportAllocs()
	for b.Loop() {
		for _, escrow := range live {
			for range newNoncesPerLiveEscrow {
				weightRecordNonce(book, escrow, escrow.latest+1)
			}
		}
		weightSweepAll(book, live, retired)
		saved += weightUnsavedEscrows(book)
		rounds++
		probe := probeStalls(book, live[0].escrowID, live[0].latest)
		if err := store.Save(context.Background(), book); err != nil {
			b.Fatalf("Save(): %v", err)
		}
		longest = max(longest, probe.finish())
	}
	b.ReportMetric(float64(saved)/float64(rounds), "escrows-saved/op")
	b.ReportMetric(float64(longest)/float64(time.Millisecond), "max-stall-ms")
}

// Test flow:
//  1. Fill a mainnet-sized book: ten live escrows and twenty retired ones of 100,000 nonces each.
//  2. Each round, sweep every escrow with the values it already holds, then save while a dispatcher keeps calling RecordAssigned.
//  3. Report the escrows each save wrote and the longest RecordAssigned call.
func BenchmarkSweepThenSaveIdle(b *testing.B) {
	benchmarkSweepThenSave(b, 0)
}

// Test flow:
//  1. Fill a mainnet-sized book: ten live escrows and twenty retired ones of 100,000 nonces each.
//  2. Each round, add a minute of traffic to every live escrow, sweep every escrow, then save while a dispatcher keeps calling RecordAssigned.
//  3. Report the escrows each save wrote and the longest RecordAssigned call.
func BenchmarkSweepThenSaveBusy(b *testing.B) {
	benchmarkSweepThenSave(b, weightNoncesPerMinute)
}

// Test flow:
//  1. Fill a mainnet-sized book.
//  2. Serve the epochs and participants reads while a dispatcher keeps calling RecordAssigned.
//  3. Report the longest RecordAssigned call.
func BenchmarkReadRoutesStall(b *testing.B) {
	book, live, _ := weightBook(b)
	filter := QueryFilter{EpochIndex: weightCurrentEpoch}
	for _, route := range []struct {
		name  string
		serve func()
	}{
		{name: "epochs", serve: func() { _ = book.Epochs(QueryFilter{}) }},
		{name: "participants", serve: func() { _ = book.Query(filter) }},
	} {
		b.Run(route.name, func(b *testing.B) {
			var longest time.Duration
			b.ReportAllocs()
			for b.Loop() {
				probe := probeStalls(book, live[0].escrowID, live[0].latest)
				route.serve()
				longest = max(longest, probe.finish())
			}
			b.ReportMetric(float64(longest)/float64(time.Millisecond), "max-stall-ms")
		})
	}
}

func heapInUse() uint64 {
	runtime.GC()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc
}

// Test flow:
//  1. Fill one escrow of 100,000 nonces, take its post-finalize reading with nothing live, and retire it.
//  2. Report the heap the retired escrow keeps after a collection.
func BenchmarkRetiredEscrowHeap(b *testing.B) {
	var retained uint64
	var rounds int
	for b.Loop() {
		rounds++
		before := heapInUse()
		book := NewBook(func() time.Time { return time.Unix(1700000000, 0).UTC() })
		weightFill(b, book, weightRetainedEpoch, 0, true)
		retained += heapInUse() - before
		runtime.KeepAlive(book)
	}
	b.ReportMetric(float64(retained)/float64(rounds)/weightBytesPerKibibyte, "retired-escrow-KiB")
}

// Test flow:
//  1. Fill a book and hold one escrow's live window of 2,000 nonces.
//  2. Each round, move the window by one nonce, as a sweep of a busy escrow sees it, and observe it.
func BenchmarkObserveInferencesMovingWindow(b *testing.B) {
	book := benchBook(b)
	escrowID := benchEscrowID(0)
	inferences := make(map[uint64]*types.InferenceRecord, benchNoncesPerEscrow)
	for nonce := uint64(1); nonce <= benchNoncesPerEscrow; nonce++ {
		inferences[nonce] = weightInference(nonce, types.StatusFinished)
	}
	next := uint64(benchNoncesPerEscrow + 1)
	b.ReportAllocs()
	for b.Loop() {
		delete(inferences, next-benchNoncesPerEscrow)
		inferences[next] = weightInference(next, types.StatusFinished)
		next++
		if err := book.ObserveInferences(escrowID, inferences); err != nil {
			b.Fatalf("ObserveInferences(): %v", err)
		}
	}
}
