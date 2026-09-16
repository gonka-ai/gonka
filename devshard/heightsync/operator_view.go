package heightsync

import (
	"encoding/json"
	"strconv"
	"time"
)

// SlotIdentity maps a roster slot to the host that occupies it.
type SlotIdentity struct {
	Slot           uint32 `json:"slot"`
	ParticipantKey string `json:"participant_key"`
}

// OriginTip is one host's first-party height claim (response-leg Anchor).
type OriginTip struct {
	Slot       uint32        `json:"slot"`
	Originator string        `json:"originator"`
	Height     uint64        `json:"height"`
	Age        time.Duration `json:"age"`
	Verified   bool          `json:"verified"`
	Fresh      bool          `json:"fresh"`
	// AgeKnown is false when the host published no observation time. Age then
	// counts from local receipt and Fresh is false: a claim with no time on it
	// is arbitrarily old, never brand new.
	AgeKnown bool `json:"age_known"`
}

// MarshalJSON encodes Age as milliseconds for GET /v1/debug/heightsync.
func (t OriginTip) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Slot       uint32 `json:"slot"`
		Originator string `json:"originator"`
		Height     uint64 `json:"height"`
		Age        int64  `json:"age"`
		Verified   bool   `json:"verified"`
		Fresh      bool   `json:"fresh"`
		AgeKnown   bool   `json:"age_known"`
	}{
		Slot: t.Slot, Originator: t.Originator, Height: t.Height,
		Age: t.Age.Milliseconds(), Verified: t.Verified, Fresh: t.Fresh,
		AgeKnown: t.AgeKnown,
	})
}

// SlotContact is how long this sequencer has gone without sending to / hearing from a slot.
type SlotContact struct {
	Slot         uint32        `json:"slot"`
	SinceContact time.Duration `json:"since_contact"`
	LastAt       time.Time     `json:"last_at"`
}

// MarshalJSON encodes SinceContact as milliseconds for GET /v1/debug/heightsync.
func (c SlotContact) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Slot         uint32    `json:"slot"`
		SinceContact int64     `json:"since_contact"`
		LastAt       time.Time `json:"last_at"`
	}{
		Slot: c.Slot, SinceContact: c.SinceContact.Milliseconds(), LastAt: c.LastAt,
	})
}

// PeerSeenRow is one observer's bitmap of which subjects it currently sees.
type PeerSeenRow struct {
	Observer uint32 `json:"observer"`
	Bits     []byte `json:"bits"`
	Count    int    `json:"count"`
}

// SlotSyncState is the last self-report from a slot's ack.
type SlotSyncState struct {
	Slot  uint32 `json:"slot"`
	State string `json:"state"`
}

// SealedAnchorCounts is the most recently sealed height's per-kind totals.
type SealedAnchorCounts struct {
	Height    uint64         `json:"height"`
	ByKind    map[string]int `json:"by_kind"`
	Turnovers int            `json:"turnovers"`
}

// SealedHeightDetail is one sealed height for the debug surface (last ~32).
type SealedHeightDetail struct {
	Height    uint64         `json:"height"`
	ByKind    map[string]int `json:"by_kind"`
	Turnovers int            `json:"turnovers"`
	Empty     bool           `json:"empty"`
}

// ExchangeOverlap is the stamp/section measurement that gates §10.1.
type ExchangeOverlap struct {
	Total       uint64 `json:"total"`
	WithSection uint64 `json:"with_section"`
	WithStamp   uint64 `json:"with_stamp"`
	Agreed      uint64 `json:"agreed"`
}

// OperatorView is a copied snapshot for gateway scrape and GET /v1/debug/heightsync.
// Callers must not hold the dispatch lock across Collect; this value is already a copy.
type OperatorView struct {
	DevshardID           string               `json:"devshard_id"`
	Now                  time.Time            `json:"now"`
	Freshness            time.Duration        `json:"freshness"`
	IdleTimeout          time.Duration        `json:"idle_timeout"`
	AckDeadlineBlocks    uint64               `json:"ack_deadline_blocks"`
	GatewayTip           uint64               `json:"gateway_tip"`
	Slots                []SlotIdentity       `json:"slots"`
	Tips                 []OriginTip          `json:"tips"`
	Contacts             []SlotContact        `json:"contacts"`
	CadenceEvents        []CadenceEvent       `json:"cadence_events"`
	CadenceCounts        map[string]uint64    `json:"cadence_counts"`
	AbandonedTurns       uint64               `json:"abandoned_turns"`
	SecondsSinceTurnover float64              `json:"seconds_since_turnover"`
	PeerSeen             []PeerSeenRow        `json:"peer_seen"`
	SyncStates           []SlotSyncState      `json:"sync_states"`
	AnchorsLastSealed    *SealedAnchorCounts  `json:"anchors_last_sealed"`
	AnchorsPerBlock      HistogramSnapshot    `json:"anchors_per_block"`
	TurnoversPerBlock    HistogramSnapshot    `json:"turnovers_per_block"`
	BlocksWithoutAnchor  uint64               `json:"blocks_without_anchor"`
	AnchorsLate          uint64               `json:"anchors_late"`
	AnchorsFuture        uint64               `json:"anchors_future"`
	Overlap              ExchangeOverlap      `json:"overlap"`
	DebugHeights         []SealedHeightDetail `json:"sealed_heights"`
}

// MarshalJSON encodes Freshness and IdleTimeout as milliseconds for
// GET /v1/debug/heightsync. Nested duration fields use their own
// MarshalJSON (OriginTip, SlotContact, CadenceEvent).
func (v OperatorView) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		DevshardID           string               `json:"devshard_id"`
		Now                  time.Time            `json:"now"`
		Freshness            int64                `json:"freshness"`
		IdleTimeout          int64                `json:"idle_timeout"`
		AckDeadlineBlocks    uint64               `json:"ack_deadline_blocks"`
		GatewayTip           uint64               `json:"gateway_tip"`
		Slots                []SlotIdentity       `json:"slots"`
		Tips                 []OriginTip          `json:"tips"`
		Contacts             []SlotContact        `json:"contacts"`
		CadenceEvents        []CadenceEvent       `json:"cadence_events"`
		CadenceCounts        map[string]uint64    `json:"cadence_counts"`
		AbandonedTurns       uint64               `json:"abandoned_turns"`
		SecondsSinceTurnover float64              `json:"seconds_since_turnover"`
		PeerSeen             []PeerSeenRow        `json:"peer_seen"`
		SyncStates           []SlotSyncState      `json:"sync_states"`
		AnchorsLastSealed    *SealedAnchorCounts  `json:"anchors_last_sealed"`
		AnchorsPerBlock      HistogramSnapshot    `json:"anchors_per_block"`
		TurnoversPerBlock    HistogramSnapshot    `json:"turnovers_per_block"`
		BlocksWithoutAnchor  uint64               `json:"blocks_without_anchor"`
		AnchorsLate          uint64               `json:"anchors_late"`
		AnchorsFuture        uint64               `json:"anchors_future"`
		Overlap              ExchangeOverlap      `json:"overlap"`
		DebugHeights         []SealedHeightDetail `json:"sealed_heights"`
	}{
		DevshardID: v.DevshardID, Now: v.Now,
		Freshness: v.Freshness.Milliseconds(), IdleTimeout: v.IdleTimeout.Milliseconds(),
		AckDeadlineBlocks: v.AckDeadlineBlocks, GatewayTip: v.GatewayTip,
		Slots: v.Slots, Tips: v.Tips, Contacts: v.Contacts,
		CadenceEvents: v.CadenceEvents, CadenceCounts: v.CadenceCounts,
		AbandonedTurns: v.AbandonedTurns, SecondsSinceTurnover: v.SecondsSinceTurnover,
		PeerSeen: v.PeerSeen, SyncStates: v.SyncStates,
		AnchorsLastSealed: v.AnchorsLastSealed, AnchorsPerBlock: v.AnchorsPerBlock,
		TurnoversPerBlock: v.TurnoversPerBlock, BlocksWithoutAnchor: v.BlocksWithoutAnchor,
		AnchorsLate: v.AnchorsLate, AnchorsFuture: v.AnchorsFuture,
		Overlap: v.Overlap, DebugHeights: v.DebugHeights,
	})
}

// HistogramSnapshot is enough to emit a ConstHistogram on scrape.
type HistogramSnapshot struct {
	Count   uint64
	Sum     float64
	Buckets map[float64]uint64 // cumulative counts keyed by upper bound (excluding +Inf)
}

// MarshalJSON encodes bucket bounds as strings. encoding/json cannot marshal
// map[float64]uint64, and GET /v1/debug/heightsync embeds this type.
func (h HistogramSnapshot) MarshalJSON() ([]byte, error) {
	type wire struct {
		Count   uint64            `json:"count"`
		Sum     float64           `json:"sum"`
		Buckets map[string]uint64 `json:"buckets,omitempty"`
	}
	w := wire{Count: h.Count, Sum: h.Sum}
	if len(h.Buckets) > 0 {
		w.Buckets = make(map[string]uint64, len(h.Buckets))
		for bound, n := range h.Buckets {
			w.Buckets[strconv.FormatFloat(bound, 'g', -1, 64)] = n
		}
	}
	return json.Marshal(w)
}

// PeerSeenBit reports whether bits has slot j set (bit j is slot j).
func PeerSeenBit(bits []byte, slot uint32) bool {
	i := int(slot / 8)
	if i >= len(bits) {
		return false
	}
	return bits[i]&(1<<(slot%8)) != 0
}

// PeerSeenPopcount is the number of set bits in the first slotsNum slots.
func PeerSeenPopcount(bits []byte, slotsNum uint32) int {
	n := 0
	for s := uint32(0); s < slotsNum; s++ {
		if PeerSeenBit(bits, s) {
			n++
		}
	}
	return n
}
