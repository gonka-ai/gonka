package apiconfig

import (
	"sync"
	"time"
)

// HostEventKind identifies discrete escrow/maintenance events on the host-events ring.
// Values match nodemanager.HostEventKind (1–2 reserved for a possible EPOCH/PARAMS fold).
type HostEventKind int32

const (
	HostEventKindUnspecified          HostEventKind = 0
	HostEventKindEscrowCreated        HostEventKind = 3
	HostEventKindEscrowSettled        HostEventKind = 4
	HostEventKindMaintenanceScheduled HostEventKind = 5
	HostEventKindMaintenanceCanceled  HostEventKind = 6
	HostEventKindWarmKeyGranted       HostEventKind = 7
	HostEventKindWarmKeyRevoked       HostEventKind = 8
	HostEventKindBlock                HostEventKind = 9
)

// WarmKeyPayload is the warm-key-log body of a HostEvent: one grant or one
// end of a grant, as the inference module emitted it.
type WarmKeyPayload struct {
	Granter       string
	Grantee       string
	GrantID       uint64
	Height        int64
	ExpiresAtUnix int64 // unix nanoseconds; 0 when the grant has no expiration
	End           string
}

// EscrowPayload is the escrow-specific body of a HostEvent.
type EscrowPayload struct {
	EscrowID    uint64
	EpochIndex  uint64
	ModelID     string
	Creator     string
	Amount      string
	Settler     string
	TotalPayout string
	Fees        string
	Remainder   string
}

// MaintenancePayload is the maintenance-specific body of a HostEvent.
type MaintenancePayload struct {
	ReservationID  uint64
	Participant    string
	StartHeight    int64
	DurationBlocks uint64
	Reason         string
}

// BlockPayload is the block body of a HostEvent. Hash and HeaderTimeUnixMs
// repeat data inside Header so a reader can use the block without decoding it.
// Header is the protobuf-encoded tendermint header.
type BlockPayload struct {
	Height           int64
	Hash             []byte
	HeaderTimeUnixMs int64
	EpochIndex       uint64
	Header           []byte
}

// HostEvent is one discrete entry in HostEventRing.
type HostEvent struct {
	Seq            uint64
	Kind           HostEventKind
	ObservedAtUnix int64
	Escrow         *EscrowPayload
	Maintenance    *MaintenancePayload
	WarmKey        *WarmKeyPayload
	Block          *BlockPayload
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
// Seq is monotonic for the lifetime of one generation. A restart restores the
// saved generation and the hot tail when the store matches the head marker.
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
	// sinceSlots counts slot reads in the last Since. Tests use it to show the
	// search does not walk the prefix before the cursor.
	sinceSlots int
}

// ResetGeneration drops every retained event and stamps a new boot generation.
// Clients whose cursor was issued under the previous generation see needs_reset.
// generation 0 is stored as 1, because a client generation of 0 means "unknown"
// and would not notice the change.
func (r *HostEventRing) ResetGeneration(generation uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if generation == 0 {
		generation = 1
	}
	r.generation = generation
	for i := range r.buf {
		r.buf[i] = HostEvent{}
	}
	r.start = 0
	r.size = 0
	r.nextSeq = 1
	r.lastByKind = make(map[HostEventKind]uint64)
	for w := range r.waiters {
		close(w.ch)
		delete(r.waiters, w)
	}
}

// DefaultHostEventRingCapacity is used when NewHostEventRing is given capacity <= 0.
const DefaultHostEventRingCapacity = 40960

// MaxHostEventsPage is the most events one GetHostEvents response may hold.
const MaxHostEventsPage = 1000

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
	ev = r.appendLocked(ev)
	r.wakeLocked(map[HostEventKind]struct{}{ev.Kind: {}})
	return ev
}

// AppendBatch stores events in one lock so a height is never visible in parts.
// Waiters are woken once if any stored kind is in their subscribe set.
func (r *HostEventRing) AppendBatch(events []HostEvent) []HostEvent {
	if len(events) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]HostEvent, len(events))
	kinds := make(map[HostEventKind]struct{}, len(events))
	for i, ev := range events {
		out[i] = r.appendLocked(ev)
		kinds[out[i].Kind] = struct{}{}
	}
	r.wakeLocked(kinds)
	return out
}

func (r *HostEventRing) appendLocked(ev HostEvent) HostEvent {
	if ev.ObservedAtUnix == 0 {
		ev.ObservedAtUnix = time.Now().Unix()
	}
	if ev.Seq == 0 {
		ev.Seq = r.nextSeq
		r.nextSeq++
	} else if ev.Seq >= r.nextSeq {
		r.nextSeq = ev.Seq + 1
	}
	r.lastByKind[ev.Kind] = ev.Seq

	if r.size < r.capacity {
		r.buf[(r.start+r.size)%r.capacity] = ev
		r.size++
	} else {
		// Full: overwrite the oldest slot in place and advance start.
		r.buf[r.start] = ev
		r.start = (r.start + 1) % r.capacity
	}
	return ev
}

func (r *HostEventRing) wakeLocked(kinds map[HostEventKind]struct{}) {
	for w := range r.waiters {
		for k := range kinds {
			if _, ok := w.kinds[k]; ok {
				close(w.ch)
				delete(r.waiters, w)
				break
			}
		}
	}
}

// Restore replaces the ring with a saved generation and its hot tail.
// nextSeq is the sequence number the next append uses. Events beyond capacity
// keep the newest rows.
func (r *HostEventRing) Restore(generation, nextSeq uint64, events []HostEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if generation == 0 {
		generation = 1
	}
	if nextSeq == 0 {
		nextSeq = 1
	}
	r.generation = generation
	r.nextSeq = nextSeq
	r.lastByKind = make(map[HostEventKind]uint64)
	for i := range r.buf {
		r.buf[i] = HostEvent{}
	}
	if len(events) > r.capacity {
		events = events[len(events)-r.capacity:]
	}
	r.start = 0
	r.size = len(events)
	for i, ev := range events {
		r.buf[i] = ev
		r.lastByKind[ev.Kind] = ev.Seq
	}
	for w := range r.waiters {
		close(w.ch)
		delete(r.waiters, w)
	}
}

// Bounds returns the head, the oldest retained sequence, and the generation.
// empty is true when the ring holds no events. head is 0 when nothing has been appended.
func (r *HostEventRing) Bounds() (head, oldest, generation uint64, empty bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size > 0 {
		oldest = r.buf[r.start].Seq
	}
	return r.headLocked(), oldest, r.generation, r.size == 0
}

// Since returns subscribed events with seq > cursor, in order.
//
// next_cursor advances to the global head so skipped (unsubscribed) seqs are
// covered. reset is set when clientGeneration mismatches, cursor is ahead of
// head, or cursor sits below the retained window (gap). On reset, Events is empty
// and the client must re-hydrate out of band.
//
// cursor 0 means "from the beginning of the retained window" (seq > 0).
func (r *HostEventRing) Since(cursor, clientGeneration uint64, subscribe []HostEventKind) HostEventSince {
	return r.since(cursor, clientGeneration, subscribe, 0)
}

// SincePage is Since limited to limit events. A full page sets next_cursor to
// the sequence of the last returned event. A short page sets it to the head.
func (r *HostEventRing) SincePage(cursor, clientGeneration uint64, subscribe []HostEventKind, limit int) HostEventSince {
	if limit <= 0 {
		limit = MaxHostEventsPage
	}
	return r.since(cursor, clientGeneration, subscribe, limit)
}

func (r *HostEventRing) since(cursor, clientGeneration uint64, subscribe []HostEventKind, limit int) HostEventSince {
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

	r.sinceSlots = 0
	for i := r.firstAfterLocked(cursor); i < r.size; i++ {
		ev := r.slotLocked(i)
		if _, ok := want[ev.Kind]; !ok {
			continue
		}
		out.Events = append(out.Events, ev)
		if limit > 0 && len(out.Events) >= limit {
			out.NextCursor = ev.Seq
			return out
		}
	}
	return out
}

// firstAfterLocked returns the logical index of the first live slot with
// seq > cursor. Slots are in increasing sequence order from start.
func (r *HostEventRing) firstAfterLocked(cursor uint64) int {
	lo, hi := 0, r.size
	for lo < hi {
		mid := lo + (hi-lo)/2
		if r.slotLocked(mid).Seq <= cursor {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func (r *HostEventRing) slotLocked(i int) HostEvent {
	r.sinceSlots++
	return r.buf[(r.start+i)%r.capacity]
}
