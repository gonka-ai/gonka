package memwatch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTopHeapIsBounded(t *testing.T) {
	// Touch the profile so the current test allocation can appear.
	buf := make([]byte, 8<<20)
	runtime.KeepAlive(buf)

	top := TopHeap(3, DefaultStackDepth)
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

func TestReadProcessSeesLiveHeap(t *testing.T) {
	buf := make([]byte, 8<<20)
	proc, err := ReadProcess()
	if err != nil {
		t.Fatal(err)
	}
	if proc.HeapAlloc == 0 || proc.HeapInuse < proc.HeapAlloc || proc.Sys < proc.HeapInuse {
		t.Fatalf("process memory %+v", proc)
	}
	if proc.Goroutines < 1 || proc.HeapObjects == 0 {
		t.Fatalf("process memory %+v", proc)
	}
	if proc.NextGC == 0 {
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

type stubInventory struct {
	Live int    `json:"live_inferences"`
	Name string `json:"fattest_escrow,omitempty"`
}

func (s stubInventory) LogAttrs() []any {
	return []any{"live_inferences", s.Live, "fattest_escrow", s.Name}
}

func TestServeHTTPServesTheStoredSnapshot(t *testing.T) {
	var calls int
	l := New(Config{Inventory: func() Inventory {
		calls++
		return stubInventory{}
	}})
	l.mu.Lock()
	l.latest = Snapshot{
		At:      time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
		Process: Process{HeapInuse: 42},
		Heap:    []HeapTop{{Rank: 1, InUseBytes: 100, InUseObjects: 2, Stack: "devshard/state.apply"}},
		inv:     stubInventory{Live: 7, Name: "escrow-1"},
	}
	l.mu.Unlock()

	rec := httptest.NewRecorder()
	l.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats/memory", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if calls != 0 {
		t.Fatalf("handler took %d samples", calls)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["heap_inuse"] != float64(42) || got["live_inferences"] != float64(7) || got["fattest_escrow"] != "escrow-1" {
		t.Fatalf("snapshot %s", rec.Body.Bytes())
	}
	heap, ok := got["heap"].([]any)
	if !ok || len(heap) != 1 {
		t.Fatalf("heap %v", got["heap"])
	}
	if _, nested := got["inventory"]; nested {
		t.Fatalf("inventory must be flat: %s", rec.Body.Bytes())
	}
}

func TestServeHTTPRejectsOtherMethods(t *testing.T) {
	rec := httptest.NewRecorder()
	New(Config{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/stats/memory", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("Allow %q", rec.Header().Get("Allow"))
	}
}
