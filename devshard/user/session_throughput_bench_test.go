package user

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/storage"
)

type stepTotals struct {
	mu      sync.Mutex
	elapsed map[string]time.Duration
	counts  map[string]int
}

func newStepTotals() *stepTotals {
	return &stepTotals{elapsed: map[string]time.Duration{}, counts: map[string]int{}}
}

func (totals *stepTotals) record(step string, elapsed time.Duration) {
	totals.mu.Lock()
	defer totals.mu.Unlock()
	totals.elapsed[step] += elapsed
	totals.counts[step]++
}

func (totals *stepTotals) report(b *testing.B) {
	totals.mu.Lock()
	defer totals.mu.Unlock()
	for _, step := range []string{StepLockWaitPrepare, StepLockHeldPrepare, StepLockWaitProcess, StepLockHeldProcess, StepStoreAppendDiff, StepStoreAddSignature} {
		if count := totals.counts[step]; count > 0 {
			b.ReportMetric(float64(totals.elapsed[step].Microseconds())/float64(count), step+"-µs")
		}
	}
}

type sentNonce struct {
	prepared *PreparedInference
	response *host.HostResponse
}

// BenchmarkSessionNonceThroughput runs one session the way the gateway drives it: a single goroutine prepares
// nonces, as an escrow's dispatcher does; `workers` goroutines send each to its in-process host, as attempts do;
// and a response is processed only once `lag` later nonces were prepared, as a stream that outlives them is. It
// reports nonces a second end to end and the session's step means.
func BenchmarkSessionNonceThroughput(b *testing.B) {
	for _, lag := range []int{0, maxRetainedDiffs + 44} {
		for _, workers := range []int{1, 16, 64, 256} {
			b.Run(fmt.Sprintf("lag=%d/workers=%d", lag, workers), func(b *testing.B) {
				runNonceThroughput(b, workers, lag, 0)
			})
		}
	}
	for _, every := range []time.Duration{100 * time.Millisecond, 10 * time.Millisecond} {
		b.Run(fmt.Sprintf("export_every=%s/workers=64", every), func(b *testing.B) {
			runNonceThroughput(b, 64, 0, every)
		})
	}
}

func runNonceThroughput(b *testing.B, workers, lag int, exportEvery time.Duration) {
	store, err := storage.NewSQLite(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	session := setupStoredSessionFunded(b, store, 1<<50)
	b.Cleanup(func() {
		_ = session.Close()
		for session.snapshotInFlight.Load() {
			runtime.Gosched()
		}
	})
	totals := newStepTotals()
	session.SetStepTimer(totals.record)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	ctx := context.Background()
	failures := make(chan error, 2*workers+1)
	fail := func(err error) {
		select {
		case failures <- err:
		default:
		}
	}
	prepared := make(chan *PreparedInference, workers)
	sent := make(chan sentNonce, workers)
	due := make(chan sentNonce, workers)

	var sending, processing sync.WaitGroup
	for range workers {
		sending.Go(func() {
			for nonce := range prepared {
				response, err := session.SendOnly(ctx, nonce, nil, nil)
				if err != nil {
					fail(err)
					continue
				}
				sent <- sentNonce{prepared: nonce, response: response}
			}
		})
		processing.Go(func() {
			for answered := range due {
				if err := session.ProcessResponse(answered.prepared.hostIdx, answered.response, answered.prepared.diff.Nonce); err != nil {
					fail(err)
				}
			}
		})
	}
	delayed := make(chan struct{})
	go func() {
		defer close(delayed)
		var held []sentNonce
		for answered := range sent {
			held = append(held, answered)
			for len(held) > 0 && session.PublishedNonce() >= held[0].prepared.diff.Nonce+uint64(lag) {
				due <- held[0]
				held = held[1:]
			}
		}
		for _, answered := range held {
			due <- answered
		}
	}()

	exporting := make(chan struct{})
	exported := make(chan struct{})
	go func() {
		defer close(exported)
		if exportEvery <= 0 {
			return
		}
		ticker := time.NewTicker(exportEvery)
		defer ticker.Stop()
		for {
			select {
			case <-exporting:
				return
			case <-ticker.C:
				_ = session.sm.ExportAllInferenceRecords()
			}
		}
	}()

	started := time.Now()
	b.ResetTimer()
	for range b.N {
		nonce, err := session.PrepareInference(ctx, params)
		if err != nil {
			b.Fatal(err)
		}
		prepared <- nonce
	}
	close(prepared)
	sending.Wait()
	close(sent)
	<-delayed
	close(due)
	processing.Wait()
	close(exporting)
	<-exported
	b.StopTimer()
	select {
	case err := <-failures:
		b.Fatal(err)
	default:
	}
	b.ReportMetric(float64(b.N)/time.Since(started).Seconds(), "nonces/s")
	totals.report(b)
}
