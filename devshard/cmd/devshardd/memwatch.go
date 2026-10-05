package main

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"log/slog"

	"devshard/cmd/devshardd/session"
	"devshard/observability"
)

// memoryLogInterval is how often the process records what its memory is.
// The snapshot copies sampled profile rows and map lengths. It does not walk
// the heap, so it does not stall requests the way a heap dump would.
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

func logMemory(manager *session.HostManager) {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	sessions := manager.SessionMemoryCounts()
	snap := memorySnapshot{
		At:               time.Now().UTC(),
		HeapAlloc:        stats.HeapAlloc,
		HeapInuse:        stats.HeapInuse,
		HeapIdle:         stats.HeapIdle,
		HeapReleased:     stats.HeapReleased,
		HeapObjects:      stats.HeapObjects,
		StackInuse:       stats.StackInuse,
		Sys:              stats.Sys,
		Goroutines:       runtime.NumGoroutine(),
		NumGC:            stats.NumGC,
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
		"heap_alloc", stats.HeapAlloc,
		"heap_inuse", stats.HeapInuse,
		"heap_idle", stats.HeapIdle,
		"heap_released", stats.HeapReleased,
		"heap_objects", stats.HeapObjects,
		"stack_inuse", stats.StackInuse,
		"sys", stats.Sys,
		"goroutines", runtime.NumGoroutine(),
		"num_gc", stats.NumGC,
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
