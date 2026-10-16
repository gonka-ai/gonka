package memwatch

import (
	"context"
	"sync/atomic"
)

// ChallengeStats is one snapshot interval of refused-timeout request bodies.
type ChallengeStats struct {
	Requests int
	BytesMin uint64
	BytesMax uint64
	BytesAvg uint64
}

type challengeStatKey struct{}

// WithChallengeStats marks ctx so the request body built from it is included
// in the next memory snapshot.
func WithChallengeStats(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, challengeStatKey{}, struct{}{})
}

// ChallengeStatsOn reports that ctx was marked by WithChallengeStats.
func ChallengeStatsOn(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(challengeStatKey{}).(struct{})
	return ok
}

// requestWindow is updated with atomics on the request path. Take runs once
// per memory snapshot and may split one in-flight record across two windows.
type requestWindow struct {
	n   atomic.Uint64
	sum atomic.Uint64
	min atomic.Uint64
	max atomic.Uint64
}

var challenges requestWindow

// RecordChallengeRequest adds one refused-timeout request body to the window
// the next memory snapshot logs and then clears.
func RecordChallengeRequest(size int) {
	if size < 0 {
		size = 0
	}
	challenges.record(uint64(size))
}

func (w *requestWindow) record(size uint64) {
	w.n.Add(1)
	w.sum.Add(size)
	// min stays 0 until the first record. A body is never zero bytes.
	for {
		old := w.min.Load()
		if old != 0 && old <= size {
			break
		}
		if w.min.CompareAndSwap(old, size) {
			break
		}
	}
	for {
		old := w.max.Load()
		if size <= old {
			break
		}
		if w.max.CompareAndSwap(old, size) {
			break
		}
	}
}

// TakeChallengeStats returns the requests recorded since the previous take
// and clears the window. The second result is false when the window is empty.
func TakeChallengeStats() (ChallengeStats, bool) {
	return challenges.take()
}

func (w *requestWindow) take() (ChallengeStats, bool) {
	n := w.n.Swap(0)
	if n == 0 {
		return ChallengeStats{}, false
	}
	sum := w.sum.Swap(0)
	min := w.min.Swap(0)
	max := w.max.Swap(0)
	return ChallengeStats{
		Requests: int(n),
		BytesMin: min,
		BytesMax: max,
		BytesAvg: sum / n,
	}, true
}
