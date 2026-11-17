package scheduler

import (
	"fmt"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/limits"
)

const benchModel = "model-bench"

var benchNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// waiterSink keeps a built waiter from being optimised away.
var waiterSink *waiter

// benchParticipant is bech32-shaped, so map keys hash over a realistic length.
func benchParticipant(index int) string {
	return fmt.Sprintf("gonka1%039d", index)
}

type drainSession struct {
	slots    []string
	keys     []string
	nonce    uint64
	prepared preparedNonce
}

func (s *drainSession) Advance(decide func(HostBinding) NonceIntent) (Prepared, error) {
	nonce := s.nonce + 1
	hostIdx := int(nonce % uint64(len(s.slots)))
	if !decide(HostBinding{Nonce: nonce, HostIdx: hostIdx, Participant: s.slots[hostIdx]}).Commit {
		return nil, nil
	}
	s.nonce = nonce
	s.prepared = preparedNonce{nonce: nonce, hostIdx: hostIdx}
	return &s.prepared, nil
}

func (s *drainSession) ParticipantKeys() []string   { return s.keys }
func (s *drainSession) SlotParticipants() []string  { return s.slots }
func (s *drainSession) ChatBudgetSpent(string) bool { return false }
func (s *drainSession) GroupSize() int              { return len(s.slots) }
func (s *drainSession) LatestNonce() uint64         { return s.nonce }
func (s *drainSession) Balance() uint64             { return 1 << 40 }
func (s *drainSession) TokenPrice() uint64          { return 1 }
func (s *drainSession) FeePerNonce() uint64         { return 0 }

type leanLimiter struct {
	refused  map[string]bool
	admitted int
}

func (l *leanLimiter) Admits(string, string) limits.Admission { return limits.AdmissionOpen }

func (l *leanLimiter) Acquire(participant, _ string, _ limits.TokenCost) (func(), limits.Admission) {
	if l.refused[participant] {
		return nil, limits.AdmissionWindowFull
	}
	l.admitted++
	return func() { l.admitted-- }, limits.AdmissionOpen
}

func (l *leanLimiter) Overdraft(_, _ string, _ limits.TokenCost) (func(), limits.Admission) {
	l.admitted++
	return func() { l.admitted-- }, limits.AdmissionOpen
}

type leanHealth struct{ ejected map[string]bool }

func (h *leanHealth) Ejected(participant, _ string) bool { return h.ejected[participant] }

type leanSnapshots struct{ snapshot chain.PhaseSnapshot }

func (s *leanSnapshots) Snapshot() chain.PhaseSnapshot { return s.snapshot }

type drainBenchConfig struct {
	hosts       int
	waiters     int
	excluded    []string
	refused     []string
	ejected     []string
	enqueuedAgo time.Duration
}

// drainBench drives one drain over a real Scheduler's predicates, so the per-drain freeze is measured too.
type drainBench struct {
	dispatcher *dispatcher
	session    *drainSession
	queued     []*waiter
}

func newDrainBench(cfg drainBenchConfig) *drainBench {
	slots := make([]string, 0, cfg.hosts)
	for index := range cfg.hosts {
		slots = append(slots, benchParticipant(index))
	}
	session := &drainSession{slots: slots, keys: slots}

	settings := config.Defaults()
	scheduler := &Scheduler{
		limiter:      &leanLimiter{refused: asSet(cfg.refused)},
		perf:         &leanHealth{ejected: asSet(cfg.ejected)},
		settings:     config.NewHolder(&settings),
		now:          func() time.Time { return benchNow },
		blockedHosts: map[string]map[string]bool{},
	}
	escrow := Escrow{ID: escrowA, Model: benchModel, Session: session}
	target := newDispatcher(dispatcherDeps{
		escrowID:    escrow.ID,
		session:     session,
		snapshots:   &leanSnapshots{snapshot: chain.PhaseSnapshot{PreservedByModel: map[string][]string{benchModel: slots}}},
		predicates:  scheduler.predicates(escrow),
		acquireSlot: scheduler.acquireSlot(escrow),
		now:         scheduler.now,
		matchWait:   matchWaitWindow,
	})

	queued := make([]*waiter, 0, cfg.waiters)
	for range cfg.waiters {
		queued = append(queued, newWaiter(
			RequestProfile{Model: benchModel, Exclude: cfg.excluded, Params: "payload"},
			benchNow.Add(-cfg.enqueuedAgo),
		))
	}
	return &drainBench{dispatcher: target, session: session, queued: queued}
}

func asSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

// reset requeues the same waiters: allocating fresh ones per iteration would measure the allocator.
func (b *drainBench) reset() {
	b.dispatcher.waiting = append(b.dispatcher.waiting[:0], b.queued...)
	for _, queued := range b.queued {
		select {
		case <-queued.replyCh:
		default:
		}
	}
	b.session.nonce = 0
}

// BenchmarkDrainServe is the healthy path: every waiter takes the next nonce bound to a host it accepts.
func BenchmarkDrainServe(b *testing.B) {
	for _, waiters := range []int{1, 4, 16} {
		bench := newDrainBench(drainBenchConfig{hosts: 16, waiters: waiters})
		b.Run(fmt.Sprintf("waiters=%d", waiters), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				bench.reset()
				bench.dispatcher.drain()
			}
		})
	}
}

// BenchmarkDrainThrottledBurn walks a group whose slots all refuse admission.
func BenchmarkDrainThrottledBurn(b *testing.B) {
	hosts := 16
	refused := make([]string, 0, hosts)
	for index := range hosts {
		refused = append(refused, benchParticipant(index))
	}
	bench := newDrainBench(drainBenchConfig{hosts: hosts, waiters: 4, refused: refused, enqueuedAgo: time.Second})
	b.ReportAllocs()
	for b.Loop() {
		bench.reset()
		bench.dispatcher.drain()
	}
}

// BenchmarkDrainSweepDrop is the outage shape, where no host can serve.
func BenchmarkDrainSweepDrop(b *testing.B) {
	hosts := 16
	ejected := make([]string, 0, hosts)
	for index := range hosts {
		ejected = append(ejected, benchParticipant(index))
	}
	for _, waiters := range []int{1, 16} {
		bench := newDrainBench(drainBenchConfig{hosts: hosts, waiters: waiters, ejected: ejected})
		b.Run(fmt.Sprintf("waiters=%d", waiters), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				bench.reset()
				bench.dispatcher.drain()
			}
		})
	}
}

// BenchmarkDrainHold is the wake-up that decides nothing.
func BenchmarkDrainHold(b *testing.B) {
	bench := newDrainBench(drainBenchConfig{hosts: 16, waiters: 4, excluded: []string{benchParticipant(1)}})
	b.ReportAllocs()
	for b.Loop() {
		bench.reset()
		if _, held := bench.dispatcher.drain(); !held {
			b.Fatal("expected the drain to hold the nonce")
		}
	}
}

// BenchmarkFreezePredicates times the drain's fixed cost.
func BenchmarkFreezePredicates(b *testing.B) {
	bench := newDrainBench(drainBenchConfig{hosts: 16, waiters: 1})
	target := bench.dispatcher
	b.ReportAllocs()
	for b.Loop() {
		avail := freeze(target.predicates(target.snapshots.Snapshot()), 16)
		_ = admit(&avail, target.acquireSlot)
	}
}

// BenchmarkNewWaiter covers the per-request cost, where an exclusion list is the exception.
func BenchmarkNewWaiter(b *testing.B) {
	for _, excludes := range []int{0, 2} {
		exclude := make([]string, 0, excludes)
		for index := range excludes {
			exclude = append(exclude, benchParticipant(index))
		}
		b.Run(fmt.Sprintf("excludes=%d", excludes), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				waiterSink = newWaiter(RequestProfile{Model: benchModel, Exclude: exclude}, benchNow)
			}
		})
	}
}
