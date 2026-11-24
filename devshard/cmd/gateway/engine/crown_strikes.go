package engine

import "sync"

// crownNarrator hears the two edges of crown denial, under crownStrikes.mu. See README.md, "Crown denial".
type crownNarrator interface {
	HostDeniedCrown(participant, model string, strikes int)
	HostCrownedAgain(participant, model string)
}

type hostModelKey struct{ participant, model string }

// crownStrikes withholds the crown from a host answering without content, but leaves it in rotation. See rules.md, "9. Bounded by construction".
type crownStrikes struct {
	mu       sync.Mutex
	strikes  map[hostModelKey]int
	narrator crownNarrator
}

func newCrownStrikes(narrator crownNarrator) *crownStrikes {
	return &crownStrikes{strikes: map[hostModelKey]int{}, narrator: narrator}
}

func (g *crownStrikes) Denied(participant, model string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.strikes[hostModelKey{participant: participant, model: model}] >= crownDenialStrikes
}

func (g *crownStrikes) Observe(participant, model string, contentless bool) {
	key := hostModelKey{participant: participant, model: model}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !contentless {
		if g.strikes[key] >= crownDenialStrikes && g.narrator != nil {
			g.narrator.HostCrownedAgain(participant, model)
		}
		delete(g.strikes, key)
		return
	}
	g.strikes[key]++
	if g.strikes[key] == crownDenialStrikes && g.narrator != nil {
		g.narrator.HostDeniedCrown(participant, model, g.strikes[key])
	}
}
