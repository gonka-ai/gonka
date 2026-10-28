package accounting

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

// Caps a pathological run rather than trimming normal traffic: a verdict is rare and an escrow dies with its epoch.
const maxEventsPerEscrow = 256

type ProtocolKind string

// protocolEvent is one chain-applied verdict against a slot, kept on the escrow that owns the nonce.
type protocolEvent struct {
	nonce     uint64
	slotID    uint32
	kind      ProtocolKind
	requestID string
	at        time.Time
}

// ProtocolEventRecord is the drill-down under the counters: which nonce and which request took a verdict.
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

func (e *escrowLedger) appendEvent(nonce uint64, kind ProtocolKind, at time.Time) {
	requestID := ""
	if record, known := e.nonces[nonce]; known {
		requestID = record.requestID
	}
	e.events = append(e.events, protocolEvent{
		nonce: nonce, slotID: e.slotOf(nonce), kind: kind, requestID: requestID, at: at.UTC(),
	})
	if overflow := len(e.events) - maxEventsPerEscrow; overflow > 0 {
		e.events = append(e.events[:0], e.events[overflow:]...)
	}
}

// Events returns the verdicts the filter admits, newest first.
func (b *Book) Events(filter QueryFilter) []ProtocolEventRecord {
	b.mu.RLock()
	defer b.mu.RUnlock()

	admitted := b.admittedEscrows(filter)
	held := 0
	for _, escrowID := range admitted {
		held += len(b.escrows[escrowID].events)
	}
	records := make([]ProtocolEventRecord, 0, held)
	for _, escrowID := range admitted {
		escrow := b.escrows[escrowID]
		for _, event := range escrow.events {
			participant := escrow.participantOf(event.slotID)
			if filter.Participant != "" && participant != filter.Participant {
				continue
			}
			records = append(records, ProtocolEventRecord{
				EscrowID:    escrowID,
				Participant: participant,
				Model:       escrow.metadata.Model,
				Nonce:       event.nonce,
				SlotID:      event.slotID,
				Kind:        event.kind,
				RequestID:   event.requestID,
				At:          event.at,
			})
		}
	}
	slices.SortStableFunc(records, compareProtocolEvent)
	return records
}

func compareProtocolEvent(first, second ProtocolEventRecord) int {
	if !first.At.Equal(second.At) {
		return second.At.Compare(first.At)
	}
	if first.EscrowID != second.EscrowID {
		return strings.Compare(first.EscrowID, second.EscrowID)
	}
	return cmp.Compare(second.Nonce, first.Nonce)
}
