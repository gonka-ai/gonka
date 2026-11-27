package storetest

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/store"
	"devshard/host"
	"devshard/user"
)

const (
	bytesPerKilobyte        = 1024
	benchmarkEscrowPrefix   = "bench-escrow-"
	sweepRowsVariable       = "GATEWAY_BENCH_SWEEP_ROWS"
	defaultSweepRows        = 100_000
	benchmarkPreparedAt     = 1_700_000_000
	benchmarkRecordedAt     = 1_700_000_000
	benchmarkRetentionHours = 24
)

var (
	benchmarkPromptKilobytes = []int{1, 8, 64}
	benchmarkGoroutines      = []int{1, 8, 64}
	statementSetGoroutines   = []int{1, 8}
)

// RunBenchmarks runs the backend benchmarks: the completion journal, the ledger, one retention sweep and the per-request statement set.
func RunBenchmarks(b *testing.B, factory Factory) {
	b.Run("JournalSaveThenDelete", func(b *testing.B) { benchmarkJournal(b, factory) })
	b.Run("LedgerRecord", func(b *testing.B) { benchmarkLedger(b, factory) })
	b.Run("RetentionSweep", func(b *testing.B) { benchmarkSweep(b, factory) })
	b.Run("PerRequestStatements", func(b *testing.B) { benchmarkPerRequest(b, factory) })
}

func benchmarkEntry(nonce uint64, prompt []byte) user.InferenceCompletion {
	return user.InferenceCompletion{
		Nonce:      nonce,
		PreparedAt: benchmarkPreparedAt,
		Payload:    host.InferencePayload{Prompt: prompt, Model: "qwen", InputLength: uint64(len(prompt)), MaxTokens: 9, StartedAt: 5},
	}
}

func runConcurrently(b *testing.B, goroutines int, work func(worker, iterations int)) {
	b.Helper()
	perWorker := max(b.N/goroutines, 1)
	var group sync.WaitGroup
	b.ResetTimer()
	for worker := range goroutines {
		group.Go(func() {
			work(worker, perWorker)
		})
	}
	group.Wait()
	b.StopTimer()
	b.ReportMetric(float64(perWorker*goroutines)/b.Elapsed().Seconds(), "operations/s")
}

func benchmarkJournal(b *testing.B, factory Factory) {
	for _, kilobytes := range benchmarkPromptKilobytes {
		for _, goroutines := range benchmarkGoroutines {
			b.Run(fmt.Sprintf("%dkB/%dgoroutines", kilobytes, goroutines), func(b *testing.B) {
				journal := factory(b)()
				prompt := make([]byte, kilobytes*bytesPerKilobyte)
				runConcurrently(b, goroutines, func(worker, iterations int) {
					escrowID := benchmarkEscrowPrefix + strconv.Itoa(worker)
					for nonce := range iterations {
						if err := journal.SaveInferenceCompletion(escrowID, benchmarkEntry(uint64(nonce+1), prompt)); err != nil {
							b.Errorf("SaveInferenceCompletion(): %v", err)
							return
						}
						if err := journal.DeleteInferenceCompletion(escrowID, uint64(nonce+1)); err != nil {
							b.Errorf("DeleteInferenceCompletion(): %v", err)
							return
						}
					}
				})
			})
		}
	}
}

func benchmarkLedger(b *testing.B, factory Factory) {
	opened := factory(b)()
	clock := func() time.Time { return time.Unix(benchmarkRecordedAt, 0).UTC() }
	ledger, err := opened.NewLedger(store.Retention{MaxAge: benchmarkRetentionHours * time.Hour, MaxRows: int64(b.N) + 1}, clock)
	if err != nil {
		b.Fatalf("NewLedger(): %v", err)
	}
	b.ResetTimer()
	for index := range b.N {
		ledger.Record(store.RequestRecord{RequestID: "bench-request-" + strconv.Itoa(index), Outcome: store.RequestSettled})
	}
	if err := ledger.Close(); err != nil {
		b.Fatalf("Close(): %v", err)
	}
	b.StopTimer()
	stats := ledger.Stats()
	b.ReportMetric(float64(stats.Written), "written")
	b.ReportMetric(float64(stats.Dropped), "dropped")
	b.ReportMetric(float64(stats.Failed), "failed")
	b.ReportMetric(float64(stats.Written)/b.Elapsed().Seconds(), "written/s")
}

func sweepRows(b *testing.B) int {
	b.Helper()
	raw := os.Getenv(sweepRowsVariable)
	if raw == "" {
		return defaultSweepRows
	}
	rows, err := strconv.Atoi(raw)
	if err != nil || rows < 1 {
		b.Fatalf("%s = %q, want a positive integer", sweepRowsVariable, raw)
	}
	return rows
}

func benchmarkSweep(b *testing.B, factory Factory) {
	opened := factory(b)()
	rows := sweepRows(b)
	ctx := context.Background()
	recordedAt := time.Unix(benchmarkRecordedAt, 0).UTC()
	for index := range rows {
		record := store.RequestRecord{RequestID: "sweep-request-" + strconv.Itoa(index), Outcome: store.RequestSettled, RecordedAt: recordedAt.Add(time.Duration(index) * time.Second)}
		if err := opened.InsertRequest(ctx, record); err != nil {
			b.Fatalf("InsertRequest(%d): %v", index, err)
		}
	}
	b.Run("ByAge", func(b *testing.B) {
		for range b.N {
			if err := opened.DeleteRequestsBefore(ctx, recordedAt.Add(-time.Hour)); err != nil {
				b.Fatalf("DeleteRequestsBefore(): %v", err)
			}
		}
	})
	b.Run("ByRowCount", func(b *testing.B) {
		for range b.N {
			if err := opened.DeleteRequestsBeyond(ctx, int64(rows)); err != nil {
				b.Fatalf("DeleteRequestsBeyond(): %v", err)
			}
		}
	})
}

func benchmarkPerRequest(b *testing.B, factory Factory) {
	for _, goroutines := range statementSetGoroutines {
		b.Run(fmt.Sprintf("%dgoroutines", goroutines), func(b *testing.B) {
			opened := factory(b)()
			ctx := context.Background()
			prompt := make([]byte, bytesPerKilobyte)
			for worker := range goroutines {
				record := store.DevshardRecord{EscrowID: benchmarkEscrowPrefix + strconv.Itoa(worker), Model: "qwen", Active: true}
				if err := opened.UpsertDevshard(ctx, record); err != nil {
					b.Fatalf("UpsertDevshard(): %v", err)
				}
			}
			runConcurrently(b, goroutines, func(worker, iterations int) {
				escrowID := benchmarkEscrowPrefix + strconv.Itoa(worker)
				for nonce := range iterations {
					if err := perRequestStatements(ctx, opened, escrowID, uint64(nonce+1), prompt); err != nil {
						b.Errorf("per-request statements: %v", err)
						return
					}
				}
			})
		})
	}
}

func perRequestStatements(ctx context.Context, opened store.Store, escrowID string, nonce uint64, prompt []byte) error {
	requestID := escrowID + "-" + strconv.FormatUint(nonce, 10)
	if _, _, err := opened.Devshard(ctx, escrowID); err != nil {
		return err
	}
	if err := opened.SaveInferenceCompletion(escrowID, benchmarkEntry(nonce, prompt)); err != nil {
		return err
	}
	if err := opened.InsertRequest(ctx, store.RequestRecord{RequestID: requestID, EscrowID: escrowID, Outcome: store.RequestSettled}); err != nil {
		return err
	}
	vote := store.OwedVote{EscrowID: escrowID, Nonce: nonce, RequestID: requestID, Prompt: prompt}
	if err := opened.SaveOwedVote(ctx, vote); err != nil {
		return err
	}
	if err := opened.DeleteOwedVote(ctx, escrowID, nonce); err != nil {
		return err
	}
	return opened.DeleteInferenceCompletion(escrowID, nonce)
}
