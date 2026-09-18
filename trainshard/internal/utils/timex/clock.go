package timex

import (
	"sync"
	"time"
)

type Frozen struct {
	mu sync.Mutex
	at time.Time
}

func NewFrozen(at time.Time) *Frozen { return &Frozen{at: at} }

func (f *Frozen) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.at
}

func (f *Frozen) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.at = f.at.Add(d)
}
