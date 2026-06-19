package runtimeconfig

import (
	"math/rand"
	"time"
)

func nextBackoff(prev, min, max time.Duration) time.Duration {
	if prev <= 0 {
		prev = min
	} else if prev < max {
		prev *= 2
		if prev > max {
			prev = max
		}
	} else {
		prev = max
	}
	jitter := time.Duration(rand.Int63n(int64(prev)))
	return prev/2 + jitter
}
