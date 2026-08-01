package accounting

import (
	"container/heap"
	"sync"
	"time"
)

type deadlineKey struct {
	escrowID string
	nonce    uint64
}

type deadlineEntry struct {
	key       deadlineKey
	at        time.Time
	kind      TimeoutKind
	phase     Phase
	outcome   *TimeoutRecord
	heapIndex int
}

type deadlineHeap []*deadlineEntry

func (h deadlineHeap) Len() int { return len(h) }

func (h deadlineHeap) Less(i, j int) bool {
	if h[i].at.Equal(h[j].at) {
		if h[i].key.escrowID == h[j].key.escrowID {
			return h[i].key.nonce < h[j].key.nonce
		}
		return h[i].key.escrowID < h[j].key.escrowID
	}
	return h[i].at.Before(h[j].at)
}

func (h deadlineHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].heapIndex = i
	h[j].heapIndex = j
}

func (h *deadlineHeap) Push(value any) {
	entry := value.(*deadlineEntry)
	entry.heapIndex = len(*h)
	*h = append(*h, entry)
}

func (h *deadlineHeap) Pop() any {
	old := *h
	last := len(old) - 1
	entry := old[last]
	old[last] = nil
	entry.heapIndex = -1
	*h = old[:last]
	return entry
}

type deadlineScheduler struct {
	mu      sync.Mutex
	entries map[deadlineKey]*deadlineEntry
	queue   deadlineHeap
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
	now     func() time.Time
	onDue   func(*deadlineEntry)
}

func newDeadlineScheduler(onDue func(*deadlineEntry)) *deadlineScheduler {
	return &deadlineScheduler{
		entries: make(map[deadlineKey]*deadlineEntry),
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		now:     time.Now,
		onDue:   onDue,
	}
}

func (s *deadlineScheduler) start() {
	go s.run()
}

func (s *deadlineScheduler) close() {
	if s == nil {
		return
	}
	s.once.Do(func() { close(s.stop) })
	<-s.done
}

func (s *deadlineScheduler) upsert(
	escrowID string,
	nonce uint64,
	at time.Time,
	kind TimeoutKind,
	phase Phase,
) {
	key := deadlineKey{escrowID: escrowID, nonce: nonce}
	s.mu.Lock()
	if current := s.entries[key]; current != nil && current.heapIndex >= 0 {
		current.at = at
		current.kind = kind
		current.phase = phase
		if current.outcome != nil {
			current.outcome.Kind = kind
		}
		heap.Fix(&s.queue, current.heapIndex)
	} else {
		var outcome *TimeoutRecord
		if current != nil && current.outcome != nil {
			copy := *current.outcome
			copy.Kind = kind
			outcome = &copy
		}
		entry := &deadlineEntry{
			key: key, at: at, kind: kind, phase: phase, outcome: outcome,
		}
		s.entries[key] = entry
		heap.Push(&s.queue, entry)
	}
	s.mu.Unlock()
	s.signal()
}

func (s *deadlineScheduler) cancel(escrowID string, nonce uint64) {
	key := deadlineKey{escrowID: escrowID, nonce: nonce}
	s.mu.Lock()
	if entry := s.entries[key]; entry != nil {
		if entry.heapIndex >= 0 {
			heap.Remove(&s.queue, entry.heapIndex)
		}
		delete(s.entries, key)
	}
	s.mu.Unlock()
	s.signal()
}

func (s *deadlineScheduler) cancelEscrow(escrowID string) {
	s.mu.Lock()
	for key, entry := range s.entries {
		if key.escrowID != escrowID {
			continue
		}
		if entry.heapIndex >= 0 {
			heap.Remove(&s.queue, entry.heapIndex)
		}
		delete(s.entries, key)
	}
	s.mu.Unlock()
	s.signal()
}

func (s *deadlineScheduler) deferOutcome(record TimeoutRecord) bool {
	key := deadlineKey{escrowID: record.EscrowID, nonce: record.Nonce}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[key]
	if entry == nil || !entry.at.After(s.now()) {
		return false
	}
	copy := record
	copy.Kind = entry.kind
	if copy.Phase == "" {
		copy.Phase = entry.phase
	}
	entry.outcome = &copy
	return true
}

func (s *deadlineScheduler) fireDue(now time.Time) {
	var due []*deadlineEntry
	s.mu.Lock()
	for len(s.queue) > 0 && !s.queue[0].at.After(now) {
		entry := heap.Pop(&s.queue).(*deadlineEntry)
		due = append(due, entry)
	}
	s.mu.Unlock()
	for _, entry := range due {
		s.onDue(entry)
	}
}

func (s *deadlineScheduler) claim(entry *deadlineEntry) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry == nil || s.entries[entry.key] != entry {
		return false
	}
	delete(s.entries, entry.key)
	return true
}

func (s *deadlineScheduler) run() {
	defer close(s.done)
	timer := time.NewTimer(time.Hour)
	stopTimer(timer)
	defer timer.Stop()
	for {
		s.mu.Lock()
		var next time.Time
		if len(s.queue) > 0 {
			next = s.queue[0].at
		}
		s.mu.Unlock()

		var timerC <-chan time.Time
		if !next.IsZero() {
			delay := next.Sub(s.now())
			if delay < 0 {
				delay = 0
			}
			timer.Reset(delay)
			timerC = timer.C
		}
		select {
		case <-s.stop:
			return
		case <-s.wake:
			stopTimer(timer)
		case now := <-timerC:
			s.fireDue(now)
		}
	}
}

func (s *deadlineScheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
