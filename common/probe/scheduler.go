package probe

import (
	"context"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// SchedObserver reports the scheduler's own health.
type SchedObserver interface {
	TickStarted()
	TickSkipped()
	TargetCount(int)
}

// Scheduler runs periodic probe waves against a TargetSource.
type Scheduler struct {
	p    *Prober
	src  TargetSource
	sink Sink
	obs  SchedObserver

	rngMu sync.Mutex
	rng   *rand.Rand
}

// NewScheduler builds a Scheduler. obs may be nil.
func NewScheduler(p *Prober, src TargetSource, sink Sink, obs SchedObserver) *Scheduler {
	return &Scheduler{
		p:    p,
		src:  src,
		sink: sink,
		obs:  obs,
		rng:  rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Run blocks until ctx is cancelled. Overlapping ticks are skipped.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.p.interval)
	defer ticker.Stop()

	var (
		mu       sync.Mutex
		prevKeys = map[string]struct{}{}
		inFlight atomic.Bool
	)

	runTick := func() {
		if !inFlight.CompareAndSwap(false, true) {
			if s.obs != nil {
				s.obs.TickSkipped()
			}
			return
		}
		go func() {
			defer inFlight.Store(false)
			s.tick(ctx, &mu, &prevKeys)
		}()
	}

	// Immediate first tick.
	runTick()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runTick()
		}
	}
}

func (s *Scheduler) tick(ctx context.Context, mu *sync.Mutex, prevKeys *map[string]struct{}) {
	if s.obs != nil {
		s.obs.TickStarted()
	}
	targets := s.src.Targets()
	if s.obs != nil {
		s.obs.TargetCount(len(targets))
	}

	sem := make(chan struct{}, s.p.conc)
	var wg sync.WaitGroup
	seen := make(map[string]struct{}, len(targets))

	for _, t := range targets {
		t := t
		seen[t.Key] = struct{}{}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if s.p.jitter > 0 {
				s.muJitterSleep(ctx)
			}
			if ctx.Err() != nil {
				return
			}
			res := s.p.ProbeOnce(ctx, t)
			if s.sink != nil {
				s.sink.Observe(res)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	for k := range *prevKeys {
		if _, ok := seen[k]; !ok && s.sink != nil {
			s.sink.Forget(k)
		}
	}
	*prevKeys = seen
	mu.Unlock()
}

func (s *Scheduler) muJitterSleep(ctx context.Context) {
	s.rngMu.Lock()
	d := time.Duration(s.rng.Int63n(int64(s.p.jitter) + 1))
	s.rngMu.Unlock()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
