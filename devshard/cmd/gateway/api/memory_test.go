package api

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"testing"
)

// Test flow:
//  1. Read the process memory through runtime/metrics.
//  2. Assert the figures are present and consistent with each other: the heap in use covers the live objects, and everything ever allocated covers what is live now.
func TestTheMemoryReadingIsConsistentWithoutStoppingTheWorld(t *testing.T) {
	reading, err := readMemory()
	if err != nil {
		t.Fatalf("readMemory() = %v, want nil", err)
	}
	if reading.Sys == 0 || reading.HeapAlloc == 0 || reading.HeapObjects == 0 || reading.NextGC == 0 {
		t.Fatalf("readMemory() = %+v, want sys, heap_alloc, heap_objects and next_gc all above zero", reading)
	}
	if reading.HeapInuse < reading.HeapAlloc || reading.TotalAlloc < reading.HeapAlloc || reading.HeapSys < reading.HeapInuse {
		t.Fatalf("readMemory() = %+v, want heap_sys >= heap_inuse >= heap_alloc and total_alloc >= heap_alloc", reading)
	}
}

// Test flow:
//  1. Ask the admin endpoint for memory stats.
//  2. Assert it answers every key it always has.
func TestTheMemstatsEndpointKeepsItsKeys(t *testing.T) {
	live := newHarness(t)

	recorder := live.request(t, http.MethodGet, "/v1/debug/memstats", "", adminHeaders())

	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("memstats body %q: %v", recorder.Body.String(), err)
	}
	for _, key := range []string{"alloc", "total_alloc", "sys", "heap_alloc", "heap_sys", "heap_idle", "heap_inuse", "heap_released", "heap_objects", "stack_inuse", "next_gc", "num_gc", "loaded_runtimes", "num_goroutine", "gc_cpu_fraction"} {
		if _, present := body[key]; !present {
			t.Fatalf("memstats keys = %v, want %q among them", slices.Sorted(maps.Keys(body)), key)
		}
	}
}
