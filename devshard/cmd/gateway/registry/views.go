package registry

import (
	"math"
	"slices"
	"sync"
	"sync/atomic"

	"devshard/cmd/gateway/scheduler"
)

const unknownChainMaxNonce uint32 = 0

// Candidates satisfies scheduler.escrowSource.
func (r *Registry) Candidates(model string) []scheduler.Escrow {
	entries := r.live.Load().byModel[model]
	candidates := make([]scheduler.Escrow, 0, len(entries))
	for _, entry := range entries {
		if !entry.accepting() {
			continue
		}
		candidates = append(candidates, entry.candidate())
	}
	return candidates
}

// Routable answers for one escrow what Candidates answers for a model, by id rather than by scanning every model.
func (r *Registry) Routable(escrowID string) (scheduler.Escrow, bool) {
	entry, known := r.live.Load().byID[escrowID]
	if !known || !entry.accepting() {
		return scheduler.Escrow{}, false
	}
	return entry.candidate(), true
}

func (e *escrowEntry) candidate() scheduler.Escrow {
	return scheduler.Escrow{
		ID:             e.id,
		Model:          e.model,
		SessionID:      e.sessionID,
		Session:        e.stream,
		ActiveUsers:    int(e.inFlight.Load()),
		IsReserve:      e.isReserve.Load(),
		Hold:           e.hold,
		EscalationHold: e.rescueHold,
	}
}

// RetiredAtNonceCap finds an escrow retired at its nonce cap that a race still holds; only that race's escalations may use it. See routing.md, "Picking an escrow".
func (r *Registry) RetiredAtNonceCap(escrowID string) (scheduler.Escrow, bool) {
	r.mu.Lock()
	var found *escrowEntry
	for entry := range r.draining {
		if entry.id == escrowID && entry.rescuableLocked() {
			found = entry
			break
		}
	}
	r.mu.Unlock()
	if found == nil || !found.accepting() {
		return scheduler.Escrow{}, false
	}
	return found.candidate(), true
}

// rescuableLocked holds while a race keeps the retired escrow open: it is busy, so no settlement that waits for the drain has started.
func (e *escrowEntry) rescuableLocked() bool {
	return e.nonceCapped && !e.closeClaimed && e.busy()
}

// escalationHoldFor is holdFor that also holds the entry once it is retired at its nonce cap, while it is rescuable.
func (r *Registry) escalationHoldFor(entry *escrowEntry) func() (func(), bool) {
	return func() (func(), bool) {
		if !entry.accepting() {
			return nil, false
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.live.Load().byID[entry.id] == entry {
			return r.holdLocked(entry), true
		}
		if _, draining := r.draining[entry]; !draining || !entry.rescuableLocked() {
			return nil, false
		}
		return r.holdLocked(entry), true
	}
}

type EscrowState struct {
	ID           string
	Model        string
	Accepting    bool
	InFlight     int64
	Participants []string
}

// Snapshot takes no registry lock, and includes the retired escrows still draining. See capacity.md, "What in-flight actually counts".
func (r *Registry) Snapshot() []EscrowState {
	published := r.live.Load()
	states := make([]EscrowState, 0, len(published.ordered))
	for _, entry := range published.ordered {
		states = append(states, stateOf(entry, false))
	}
	if draining := r.drainingView.Load(); draining != nil {
		for _, entry := range *draining {
			if entry.busy() {
				states = append(states, stateOf(entry, true))
			}
		}
	}
	return states
}

func stateOf(entry *escrowEntry, draining bool) EscrowState {
	return EscrowState{
		ID:           entry.id,
		Model:        entry.model,
		Accepting:    !draining && entry.accepting(),
		InFlight:     entry.inFlight.Load(),
		Participants: slices.Clone(entry.participants),
	}
}

// RoutableSession takes no in-flight count and is deliberately not the dispatch path; that is Acquire.
func (r *Registry) RoutableSession(escrowID string) (EscrowSession, bool) {
	entry, known := r.live.Load().byID[escrowID]
	if !known {
		return nil, false
	}
	return entry.session, true
}

// SettlementSession resolves published or draining, deliberately asymmetric with routing. See rules.md, "4. Routing and settlement read the escrow set asymmetrically".
func (r *Registry) SettlementSession(escrowID string) (EscrowSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, known := r.settlementEntryLocked(escrowID)
	if !known {
		return nil, false
	}
	return entry.session, true
}

// HoldsSession reports whether the registry still holds an escrow's session, published or draining.
func (r *Registry) HoldsSession(escrowID string) bool {
	_, held := r.SettlementSession(escrowID)
	return held
}

// HoldSettlement: see README.md, "The published set and its readers".
func (r *Registry) HoldSettlement(escrowID string) (EscrowSession, func(), bool) {
	return r.holdUnclosed(escrowID, func(entry *escrowEntry) *atomic.Int64 { return &entry.settlementHolds })
}

// holdForSweep keeps a published or draining escrow's session open and busy for a timeout vote without counting it as a request. See README.md, "The published set and its readers".
func (r *Registry) holdForSweep(escrowID string) (EscrowSession, func(), bool) {
	return r.holdUnclosed(escrowID, func(entry *escrowEntry) *atomic.Int64 { return &entry.sweepHolds })
}

func (r *Registry) holdUnclosed(escrowID string, holdsOf func(*escrowEntry) *atomic.Int64) (EscrowSession, func(), bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, known := r.settlementEntryLocked(escrowID)
	if !known || entry.closeClaimed {
		return nil, nil, false
	}
	return entry.session, r.countHoldLocked(entry, holdsOf(entry)), true
}

func (r *Registry) countHoldLocked(entry *escrowEntry, holds *atomic.Int64) func() {
	holds.Add(1)
	var once sync.Once
	return func() { once.Do(func() { r.releaseHold(entry, holds) }) }
}

func (r *Registry) settlementEntryLocked(escrowID string) (*escrowEntry, bool) {
	if entry, known := r.live.Load().byID[escrowID]; known {
		return entry, true
	}
	for entry := range r.draining {
		if entry.id == escrowID {
			return entry, true
		}
	}
	return nil, false
}

func (r *Registry) Models() []string {
	published := r.live.Load()
	models := make([]string, 0, len(published.byModel))
	for model, entries := range published.byModel {
		if slices.ContainsFunc(entries, (*escrowEntry).accepting) {
			models = append(models, model)
		}
	}
	slices.Sort(models)
	return models
}

func (r *Registry) Serves(model string) bool {
	return slices.ContainsFunc(r.live.Load().byModel[model], (*escrowEntry).accepting)
}

// Acquire resolves the dispatch handle and counts one in-flight request in the same locked step. See routing.md, "The escrow registry".
func (r *Registry) Acquire(escrowID string) (session EscrowSession, release func(), ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, known := r.live.Load().byID[escrowID]
	if !known {
		return nil, nil, false
	}
	return entry.session, r.holdLocked(entry), true
}

// AcquireCommitted resolves the escrow a committed nonce is bound to, published or draining, with a request hold: a rotation since the pick changes nothing the nonce owes. See rules.md, "4. Routing and settlement read the escrow set asymmetrically".
func (r *Registry) AcquireCommitted(escrowID string) (session EscrowSession, release func(), ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, known := r.settlementEntryLocked(escrowID)
	if !known || entry.closeClaimed {
		return nil, nil, false
	}
	return entry.session, r.holdLocked(entry), true
}

// holdFor is bound to the entry rather than its id. See routing.md, "Where the nonce, the slot and the hold are taken".
func (r *Registry) holdFor(entry *escrowEntry) func() (func(), bool) {
	return func() (func(), bool) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.live.Load().byID[entry.id] != entry {
			return nil, false
		}
		return r.holdLocked(entry), true
	}
}

func (r *Registry) holdLocked(entry *escrowEntry) func() {
	entry.holds.Add(1)
	return r.countHoldLocked(entry, &entry.inFlight)
}

// Holds counts every hold a request ever took on the live escrow, so a reader polling it can tell work happened between two looks.
func (r *Registry) Holds(escrowID string) uint64 {
	entry, known := r.live.Load().byID[escrowID]
	if !known {
		return 0
	}
	return entry.holds.Load()
}

// ShareChainMaxNonce hands every published and draining session, and every serving session opened later, the chain's max_nonce. See README.md.
func (r *Registry) ShareChainMaxNonce(maxNonce uint64) {
	capped := uint32(min(maxNonce, math.MaxUint32))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chainMaxNonce.Store(capped)
	for _, entry := range r.live.Load().ordered {
		entry.session.SetChainMaxNonce(capped)
	}
	if draining := r.drainingView.Load(); draining != nil {
		for _, entry := range *draining {
			entry.session.SetChainMaxNonce(capped)
		}
	}
}

func (r *Registry) withChainMaxNonceLocked(session EscrowSession) EscrowSession {
	session.SetChainMaxNonce(r.chainMaxNonce.Load())
	return session
}

// HostDials is every address the live escrows can reach, once each. See README.md.
func (r *Registry) HostDials() []HostDial {
	published := r.live.Load()
	seen := make(map[string]struct{}, len(published.ordered))
	dials := make([]HostDial, 0, len(published.ordered))
	for _, entry := range published.ordered {
		for _, dial := range entry.session.HostDials() {
			if dial.BaseURL == "" {
				continue
			}
			if _, known := seen[dial.BaseURL]; known {
				continue
			}
			seen[dial.BaseURL] = struct{}{}
			dials = append(dials, dial)
		}
	}
	return dials
}
