package perf

import (
	"testing"
	"time"
)

// The quantile is read once per attempt, not per event; this is what that decision is worth.
func BenchmarkLatencyWindowP75(b *testing.B) {
	var window latencyWindow
	for i := range latencyWindowSize {
		window.add(time.Duration(i%37) * time.Second)
	}

	b.ReportAllocs()
	for b.Loop() {
		window.p75(latencyWindowMinimum)
	}
}
