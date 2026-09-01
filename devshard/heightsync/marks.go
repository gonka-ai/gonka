package heightsync

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sync"

	"devshard/signing"
)

// MarkKind names an attributable height-sync outcome (spec §14 result
// classes). None of these INVALID-ate the diff except via a separate
// log-plane check.
type MarkKind string

const (
	MarkDisputeOriginator   MarkKind = "dispute_originator"
	MarkDisputeCarrier      MarkKind = "dispute_carrier"
	MarkVectorContradiction MarkKind = "vector_contradiction"
	MarkDeferredFail        MarkKind = "deferred_fail"
	MarkAdmissionDelta      MarkKind = "l5a_admission"
	// MarkFloorOutOfBand names a signer whose reference height no other party
	// is within W_conf of. The floor does not follow such a claim, so this is
	// how the attempt stays on the record instead of being a silent clamp.
	MarkFloorOutOfBand MarkKind = "floor_out_of_band"
)

// AttributableMark is an append-only evidence record: kind, slot, turn_seq,
// nonce, and the verbatim blob + signature that later verifiers cannot recompute.
//
// Request-leg L4 keeps the signed HTTP (body, sig, ts, escrow_id).
// Response-leg L4 keeps the origin canonical blob + field 8.
// Origin names the party that first put a carried (height, hash) into the log,
// when the marked leg repeats the floor rather than claiming a height of its
// own. It is empty when the marked signer authored the pair itself.
type AttributableMark struct {
	Kind      MarkKind
	Slot      uint32
	TurnStart uint64 // turn identity: span-start nonce
	Nonce     uint64
	Blob      []byte
	Sig       []byte
	EscrowID  string
	Timestamp int64
	Detail    string
	// Origin and OriginNonce attribute a carried pair to whoever originated it.
	// The producer rule obliges a lagging party to carry F(m), so a bad pair
	// reaching L6 through a carrier is not the carrier's misbehaviour.
	Origin      string
	OriginNonce uint64
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
// past the ±30s admission window (spec §15 request leg).
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

const (
	// DefaultMarkLogCapacity is the ring size for both the SM and transport
	// mark logs (same order as AuditRing). Older marks are dropped.
	DefaultMarkLogCapacity = 1024
	// MaxMarkBlobBytes caps AttributableMark.Blob. Request-leg L4 stores the
	// 32-byte CanonicalRequestLegBytes digest; anything larger is hashed on
	// Append so an attacker-chosen HTTP body cannot grow the log.
	MaxMarkBlobBytes = 256
)

// VerifyRequestLegMark recovers the signer of a retained request-leg mark.
// Production L4 stores CanonicalRequestLegBytes in Blob (not the HTTP body).
func VerifyRequestLegMark(verifier signing.Verifier, m AttributableMark) (string, error) {
	if verifier == nil {
		return "", fmt.Errorf("heightsync: nil verifier")
	}
	if len(m.Sig) == 0 {
		return "", fmt.Errorf("heightsync: missing request-leg signature")
	}
	if len(m.Blob) != sha256.Size {
		return "", fmt.Errorf("heightsync: request-leg mark blob must be %d-byte digest", sha256.Size)
	}
	addr, err := verifier.RecoverAddress(m.Blob, m.Sig)
	if err != nil {
		return "", fmt.Errorf("heightsync: recover request-leg address: %w", err)
	}
	return addr, nil
}

// MarkLog is a bounded ring of L4/L5a/L6/L7 marks.
type MarkLog struct {
	mu    sync.Mutex
	cap   int
	start int
	size  int
	buf   []AttributableMark
}

// NewMarkLog constructs a log with DefaultMarkLogCapacity.
func NewMarkLog() *MarkLog {
	return NewMarkLogCapacity(DefaultMarkLogCapacity)
}

// NewMarkLogCapacity constructs a log that retains at most n marks.
func NewMarkLogCapacity(n int) *MarkLog {
	if n <= 0 {
		n = DefaultMarkLogCapacity
	}
	return &MarkLog{cap: n, buf: make([]AttributableMark, n)}
}

func copyMark(m AttributableMark) AttributableMark {
	m.Blob = capMarkBlob(m.Blob)
	m.Sig = append([]byte(nil), m.Sig...)
	return m
}

func capMarkBlob(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	if len(b) <= MaxMarkBlobBytes {
		return append([]byte(nil), b...)
	}
	sum := sha256.Sum256(b)
	return sum[:]
}

// Append stores a copy of m, dropping the oldest entry when the ring is full.
//
// This is also where marks are counted. Retention is the single point where a
// mark becomes a fact about the escrow: CheckDiffLogPlane may produce the same
// mark on every trial-loop prefix and on every replay, and a rolled-back apply
// discards its marks without ever reaching here.
func (l *MarkLog) Append(m AttributableMark) {
	if l == nil {
		return
	}
	m = copyMark(m)
	l.mu.Lock()
	l.appendLocked(m)
	l.mu.Unlock()
	IncMarks(string(m.Kind))
	if m.Kind == MarkAdmissionDelta {
		IncStaleStamp("l5a_admission")
	}
}

func (l *MarkLog) appendLocked(m AttributableMark) {
	if l.cap <= 0 || len(l.buf) == 0 {
		l.cap = DefaultMarkLogCapacity
		l.buf = make([]AttributableMark, l.cap)
		l.start, l.size = 0, 0
	}
	if l.size < len(l.buf) {
		idx := (l.start + l.size) % len(l.buf)
		l.buf[idx] = m
		l.size++
		return
	}
	l.buf[l.start] = m
	l.start = (l.start + 1) % len(l.buf)
}

// AppendAll stores copies of ms.
func (l *MarkLog) AppendAll(ms []AttributableMark) {
	for _, m := range ms {
		l.Append(m)
	}
}

// Len is the number of marks currently retained.
func (l *MarkLog) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.size
}

// All returns a copy of stored marks in insertion order (oldest → newest).
func (l *MarkLog) All() []AttributableMark {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size == 0 {
		return nil
	}
	out := make([]AttributableMark, 0, l.size)
	for i := 0; i < l.size; i++ {
		idx := (l.start + i) % len(l.buf)
		out = append(out, copyMark(l.buf[idx]))
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
	for i := 0; i < l.size; i++ {
		idx := (l.start + i) % len(l.buf)
		if l.buf[idx].Kind == kind {
			return true
		}
	}
	return false
}
