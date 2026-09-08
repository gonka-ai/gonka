package accounting

import (
	"sort"
	"time"
)

// maxProtocolEventsPerEscrow bounds the newest-wins ring each escrow keeps. A miss or an invalid is
// rare and an escrow dies with its epoch, so this only caps a pathological run rather than trimming
// normal traffic. Without a bound the feed would reproduce the growth this endpoint set just fixed.
const maxProtocolEventsPerEscrow = 256

// ProtocolEvent is one chain-applied verdict against a slot. Counters answer how many misses a
// participant took; this answers which nonce and which client request took them.
type ProtocolEvent struct {
	Nonce     uint64       `json:"nonce"`
	SlotID    uint32       `json:"slot_id"`
	Kind      ProtocolKind `json:"kind"`
	RequestID string       `json:"request_id,omitempty"`
	At        time.Time    `json:"at"`
}

// ProtocolEventRecord resolves a ProtocolEvent against the escrow and participant it belongs to.
type ProtocolEventRecord struct {
	EscrowID    string       `json:"escrow_id"`
	Participant string       `json:"participant"`
	Model       string       `json:"model"`
	Nonce       uint64       `json:"nonce"`
	SlotID      uint32       `json:"slot_id"`
	Kind        ProtocolKind `json:"kind"`
	RequestID   string       `json:"request_id,omitempty"`
	At          time.Time    `json:"at"`
}

// recordsProtocolEvent reports whether a verdict is one a reader needs per nonce. Receipts and
// finishes are the normal path and would bury the two verdicts that cost a participant.
func recordsProtocolEvent(kind ProtocolKind) bool {
	return kind == ProtocolTimeoutApplied || kind == ProtocolInvalidated
}

func (e *escrowState) appendProtocolEvent(nonce uint64, slot uint32, kind ProtocolKind, at time.Time) {
	requestID := ""
	if live := e.Live[nonce]; live != nil {
		requestID = live.RequestID
	}
	e.Events = append(e.Events, ProtocolEvent{
		Nonce:     nonce,
		SlotID:    slot,
		Kind:      kind,
		RequestID: requestID,
		At:        at,
	})
	if overflow := len(e.Events) - maxProtocolEventsPerEscrow; overflow > 0 {
		copy(e.Events, e.Events[overflow:])
		e.Events = e.Events[:maxProtocolEventsPerEscrow]
	}
}

// Events returns the misses and invalids matching the filter, newest first.
func (t *Tracker) Events(filter QueryFilter) []ProtocolEventRecord {
	views, _ := t.viewsFor(filter)
	records := make([]ProtocolEventRecord, 0)
	for i := range views {
		escrow := &views[i]
		for _, event := range escrow.events {
			participant := participantForSlot(escrow.meta, event.SlotID)
			if filter.Participant != "" && participant != filter.Participant {
				continue
			}
			records = append(records, ProtocolEventRecord{
				EscrowID:    escrow.id,
				Participant: participant,
				Model:       escrow.meta.Model,
				Nonce:       event.Nonce,
				SlotID:      event.SlotID,
				Kind:        event.Kind,
				RequestID:   event.RequestID,
				At:          event.At,
			})
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].At.Equal(records[j].At) {
			if records[i].EscrowID == records[j].EscrowID {
				return records[i].Nonce > records[j].Nonce
			}
			return records[i].EscrowID < records[j].EscrowID
		}
		return records[i].At.After(records[j].At)
	})
	return records
}

func participantForSlot(meta EscrowMetadata, slotID uint32) string {
	for _, slot := range meta.Slots {
		if slot.SlotID == slotID {
			return slot.ValidatorAddress
		}
	}
	return ""
}
