package main

import (
	"strconv"
	"testing"
)

// Targets per plan: RecordRequest < 5 µs, ScoreHost < 1 µs.

func BenchmarkRecordRequest_SingleHost(b *testing.B) {
	tr := NewHostScoreTracker(nil)
	rec := mkRequest("m", 1_000, mkInvolvement("A", 100, 1000))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.RecordRequest(rec)
	}
}

func BenchmarkRecordRequest_TwoHosts(b *testing.B) {
	tr := NewHostScoreTracker(nil)
	rec := mkRequest("m", 1_000,
		mkInvolvement("A", 100, 1000),
		mkInvolvement("B", 110, 1100),
	)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.RecordRequest(rec)
	}
}

func BenchmarkRecordRequest_FourHosts(b *testing.B) {
	tr := NewHostScoreTracker(nil)
	hosts := make([]HostInvolvement, 4)
	for i := range hosts {
		hosts[i] = mkInvolvement("H"+strconv.Itoa(i), float64(100+i*10), float64(1000+i*100))
	}
	rec := mkRequest("m", 1_000, hosts...)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.RecordRequest(rec)
	}
}

func BenchmarkScoreHost_ColdStart(b *testing.B) {
	tr := NewHostScoreTracker(nil)
	tr.RecordRequest(mkRequest("m", 1_000, mkInvolvement("A", 100, 1000)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tr.ScoreHost("m", "A", "lt_1k", false, "")
	}
}

func BenchmarkScoreHost_FullRing(b *testing.B) {
	tr := NewHostScoreTracker(nil)
	for i := 0; i < HostScoreWindowSize; i++ {
		tr.RecordRequest(mkRequest("m", 1_000, mkInvolvement("A", float64(100+i), float64(1000+i))))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tr.ScoreHost("m", "A", "lt_1k", false, "")
	}
}

func BenchmarkScoreHost_WithH2H(b *testing.B) {
	pw := NewPairwiseTracker()
	tr := NewHostScoreTracker(pw)
	for i := 0; i < HostScoreMinSamples+5; i++ {
		rec := mkRequest("m", 1_000, mkInvolvement("A", 100, 1000), mkInvolvement("B", 200, 2000))
		pw.RecordRequest(rec)
		tr.RecordRequest(rec)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tr.ScoreHost("m", "A", "lt_1k", false, "B")
	}
}
