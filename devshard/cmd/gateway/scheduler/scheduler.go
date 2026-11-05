// Package scheduler picks the escrow, host, and nonce that serve a chat-completions request.
package scheduler

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"devshard/cmd/gateway/config"
)

// idleDispatcherGrace is how long an escrow's actor stays alive with an empty queue. See routing.md, "Idle dispatchers are reaped".
const idleDispatcherGrace = 5 * time.Minute

// Deps wires the runtime facts routing reads; Observer, Now, OnEscrowExhausted, OnReserveTaken and OnMoneyShort are optional.
type Deps struct {
	Escrows           escrowSource
	Capacity          escrowWeights
	Limiter           hostLimiter
	Perf              hostHealth
	Snapshots         snapshotSource
	Config            *config.Holder
	Observer          dispatchObserver
	Now               func() time.Time
	OnEscrowExhausted func(escrowID string, reason ExhaustionReason)
	OnReserveTaken    func(escrowID string)
	OnMoneyShort      func(model string)
}

// Scheduler owns one actor per escrow. See routing.md, "Picking an escrow".
type Scheduler struct {
	escrows           escrowSource
	capacity          escrowWeights
	limiter           hostLimiter
	perf              hostHealth
	snapshots         snapshotSource
	settings          *config.Holder
	observer          dispatchObserver
	now               func() time.Time
	newTimer          func(time.Duration) (<-chan time.Time, func())
	onEscrowExhausted func(escrowID string, reason ExhaustionReason)
	onReserveTaken    func(escrowID string)
	onMoneyShort      func(model string)

	tieBreak atomic.Int64

	registryMu  sync.Mutex
	dispatchers map[string]*dispatcher
	superseded  map[*dispatcher]struct{}
	stopped     bool

	blocksMu     sync.RWMutex
	blockedHosts map[string]map[string]bool
	absentHosts  map[string]map[string]bool
	replays      replayCredit
}

// NewScheduler refuses a dependency set it cannot route with, rather than panicking on the first request.
func NewScheduler(deps Deps) (*Scheduler, error) {
	switch {
	case deps.Config == nil:
		return nil, errors.New("scheduler: Config is required")
	case deps.Escrows == nil:
		return nil, errors.New("scheduler: Escrows is required")
	case deps.Capacity == nil:
		return nil, errors.New("scheduler: Capacity is required")
	case deps.Limiter == nil:
		return nil, errors.New("scheduler: Limiter is required")
	case deps.Perf == nil:
		return nil, errors.New("scheduler: Perf is required")
	case deps.Snapshots == nil:
		return nil, errors.New("scheduler: Snapshots is required")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Scheduler{
		escrows:           deps.Escrows,
		capacity:          deps.Capacity,
		limiter:           deps.Limiter,
		perf:              deps.Perf,
		snapshots:         deps.Snapshots,
		settings:          deps.Config,
		observer:          deps.Observer,
		now:               deps.Now,
		onEscrowExhausted: deps.OnEscrowExhausted,
		onReserveTaken:    deps.OnReserveTaken,
		onMoneyShort:      deps.OnMoneyShort,
		dispatchers:       map[string]*dispatcher{},
		blockedHosts:      map[string]map[string]bool{},
		absentHosts:       map[string]map[string]bool{},
	}, nil
}

// HostDiverged reports whether the participant still had its catch-up replay; the last one blocks it.
func (s *Scheduler) HostDiverged(escrowID, participant string, at time.Time) bool {
	if s.replays.spend(escrowID, participant, at) {
		return true
	}
	s.BlockHost(escrowID, participant)
	return false
}

// HostServed returns the replay to a participant whose later send the group accepted.
func (s *Scheduler) HostServed(escrowID, participant string, sentAt time.Time) {
	s.replays.restore(escrowID, participant, sentAt)
}

// HostAbsent bars a participant from one escrow whose session holds no client for it. See routing.md, "A slot the chain did not list at open".
func (s *Scheduler) HostAbsent(escrowID, participant string) {
	s.blocksMu.Lock()
	defer s.blocksMu.Unlock()
	absent := s.absentHosts[escrowID]
	if absent == nil {
		absent = map[string]bool{}
		s.absentHosts[escrowID] = absent
	}
	absent[participant] = true
}

// AbsentHosts returns, per escrow, the participants HostAbsent barred from it.
func (s *Scheduler) AbsentHosts() map[string][]string {
	s.blocksMu.RLock()
	defer s.blocksMu.RUnlock()
	marked := make(map[string][]string, len(s.absentHosts))
	for escrowID, participants := range s.absentHosts {
		for participant := range participants {
			marked[escrowID] = append(marked[escrowID], participant)
		}
	}
	return marked
}

// ForgetAbsentHosts lifts an escrow's HostAbsent bars, for a session that is opened anew.
func (s *Scheduler) ForgetAbsentHosts(escrowID string) {
	s.blocksMu.Lock()
	defer s.blocksMu.Unlock()
	delete(s.absentHosts, escrowID)
}

// BlockHost bars a participant from one escrow for as long as the escrow's dispatcher lives. See routing.md.
func (s *Scheduler) BlockHost(escrowID, participant string) {
	s.blocksMu.Lock()
	defer s.blocksMu.Unlock()
	blocked := s.blockedHosts[escrowID]
	if blocked == nil {
		blocked = map[string]bool{}
		s.blockedHosts[escrowID] = blocked
	}
	blocked[participant] = true
}
