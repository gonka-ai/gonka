package runtimeconfig

import "time"

// Clock abstracts time for tests.
type Clock interface {
	Now() time.Time
	Since(time.Time) time.Duration
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                      { return time.Now() }
func (realClock) Since(t time.Time) time.Duration     { return time.Since(t) }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
