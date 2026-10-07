package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"runtime/metrics"
	"sort"
	"strings"
	"sync"
	"time"

	"log/slog"

	"devshard/cmd/devshardd/session"
	"devshard/observability"
)

// memoryLogInterval is how often the process records what its memory is.
// Byte totals come from runtime/metrics, which sums per-processor counters
// without stopping the world. The heap profile is a sample, and the session
// fields are map lengths.
const memoryLogInterval = 10 * time.Minute

const memoryHeapTopN = 12
const memoryHeapStackDepth = 8

func startMemoryLog(ctx context.Context, manager *session.HostManager) {
	go func() {
		logMemory(manager)
		ticker := time.NewTicker(memoryLogInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				logMemory(manager)
			}
		}
	}()
}

type memorySnapshot struct {
	At               time.Time `json:"sampled_at"`
	HeapAlloc        uint64    `json:"heap_alloc"`
	HeapInuse        uint64    `json:"heap_inuse"`
	HeapIdle         uint64    `json:"heap_idle"`
	HeapReleased     uint64    `json:"heap_released"`
	HeapObjects      uint64    `json:"heap_objects"`
	StackInuse       uint64    `json:"stack_inuse"`
	Sys              uint64    `json:"sys"`
	Goroutines       int       `json:"goroutines"`
	NumGC            uint32    `json:"num_gc"`
	Sessions         int       `json:"sessions"`
	LiveInferences   int       `json:"live_inferences"`
	SealedInferences int       `json:"sealed_inferences"`
	Mempool          int       `json:"mempool"`
	Executing        int       `json:"executing"`
	Validating       int       `json:"validating"`
	FattestEscrow    string    `json:"fattest_escrow,omitempty"`
	FattestLive      int       `json:"fattest_live"`
	Heap             []heapTop `json:"heap"`
}

var (
	memoryMu       sync.Mutex
	latestSnapshot memorySnapshot
)

// processMemory is the whole-process portion of a snapshot. The byte fields
// match the MemStats names the log already uses:
//
//	heap_alloc    live heap objects, including objects not yet marked free
//	heap_inuse    spans that hold heap objects
//	heap_idle     heap spans with no objects, including memory returned to the OS
//	heap_released idle heap already returned to the OS
//	heap_objects  live heap objects
//	stack_inuse   goroutine stacks allocated from the heap
//	sys           bytes obtained from the OS
//
// These are read from runtime/metrics. That path sums counters the runtime
// already keeps and does not stop the world. Bytes still sitting in a
// processor's cache may be missing until the cache is flushed.
type processMemory struct {
	HeapAlloc    uint64
	HeapInuse    uint64
	HeapIdle     uint64
	HeapReleased uint64
	HeapObjects  uint64
	StackInuse   uint64
	Sys          uint64
	Goroutines   int
	NumGC        uint32
}

const (
	metricHeapObjectsBytes = "/memory/classes/heap/objects:bytes"
	metricHeapUnusedBytes  = "/memory/classes/heap/unused:bytes"
	metricHeapFreeBytes    = "/memory/classes/heap/free:bytes"
	metricHeapReleased     = "/memory/classes/heap/released:bytes"
	metricHeapObjects      = "/gc/heap/objects:objects"
	metricHeapStacks       = "/memory/classes/heap/stacks:bytes"
	metricMemoryTotal      = "/memory/classes/total:bytes"
	metricGoroutines       = "/sched/goroutines:goroutines"
	metricGCCycles         = "/gc/cycles/total:gc-cycles"
)

func readProcessMemory() (processMemory, error) {
	samples := []metrics.Sample{
		{Name: metricHeapObjectsBytes},
		{Name: metricHeapUnusedBytes},
		{Name: metricHeapFreeBytes},
		{Name: metricHeapReleased},
		{Name: metricHeapObjects},
		{Name: metricHeapStacks},
		{Name: metricMemoryTotal},
		{Name: metricGoroutines},
		{Name: metricGCCycles},
	}
	metrics.Read(samples)
	vals := make([]uint64, len(samples))
	for i, sample := range samples {
		if sample.Value.Kind() != metrics.KindUint64 {
			return processMemory{}, fmt.Errorf("metric %s kind %v", sample.Name, sample.Value.Kind())
		}
		vals[i] = sample.Value.Uint64()
	}
	objects := vals[0]
	unused := vals[1]
	free := vals[2]
	released := vals[3]
	return processMemory{
		HeapAlloc:    objects,
		HeapInuse:    objects + unused,
		HeapIdle:     free + released,
		HeapReleased: released,
		HeapObjects:  vals[4],
		StackInuse:   vals[5],
		Sys:          vals[6],
		Goroutines:   int(vals[7]),
		NumGC:        uint32(vals[8]),
	}, nil
}

func logMemory(manager *session.HostManager) {
	proc, err := readProcessMemory()
	if err != nil {
		slog.Error("memory snapshot", "error", err)
		return
	}
	sessions := manager.SessionMemoryCounts()
	snap := memorySnapshot{
		At:               time.Now().UTC(),
		HeapAlloc:        proc.HeapAlloc,
		HeapInuse:        proc.HeapInuse,
		HeapIdle:         proc.HeapIdle,
		HeapReleased:     proc.HeapReleased,
		HeapObjects:      proc.HeapObjects,
		StackInuse:       proc.StackInuse,
		Sys:              proc.Sys,
		Goroutines:       proc.Goroutines,
		NumGC:            proc.NumGC,
		Sessions:         sessions.Sessions,
		LiveInferences:   sessions.Live,
		SealedInferences: sessions.Sealed,
		Mempool:          sessions.Mempool,
		Executing:        sessions.Executing,
		Validating:       sessions.Validating,
		FattestEscrow:    sessions.Fattest,
		FattestLive:      sessions.FattestLive,
		Heap:             topHeapInUse(memoryHeapTopN),
	}
	memoryMu.Lock()
	latestSnapshot = snap
	memoryMu.Unlock()
	observability.SetMemoryInventory(observability.MemoryInventory{
		Sessions:    snap.Sessions,
		Live:        snap.LiveInferences,
		Sealed:      snap.SealedInferences,
		Mempool:     snap.Mempool,
		Executing:   snap.Executing,
		Validating:  snap.Validating,
		Fattest:     snap.FattestEscrow,
		FattestLive: snap.FattestLive,
	})
	slog.Info("memory snapshot",
		"heap_alloc", proc.HeapAlloc,
		"heap_inuse", proc.HeapInuse,
		"heap_idle", proc.HeapIdle,
		"heap_released", proc.HeapReleased,
		"heap_objects", proc.HeapObjects,
		"stack_inuse", proc.StackInuse,
		"sys", proc.Sys,
		"goroutines", proc.Goroutines,
		"num_gc", proc.NumGC,
		"sessions", sessions.Sessions,
		"live_inferences", sessions.Live,
		"sealed_inferences", sessions.Sealed,
		"mempool", sessions.Mempool,
		"executing", sessions.Executing,
		"validating", sessions.Validating,
		"fattest_escrow", snap.FattestEscrow,
		"fattest_live", snap.FattestLive,
	)
	for _, line := range snap.Heap {
		slog.Info("memory heap",
			"rank", line.Rank,
			"inuse_bytes", line.InUseBytes,
			"inuse_objects", line.InUseObjects,
			"stack", line.Stack,
		)
	}
}

// handleDebugMemory serves the latest snapshot. It does not take a new one:
// a scrape must not stop the world.
func handleDebugMemory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	memoryMu.Lock()
	snap := latestSnapshot
	memoryMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(snap)
}

type heapTop struct {
	Rank         int    `json:"rank"`
	InUseBytes   int64  `json:"inuse_bytes"`
	InUseObjects int64  `json:"inuse_objects"`
	Stack        string `json:"stack"`
}

// topHeapInUse ranks sampled heap by the stack that allocated it, so retained
// escrow state shows up under state.snapshotMutable (copyInferences,
// cloneCommittedInferenceEntries). ValidateDiff and PreviewLocalBestEffort
// build the post-state as a copy and CommitValidated installs that copy as the
// live state. Those stacks are mostly live inferences, not snapshot garbage.
func topHeapInUse(limit int) []heapTop {
	if limit <= 0 {
		return nil
	}
	n, _ := runtime.MemProfile(nil, false)
	if n == 0 {
		return nil
	}
	records := make([]runtime.MemProfileRecord, n)
	n, ok := runtime.MemProfile(records, false)
	if !ok {
		records = make([]runtime.MemProfileRecord, n)
		n, ok = runtime.MemProfile(records, false)
		if !ok {
			return nil
		}
	}
	records = records[:n]
	sort.Slice(records, func(i, j int) bool {
		return records[i].InUseBytes() > records[j].InUseBytes()
	})
	if len(records) > limit {
		records = records[:limit]
	}
	out := make([]heapTop, 0, len(records))
	for i, rec := range records {
		if rec.InUseBytes() == 0 {
			break
		}
		out = append(out, heapTop{
			Rank:         i + 1,
			InUseBytes:   rec.InUseBytes(),
			InUseObjects: rec.InUseObjects(),
			Stack:        stackLine(rec.Stack(), memoryHeapStackDepth),
		})
	}
	return out
}

func stackLine(pcs []uintptr, depth int) string {
	frames := runtime.CallersFrames(pcs)
	var b strings.Builder
	for written := 0; written < depth; written++ {
		frame, more := frames.Next()
		if frame.Function == "" {
			if !more {
				break
			}
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(';')
		}
		b.WriteString(frame.Function)
		if !more {
			break
		}
	}
	return b.String()
}
