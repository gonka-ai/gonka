package apiconfig

import (
	"sync"
	"time"
)

// HostEventKind identifies discrete escrow/maintenance events on the host-events ring.
// Values match nodemanager.HostEventKind (1–2 reserved for a possible EPOCH/PARAMS fold).
type HostEventKind int32

const (
	HostEventKindUnspecified           HostEventKind = 0
	HostEventKindEscrowCreated         HostEventKind = 3
	HostEventKindEscrowSettled         HostEventKind = 4
	HostEventKindMaintenanceScheduled  HostEventKind = 5
	HostEventKindMaintenanceCanceled   HostEventKind = 6
)

// EscrowPayload is the escrow-specific body of a HostEvent.
type EscrowPayload struct {
	EscrowID   uint64
	EpochIndex uint64
	ModelID    string
	Creator    string
	Amount     string
	Settler    string
	TotalPayout string
	Fees       string
	Remainder  string
}

// MaintenancePayload is the maintenance-specific body of a HostEvent.
type MaintenancePayload struct {
	ReservationID  uint64
	Participant    string
	StartHeight    int64
	DurationBlocks uint64
	Reason         string
}

// HostEvent is one discrete entry in HostEventRing.
type HostEvent struct {
	Seq            uint64
	Kind           HostEventKind
	ObservedAtUnix int64
	Escrow         *EscrowPayload
	Maintenance    *MaintenancePayload
}

// HostEventSince is the result of HostEventRing.Since.
type HostEventSince struct {
	Events     []HostEvent
	NextCursor uint64
	Reset      bool
	Generation uint64
}

// hostEventWaiter is one registered long-poll waiter and the kinds it cares
// about. Its channel is closed (once) by Append when a matching kind arrives.
type hostEventWaiter struct {
	kinds map[HostEventKind]struct{}
	ch    chan struct{}
}

// HostEventRing is a bounded in-memory log of discrete host events with a
// subscribe-filtered wake registry: a waiter is woken only by Appends whose
// kind is in its subscribe set, so unsubscribed kinds (e.g. maintenance) never
// wake an escrow-only waiter and reset its long-poll deadline.
//
// Seq is monotonic for the lifetime of one generation (dapi boot). After a
// restart the ring is empty with a new generation; clients must re-hydrate.
type HostEventRing struct {
	mu         sync.Mutex
	generation uint64
	capacity   int
	// buf is a fixed-length (capacity) ring buffer. start is the index of the
	// oldest live event; size is the number of live events (0..capacity). On
	// wrap the oldest slot is overwritten in place, so evicted events (and their
	// payload pointers) are released immediately with no reallocation or churn.
	buf        []HostEvent
	start      int
	size       int
	nextSeq    uint64 // next seq to assign; head = nextSeq-1 when nextSeq > 0
	lastByKind map[HostEventKind]uint64
	waiters    map[*hostEventWaiter]struct{}
}

// DefaultHostEventRingCapacity is used when NewHostEventRing is given capacity <= 0.
const DefaultHostEventRingCapacity = 4096

// NewHostEventRing creates an empty ring. generation is the dapi boot nonce
// echoed to clients; capacity bounds retained events (oldest dropped on wrap).
func NewHostEventRing(capacity int, generation uint64) *HostEventRing {
	if capacity <= 0 {
		capacity = DefaultHostEventRingCapacity
	}
	return &HostEventRing{
		generation: generation,
		capacity:   capacity,
		buf:        make([]HostEvent, capacity),
		nextSeq:    1,
		lastByKind: make(map[HostEventKind]uint64),
		waiters:    make(map[*hostEventWaiter]struct{}),
	}
}

// Generation returns the boot nonce stamped on this ring.
func (r *HostEventRing) Generation() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generation
}

// Head returns the last assigned seq, or 0 if nothing has been appended.
func (r *HostEventRing) Head() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.headLocked()
}

func (r *HostEventRing) headLocked() uint64 {
	if r.nextSeq == 0 {
		return 0
	}
	return r.nextSeq - 1
}

// Subscribe registers a long-poll waiter woken only when an event whose kind is
// in subscribe is appended (true wake filter — unsubscribed kinds do not wake an
// escrow-only waiter). It returns the wake channel (closed on the first matching
// Append) and a release func the caller MUST invoke when it stops waiting.
//
// Subscribe before Since to avoid lost wake-ups: an Append between Subscribe and
// Since closes the channel, so the following Wait returns immediately and the
// re-run Since observes the event.
func (r *HostEventRing) Subscribe(subscribe []HostEventKind) (<-chan struct{}, func()) {
	w := &hostEventWaiter{
		kinds: make(map[HostEventKind]struct{}, len(subscribe)),
		ch:    make(chan struct{}),
	}
	for _, k := range subscribe {
		w.kinds[k] = struct{}{}
	}
	r.mu.Lock()
	r.waiters[w] = struct{}{}
	r.mu.Unlock()
	return w.ch, func() {
		r.mu.Lock()
		delete(r.waiters, w)
		r.mu.Unlock()
	}
}

// Append stores a copy of ev with an assigned seq (and ObservedAtUnix if zero),
// drops the oldest entry when at capacity, and wakes every registered waiter
// whose subscribe set includes ev.Kind (each such waiter is woken once and
// removed; it re-subscribes for the next event).
func (r *HostEventRing) Append(ev HostEvent) HostEvent {
	r.mu.Lock()
	defer r.mu.Unlock()

	if ev.ObservedAtUnix == 0 {
		ev.ObservedAtUnix = time.Now().Unix()
	}
	ev.Seq = r.nextSeq
	r.nextSeq++
	r.lastByKind[ev.Kind] = ev.Seq

	if r.size < r.capacity {
		r.buf[(r.start+r.size)%r.capacity] = ev
		r.size++
	} else {
		// Full: overwrite the oldest slot in place and advance start.
		r.buf[r.start] = ev
		r.start = (r.start + 1) % r.capacity
	}

	for w := range r.waiters {
		if _, ok := w.kinds[ev.Kind]; ok {
			close(w.ch)
			delete(r.waiters, w)
		}
	}
	return ev
}

// Since returns subscribed events with seq > cursor, in order.
//
// next_cursor always advances to the global head so skipped (unsubscribed) seqs
// are covered. reset is set when clientGeneration mismatches, cursor is ahead of
// head, or cursor sits below the retained window (gap). On reset, Events is empty
// and the client must re-hydrate out of band.
//
// cursor 0 means "from the beginning of the retained window" (seq > 0).
// GetHostEvents passes the client cursor through so cursor 0 performs bounded catch-up.
func (r *HostEventRing) Since(cursor, clientGeneration uint64, subscribe []HostEventKind) HostEventSince {
	r.mu.Lock()
	defer r.mu.Unlock()

	head := r.headLocked()
	out := HostEventSince{
		NextCursor: head,
		Generation: r.generation,
	}

	if clientGeneration != 0 && clientGeneration != r.generation {
		out.Reset = true
		out.NextCursor = head
		return out
	}

	if cursor > head {
		out.Reset = true
		return out
	}

	if r.size > 0 {
		oldest := r.buf[r.start].Seq
		if cursor > 0 && oldest > cursor+1 {
			out.Reset = true
			return out
		}
	} else if cursor > 0 && head == 0 {
		// Non-zero cursor against an empty never-written ring (fresh boot).
		out.Reset = true
		return out
	}

	if len(subscribe) == 0 {
		return out
	}
	want := make(map[HostEventKind]struct{}, len(subscribe))
	for _, k := range subscribe {
		want[k] = struct{}{}
	}

	for i := 0; i < r.size; i++ {
		ev := r.buf[(r.start+i)%r.capacity]
		if ev.Seq <= cursor {
			continue
		}
		if _, ok := want[ev.Kind]; !ok {
			continue
		}
		out.Events = append(out.Events, ev)
	}
	return out
}
