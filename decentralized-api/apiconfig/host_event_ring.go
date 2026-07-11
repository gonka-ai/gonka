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

// HostEventRing is a bounded in-memory log of discrete host events with a
// fan-out wake channel (same close-and-replace pattern as RuntimeConfigNotifier).
//
// Seq is monotonic for the lifetime of one generation (dapi boot). After a
// restart the ring is empty with a new generation; clients must re-hydrate.
type HostEventRing struct {
	mu         sync.Mutex
	generation uint64
	capacity   int
	events     []HostEvent
	nextSeq    uint64 // next seq to assign; head = nextSeq-1 when nextSeq > 0
	lastByKind map[HostEventKind]uint64
	ch         chan struct{}
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
		events:     make([]HostEvent, 0, capacity),
		nextSeq:    1,
		lastByKind: make(map[HostEventKind]uint64),
		ch:         make(chan struct{}),
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

// NotifyChan returns a channel closed on the next Append. After waking,
// callers must call NotifyChan again to wait for the next event.
func (r *HostEventRing) NotifyChan() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ch
}

// Append stores a copy of ev with an assigned seq (and ObservedAtUnix if zero),
// drops the oldest entry when at capacity, and wakes all waiters.
func (r *HostEventRing) Append(ev HostEvent) HostEvent {
	r.mu.Lock()
	defer r.mu.Unlock()

	if ev.ObservedAtUnix == 0 {
		ev.ObservedAtUnix = time.Now().Unix()
	}
	ev.Seq = r.nextSeq
	r.nextSeq++
	r.lastByKind[ev.Kind] = ev.Seq

	if len(r.events) >= r.capacity {
		r.events = r.events[1:]
	}
	r.events = append(r.events, ev)

	close(r.ch)
	r.ch = make(chan struct{})
	return ev
}

// Since returns subscribed events with seq > cursor, in order.
//
// next_cursor always advances to the global head so skipped (unsubscribed) seqs
// are covered. reset is set when clientGeneration mismatches, cursor is ahead of
// head, or cursor sits below the retained window (gap). On reset, Events is empty
// and the client must re-hydrate out of band.
//
// cursor 0 means "from the beginning of the retained window" (seq > 0). Live-from-now
// is a GetHostEvents RPC policy: bump cursor to Head() before calling Since.
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

	if len(r.events) > 0 {
		oldest := r.events[0].Seq
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

	for _, ev := range r.events {
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
