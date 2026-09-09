package heightsync

import (
	"encoding/json"
	"time"

	"devshard/logging"
)

// CadenceEventKind is the disposition of one heartbeat due-check.
type CadenceEventKind string

const (
	CadenceHeartbeatOpened       CadenceEventKind = "heartbeat_opened"
	CadenceDischargedByInference CadenceEventKind = "discharged_by_inference"
	CadenceSkippedNoHeight       CadenceEventKind = "skipped_no_height"
	CadenceTurnAbandoned         CadenceEventKind = "turn_abandoned"
	CadenceTurnSettledDegraded   CadenceEventKind = "turn_settled_degraded"
)

// DefaultCadenceRingCapacity is the last-N heartbeat due-check ring kept for
// GET /v1/debug/heightsync and cadence_counts.
const DefaultCadenceRingCapacity = 64

// CadenceEvent is one due-check outcome. Copied on read; never holds producer locks.
type CadenceEvent struct {
	At                 time.Time        `json:"at"`
	Event              CadenceEventKind `json:"event"`
	TurnStart          uint64           `json:"turn_start"`
	HRef               uint64           `json:"h_ref"`
	Span               int              `json:"span"`
	SlotsAcked         int              `json:"slots_acked"`
	Quorum             int              `json:"quorum"`
	Outcome            string           `json:"outcome,omitempty"`
	DurationToTurnover time.Duration    `json:"duration_to_turnover"`
	Reason             string           `json:"reason,omitempty"`
}

// MarshalJSON encodes DurationToTurnover as milliseconds for
// GET /v1/debug/heightsync. Prometheus gauges stay in seconds.
func (e CadenceEvent) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		At                 time.Time        `json:"at"`
		Event              CadenceEventKind `json:"event"`
		TurnStart          uint64           `json:"turn_start"`
		HRef               uint64           `json:"h_ref"`
		Span               int              `json:"span"`
		SlotsAcked         int              `json:"slots_acked"`
		Quorum             int              `json:"quorum"`
		Outcome            string           `json:"outcome,omitempty"`
		DurationToTurnover int64            `json:"duration_to_turnover"`
		Reason             string           `json:"reason,omitempty"`
	}{
		At: e.At, Event: e.Event, TurnStart: e.TurnStart, HRef: e.HRef,
		Span: e.Span, SlotsAcked: e.SlotsAcked, Quorum: e.Quorum,
		Outcome: e.Outcome, DurationToTurnover: e.DurationToTurnover.Milliseconds(),
		Reason: e.Reason,
	})
}

// cadenceRing holds the last-N events for the debug surface. It deliberately
// keeps no totals: two event kinds are written to the ring at most once per
// Interval, so a ring-derived count would undercount exactly the events the
// cadence ratio is meant to measure. Totals live on Heartbeat and are
// incremented per occurrence.
type cadenceRing struct {
	cap   int
	start int
	size  int
	buf   []CadenceEvent
	last  time.Time
}

func newCadenceRing(n int) cadenceRing {
	if n <= 0 {
		n = DefaultCadenceRingCapacity
	}
	return cadenceRing{
		cap: n,
		buf: make([]CadenceEvent, n),
	}
}

func (r *cadenceRing) append(ev CadenceEvent) {
	if r.cap <= 0 || len(r.buf) == 0 {
		*r = newCadenceRing(DefaultCadenceRingCapacity)
	}
	r.last = ev.At
	if r.size < len(r.buf) {
		idx := (r.start + r.size) % len(r.buf)
		r.buf[idx] = ev
		r.size++
		return
	}
	r.buf[r.start] = ev
	r.start = (r.start + 1) % len(r.buf)
}

func (r *cadenceRing) snapshot() (events []CadenceEvent, last time.Time) {
	if r.size == 0 {
		return nil, r.last
	}
	events = make([]CadenceEvent, 0, r.size)
	for i := 0; i < r.size; i++ {
		idx := (r.start + i) % len(r.buf)
		events = append(events, r.buf[idx])
	}
	return events, r.last
}

func copyCadenceCounts(in map[CadenceEventKind]uint64) map[string]uint64 {
	if len(in) == 0 {
		return map[string]uint64{}
	}
	out := make(map[string]uint64, len(in))
	for k, v := range in {
		out[string(k)] = v
	}
	return out
}

func logCadence(ev CadenceEvent) {
	kvs := []any{
		LogFieldSubsystem, "heightsync",
		LogFieldEvent, string(ev.Event),
		LogFieldTurnStart, ev.TurnStart,
		LogFieldHeight, ev.HRef,
		"span", ev.Span,
		"slots_acked", ev.SlotsAcked,
		"quorum", ev.Quorum,
		"outcome", ev.Outcome,
		"duration_to_turnover_ms", ev.DurationToTurnover.Milliseconds(),
	}
	if ev.Reason != "" {
		kvs = append(kvs, LogFieldReason, ev.Reason)
	}
	logging.Info("heightsync: cadence", kvs...)
}
