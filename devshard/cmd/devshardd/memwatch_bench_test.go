package main

import (
	"math"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"testing"
	"time"

	"log/slog"
)

// BenchmarkMemorySnapshot10GB times one memory snapshot while 10 GB stays live.
//
// The pause count is only for the loop. b.ResetTimer and b.StopTimer are
// outside that window on purpose: the testing package calls ReadMemStats
// inside both, and that call stops the world for about 20 µs. Counting those
// calls made an earlier run report one pause in the 0.020–0.025 ms bucket
// for logMemory, which does not stop the world itself.
//
//	go test -bench=BenchmarkMemorySnapshot10GB -benchtime=200ms -count=1 -v ./cmd/devshardd/
func BenchmarkMemorySnapshot10GB(b *testing.B) {
	const heapBytes = 10 << 30
	hold := retainHeap(b, heapBytes)

	logger := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	b.Cleanup(func() {
		slog.SetDefault(logger)
		runtime.KeepAlive(hold)
	})

	// Idle is the control. It does no snapshot work. A pause here would come
	// from something other than the snapshot.
	b.Run("Idle", func(b *testing.B) {
		reportLoopPauses(b, func() {})
		runtime.KeepAlive(hold)
	})

	b.Run("RuntimeMetrics", func(b *testing.B) {
		reportLoopPauses(b, func() {
			if _, err := readProcessMemory(); err != nil {
				b.Fatal(err)
			}
		})
		runtime.KeepAlive(hold)
	})

	b.Run("TopHeapInUse", func(b *testing.B) {
		if top := topHeapInUse(memoryHeapTopN); len(top) == 0 {
			b.Fatal("empty heap profile")
		}
		var top []heapTop
		reportLoopPauses(b, func() {
			top = topHeapInUse(memoryHeapTopN)
		})
		runtime.KeepAlive(top)
		runtime.KeepAlive(hold)
	})

	b.Run("LogMemory", func(b *testing.B) {
		reportLoopPauses(b, func() {
			logMemory(nil)
		})
		runtime.KeepAlive(hold)
	})
}

// reportLoopPauses times fn and counts non-GC stop-the-world pauses that
// start and finish inside the loop. ResetTimer and StopTimer run outside
// the count.
func reportLoopPauses(b *testing.B, fn func()) {
	b.Helper()
	b.ResetTimer()
	before := readOtherSTW(b)
	for i := 0; i < b.N; i++ {
		fn()
	}
	delta := diffOtherSTW(b, before)
	b.StopTimer()

	b.ReportMetric(float64(delta.n), "pauses")
	if delta.n == 0 {
		return
	}
	b.Logf("non-gc stop-the-world pauses inside the loop: %d; longest bucket %.4f–%.4f ms",
		delta.n, delta.maxLo*1000, delta.maxHi*1000)
	b.ReportMetric(delta.maxLo*1000, "ms/pause-lo")
	b.ReportMetric(delta.maxHi*1000, "ms/pause-hi")
}

// retainHeap allocates nbytes of live heap and leaves the garbage collector
// off so a collection does not land inside the timed snapshot.
func retainHeap(b *testing.B, nbytes int) [][]byte {
	b.Helper()
	const chunk = 64 << 10
	n := nbytes / chunk
	prev := debug.SetGCPercent(-1)
	b.Cleanup(func() { debug.SetGCPercent(prev) })

	started := time.Now()
	hold := make([][]byte, n)
	for i := range hold {
		// make zeroes the chunk, so the pages are resident, not just reserved.
		hold[i] = make([]byte, chunk)
	}
	// Samples sit in the next profile cycle until a collection folds them in.
	// GC is off for the timed section, so fold this fill in now.
	runtime.GC()

	proc, err := readProcessMemory()
	if err != nil {
		b.Fatal(err)
	}
	top := topHeapInUse(1)
	b.Logf("retained %d chunks of %d bytes, fill+gc %s; heap_inuse=%d heap_alloc=%d heap_objects=%d gomaxprocs=%d top=%v",
		n, chunk, time.Since(started).Round(time.Millisecond),
		proc.HeapInuse, proc.HeapAlloc, proc.HeapObjects, runtime.GOMAXPROCS(0), top)
	if proc.HeapAlloc < uint64(nbytes) {
		b.Fatalf("heap alloc %d, want at least %d", proc.HeapAlloc, nbytes)
	}
	// The profile records sampled objects, about one per 512 KiB, so the
	// largest stack is well below the real 10 GB. It still has to be the
	// retained heap, not a small allocation from process startup.
	const minSampled = 64 << 20
	if len(top) == 0 || top[0].InUseBytes < minSampled {
		b.Fatalf("largest sampled stack is %v, want at least %d bytes", top, minSampled)
	}
	return hold
}

// otherSTW is the runtime histogram of stop-the-world pauses that are not
// garbage collections. Each pause increments one bucket.
type otherSTW struct {
	counts  []uint64
	buckets []float64
}

type otherSTWDelta struct {
	n     uint64
	maxLo float64
	maxHi float64
}

func readOtherSTW(b *testing.B) otherSTW {
	b.Helper()
	sample := []metrics.Sample{{Name: "/sched/pauses/total/other:seconds"}}
	metrics.Read(sample)
	if sample[0].Value.Kind() != metrics.KindFloat64Histogram {
		b.Fatalf("pause metric kind %v", sample[0].Value.Kind())
	}
	h := sample[0].Value.Float64Histogram()
	return otherSTW{
		counts:  append([]uint64(nil), h.Counts...),
		buckets: append([]float64(nil), h.Buckets...),
	}
}

func diffOtherSTW(b *testing.B, before otherSTW) otherSTWDelta {
	b.Helper()
	after := readOtherSTW(b)
	var delta otherSTWDelta
	for i := range after.counts {
		var prev uint64
		if i < len(before.counts) {
			prev = before.counts[i]
		}
		if after.counts[i] <= prev {
			continue
		}
		delta.n += after.counts[i] - prev
		lo := after.buckets[i]
		hi := after.buckets[i+1]
		if math.IsInf(hi, 1) {
			hi = lo
		}
		if hi > delta.maxHi {
			delta.maxLo, delta.maxHi = lo, hi
		}
	}
	return delta
}
