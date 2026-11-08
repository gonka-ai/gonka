package perf

import (
	"strconv"
	"testing"
	"time"
)

// A pool the size the gateway actually serves: tens of participants, three models each.
const (
	benchParticipantCount = 40
	benchEjectedCount     = 12
)

var benchModels = [...]string{"qwen", "kimi", "minimax"}

func benchParticipant(index int) string {
	return "gonka1participant" + strconv.Itoa(index)
}

// benchClock returns a monotonically increasing clock that never repeats an instant.
func benchClock() func() time.Time {
	start := time.Now()
	elapsed := time.Duration(0)
	return func() time.Time {
		elapsed += time.Millisecond
		return start.Add(elapsed)
	}
}

// warmTracker fills every participant/model pair with healthy samples and latencies.
func warmTracker(now func() time.Time) *Tracker {
	tracker := newTestTracker(testPerf(), now)
	for round := range latencyWindowSize {
		for participant := range benchParticipantCount {
			for _, model := range benchModels {
				tracker.RecordSample(Sample{
					ParticipantKey:     benchParticipant(participant),
					Model:              model,
					Responsive:         true,
					FirstContent:       time.Duration(round%37) * time.Millisecond,
					TimePerOutputToken: time.Duration(round%23) * time.Millisecond,
				})
			}
		}
	}
	return tracker
}

func BenchmarkTrackerRecordSample(b *testing.B) {
	tracker := warmTracker(benchClock())
	samples := make([]Sample, 0, benchParticipantCount*len(benchModels))
	for participant := range benchParticipantCount {
		for _, model := range benchModels {
			samples = append(samples, Sample{
				ParticipantKey:     benchParticipant(participant),
				Model:              model,
				Responsive:         true,
				FirstContent:       12 * time.Millisecond,
				TimePerOutputToken: 7 * time.Millisecond,
			})
		}
	}

	b.ReportAllocs()
	next := 0
	for b.Loop() {
		tracker.RecordSample(samples[next])
		next = (next + 1) % len(samples)
	}
}

func BenchmarkTrackerRebuildEjectedView(b *testing.B) {
	tracker := warmTracker(fixedNow(testEpoch))
	perf := testPerf()
	for participant := range benchEjectedCount {
		for _, model := range benchModels {
			failAllConsecutive(tracker, benchParticipant(participant), model, perf.ConsecutiveFailThreshold)
		}
	}

	b.ReportAllocs()
	for b.Loop() {
		tracker.rebuildEjectedViewLocked(testEpoch, perf)
	}
}

func BenchmarkTrackerFirstContentP75(b *testing.B) {
	tracker := warmTracker(fixedNow(testEpoch))

	b.ReportAllocs()
	next := 0
	for b.Loop() {
		tracker.FirstContentP75(benchParticipant(next%benchParticipantCount), benchModels[next%len(benchModels)])
		next++
	}
}

func BenchmarkTrackerSnapshot(b *testing.B) {
	tracker := warmTracker(fixedNow(testEpoch))

	b.ReportAllocs()
	for b.Loop() {
		tracker.Snapshot()
	}
}
