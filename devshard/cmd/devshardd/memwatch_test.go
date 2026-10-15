package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTopHeapInUseIsBounded(t *testing.T) {
	// Touch the profile so the current test allocation can appear.
	buf := make([]byte, 8<<20)
	runtime.KeepAlive(buf)

	top := topHeapInUse(3)
	if len(top) > 3 {
		t.Fatalf("top heap returned %d rows, limit is 3", len(top))
	}
	var prev int64
	for i, line := range top {
		if line.Rank != i+1 {
			t.Fatalf("rank %d, want %d", line.Rank, i+1)
		}
		if line.InUseBytes <= 0 {
			t.Fatalf("rank %d has no in-use bytes", line.Rank)
		}
		if prev > 0 && line.InUseBytes > prev {
			t.Fatalf("rank %d has more bytes than the rank above it", line.Rank)
		}
		prev = line.InUseBytes
		if strings.Contains(line.Stack, "\n") {
			t.Fatalf("stack must stay on one log line: %q", line.Stack)
		}
	}
}

func TestDebugMemoryServesTheStoredSnapshot(t *testing.T) {
	memoryMu.Lock()
	latestSnapshot = memorySnapshot{
		At:             time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
		HeapInuse:      42,
		LiveInferences: 7,
		FattestEscrow:  "escrow-1",
		FattestLive:    7,
		Heap:           []heapTop{{Rank: 1, InUseBytes: 100, InUseObjects: 2, Stack: "devshard/state.apply"}},
	}
	memoryMu.Unlock()

	rec := httptest.NewRecorder()
	handleDebugMemory(rec, httptest.NewRequest(http.MethodGet, "/stats/memory", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got memorySnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.LiveInferences != 7 || got.FattestEscrow != "escrow-1" || len(got.Heap) != 1 {
		t.Fatalf("snapshot %+v", got)
	}
}

func TestReadProcessMemorySeesLiveHeap(t *testing.T) {
	buf := make([]byte, 8<<20)
	proc, err := readProcessMemory()
	if err != nil {
		t.Fatal(err)
	}
	if proc.HeapAlloc == 0 || proc.HeapInuse < proc.HeapAlloc || proc.Sys < proc.HeapInuse {
		t.Fatalf("process memory %+v", proc)
	}
	if proc.Goroutines < 1 || proc.HeapObjects == 0 {
		t.Fatalf("process memory %+v", proc)
	}
	runtime.KeepAlive(buf)
}

func TestStackLineCapsDepth(t *testing.T) {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(0, pcs)
	line := stackLine(pcs[:n], 2)
	if line == "" {
		t.Fatal("empty stack")
	}
	if got := strings.Count(line, ";"); got > 1 {
		t.Fatalf("depth cap kept %d separators: %s", got, line)
	}
}
