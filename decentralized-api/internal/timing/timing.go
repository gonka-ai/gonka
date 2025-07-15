package timing

import (
	"log"
	"sync"
	"time"
)

type TimingTracker struct {
	mu    sync.RWMutex
	times map[string][]time.Duration
}

var tracker = &TimingTracker{
	times: make(map[string][]time.Duration),
}

func init() {
	go logAverages()
}

func Track(operation string, duration time.Duration) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.times[operation] = append(tracker.times[operation], duration)
}

func logAverages() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		tracker.mu.Lock()
		if len(tracker.times) > 0 {
			log.Println("=== TIMING AVERAGES (last minute) ===")
			for operation, durations := range tracker.times {
				if len(durations) > 0 {
					var total time.Duration
					for _, d := range durations {
						total += d
					}
					avg := total / time.Duration(len(durations))
					log.Printf("%s: avg=%v count=%d", operation, avg, len(durations))
				}
			}
			// Clear data for next minute
			tracker.times = make(map[string][]time.Duration)
		}
		tracker.mu.Unlock()
	}
}

func TimeOperation(operation string) func() {
	start := time.Now()
	return func() {
		Track(operation, time.Since(start))
	}
}
