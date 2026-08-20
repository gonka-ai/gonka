package heightsync

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sync"

	"devshard/signing"
)

// MarkKind names an attributable height-sync outcome (plan §8.7). None of
// these INVALID-ate the diff except via a separate log-plane check.
type MarkKind string

const (
	MarkDisputeOriginator   MarkKind = "dispute_originator"
	MarkDisputeCarrier      MarkKind = "dispute_carrier"
	MarkVectorContradiction MarkKind = "vector_contradiction"
	MarkDeferredFail        MarkKind = "deferred_fail"
	MarkAdmissionDelta      MarkKind = "l5a_admission"
)

// AttributableMark is an append-only evidence record: kind, slot, turn_seq,
// nonce, and the verbatim blob + signature that later verifiers cannot recompute.
//
// Request-leg L4 keeps the signed HTTP (body, sig, ts, escrow_id).
// Response-leg L4 keeps the origin canonical blob + field 8.
type AttributableMark struct {
	Kind      MarkKind
	Slot      uint32
	TurnSeq   uint64
	Nonce     uint64
	Blob      []byte
	Sig       []byte
	EscrowID  string
	Timestamp int64
	Detail    string
}

// RequestLegEvidence is the user-signed HTTP request covering a request-leg section.
type RequestLegEvidence struct {
	Body      []byte
	Sig       []byte
	Timestamp int64
	EscrowID  string
}

// CanonicalRequestLegBytes is sha256(escrow_id || body || timestamp_be8),
// matching transport.signatureMessage so a retained mark verifies offline
// past the ±30s admission window (H32).
func CanonicalRequestLegBytes(escrowID string, body []byte, ts int64) []byte {
	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(ts))
	h := sha256.New()
	h.Write([]byte(escrowID))
	h.Write(body)
	h.Write(tsBuf[:])
	return h.Sum(nil)
}

// VerifyRequestLegOffline recovers the signer of a request-leg mark with no
// clock-drift check.
func VerifyRequestLegOffline(verifier signing.Verifier, ev RequestLegEvidence) (string, error) {
	if verifier == nil {
		return "", fmt.Errorf("heightsync: nil verifier")
	}
	if len(ev.Sig) == 0 {
		return "", fmt.Errorf("heightsync: missing request-leg signature")
	}
	msg := CanonicalRequestLegBytes(ev.EscrowID, ev.Body, ev.Timestamp)
	addr, err := verifier.RecoverAddress(msg, ev.Sig)
	if err != nil {
		return "", fmt.Errorf("heightsync: recover request-leg address: %w", err)
	}
	return addr, nil
}

// MarkLog is an append-only store for L4/L5a/L6/L7 marks.
type MarkLog struct {
	mu    sync.Mutex
	marks []AttributableMark
}

// NewMarkLog constructs an empty log.
func NewMarkLog() *MarkLog {
	return &MarkLog{}
}

// Append stores a copy of m.
func (l *MarkLog) Append(m AttributableMark) {
	if l == nil {
		return
	}
	m.Blob = append([]byte(nil), m.Blob...)
	m.Sig = append([]byte(nil), m.Sig...)
	l.mu.Lock()
	l.marks = append(l.marks, m)
	l.mu.Unlock()
}

// AppendAll stores copies of ms.
func (l *MarkLog) AppendAll(ms []AttributableMark) {
	for _, m := range ms {
		l.Append(m)
	}
}

// All returns a copy of stored marks.
func (l *MarkLog) All() []AttributableMark {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]AttributableMark, len(l.marks))
	for i, m := range l.marks {
		m.Blob = append([]byte(nil), m.Blob...)
		m.Sig = append([]byte(nil), m.Sig...)
		out[i] = m
	}
	return out
}

// HasKind reports whether any mark of kind is present.
func (l *MarkLog) HasKind(kind MarkKind) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.marks {
		if m.Kind == kind {
			return true
		}
	}
	return false
}
