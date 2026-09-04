package client

import (
	"math/rand"
	"time"
)

func nextBackoff(prev, minimum, maximum time.Duration) time.Duration {
	switch {
	case prev <= 0:
		prev = minimum
	case prev < maximum:
		prev = min(prev*2, maximum)
	default:
		prev = maximum
	}
	jitter := time.Duration(rand.Int63n(int64(prev)))
	return prev/2 + jitter
}
