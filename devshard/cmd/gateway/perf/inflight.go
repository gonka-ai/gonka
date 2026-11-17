package perf

import "sync"

type inflightGauge struct {
	mu     sync.Mutex
	counts map[string]int
}

func newInflightGauge() *inflightGauge {
	return &inflightGauge{counts: make(map[string]int)}
}

func (g *inflightGauge) acquire(participant string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.counts[participant]++
}

func (g *inflightGauge) release(participant string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if n := g.counts[participant] - 1; n > 0 {
		g.counts[participant] = n
	} else {
		delete(g.counts, participant)
	}
}

func (g *inflightGauge) count(participant string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.counts[participant]
}
