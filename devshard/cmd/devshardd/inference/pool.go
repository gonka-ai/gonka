package inference

import (
	"sync"
	"time"
)

// flipPool accumulates the echo signals of one executor and model: the
// positions off the winner over a participant's recent answers are binomial
// under the same rate as one answer's, so their sum catches a substitute that
// passes answer by answer.
type flipPool struct {
	mu      sync.Mutex
	window  int
	maxAge  time.Duration
	now     func() time.Time
	entries map[poolKey][]poolSample
}

type poolKey struct{ executor, model string }

type poolSample struct {
	at           time.Time
	steps, count int
}

func newFlipPool(window int, maxAge time.Duration) *flipPool {
	return &flipPool{window: window, maxAge: maxAge, now: time.Now, entries: map[poolKey][]poolSample{}}
}

// Observe records one scored answer and returns the number of answers, the
// positions and the positions off the winner pooled over the window.
func (p *flipPool) Observe(executor, model string, steps, count int) (answers, pooledSteps, pooledCount int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := poolKey{executor, model}
	now := p.now()
	kept := make([]poolSample, 0, p.window)
	for _, s := range p.entries[key] {
		if now.Sub(s.at) <= p.maxAge {
			kept = append(kept, s)
		}
	}
	kept = append(kept, poolSample{at: now, steps: steps, count: count})
	if len(kept) > p.window {
		kept = kept[len(kept)-p.window:]
	}
	p.entries[key] = kept
	for _, s := range kept {
		pooledSteps += s.steps
		pooledCount += s.count
	}
	return len(kept), pooledSteps, pooledCount
}
