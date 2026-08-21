package heightsync

import (
	"strings"
	"sync"

	"common/chainoracle/blocks"
)

const defaultAuditRingCapacity = 1024

// AttestationTrust classifies where the host's (mainnet_height, block_hash) pair
// came from: oracle-backed emission vs ahead-of-oracle peer sync vs peer at/below oracle.
type AttestationTrust string

const (
	// TrustOracle is the host's own Anchor filled from the local block oracle (outbound response).
	TrustOracle AttestationTrust = "trusted_oracle"
	// TrustUntrustedPeer is an inbound Anchor whose height is strictly greater than the
	// local oracle height at observation time (ahead-of-oracle sync).
	TrustUntrustedPeer AttestationTrust = "untrusted_peer"
	// TrustPeerAligned is an inbound Anchor at or below the local oracle height at observation time.
	TrustPeerAligned AttestationTrust = "peer_aligned"
	// TrustDisputeCarrier marks an inbound carry-forward Anchor rejected by the
	// receiver (e.g. stale originator timestamp); dispute evidence only.
	TrustDisputeCarrier AttestationTrust = "dispute_carrier"
	// TrustForceRequestAnchorMissing is a sentinel local-only audit marker recorded
	// when an inbound request whose nonce falls inside an active forced sync turn
	// arrives without a valid Anchor section. The entry carries no oracle data
	// (MainnetHeight=0, MainnetBlockHash=nil) and serves as dispute evidence: the
	// user signed a MsgForceHeightSyncTurn that opened the window, then chose not
	// to honour it on the wire. Hosts MUST still serve the request normally.
	TrustForceRequestAnchorMissing AttestationTrust = "force_request_anchor_missing"
)

// IsAnchorSection reports whether hs is a PoC Anchor (height-anchor-v1 with height and hash).
func IsAnchorSection(hs *HeightSyncSection) bool {
	if hs == nil || hs.ProofType != AnchorProofType {
		return false
	}
	if hs.MainnetHeight <= 0 || strings.TrimSpace(hs.MainnetBlockHashHex) == "" {
		return false
	}
	return true
}

// InboundTrust classifies an inbound Anchor vs the local oracle tip at receipt time.
func InboundTrust(hs *HeightSyncSection, oracleHdr *blocks.Header) AttestationTrust {
	if !IsAnchorSection(hs) {
		return ""
	}
	localH := int64(0)
	if oracleHdr != nil {
		localH = oracleHdr.Height
	}
	if hs.MainnetHeight > localH {
		return TrustUntrustedPeer
	}
	return TrustPeerAligned
}

// AnchorAttestation captures one observed peer anchor claim.
type AnchorAttestation struct {
	PeerID           string
	Direction        string
	MainnetHeight    int64
	MainnetBlockHash []byte
	ObservedAtUnixMs int64
	SourceMessage    string
	Trust            AttestationTrust
	// Tag is cadence vs lazy for accepted inbound request Anchors (empty otherwise).
	Tag AnchorCadenceTag
	// OriginatorSenderID is the first observer of (height, hash) on carry-forward
	// request Anchors (empty for host-oracle emissions and legacy entries).
	OriginatorSenderID string
	// OriginatorTimestampMs is when the originator observed the pair. (C-quorum)
	// eligibility uses this when set so a carry-forward admitted at age F−ε
	// cannot stay quorum-eligible for another F from receipt time.
	OriginatorTimestampMs int64
	// OriginSignedBlobAvailable is true when the user cached a verified response-leg
	// signed blob for this attestation (spec §15).
	OriginSignedBlobAvailable bool
}

// AuditRing stores recent attestations in bounded per-peer rings.
type AuditRing struct {
	mu       sync.RWMutex
	capacity int
	peers    map[string]*peerRing
	confirm  *ConfirmationIndex
}

type peerRing struct {
	buf   []AnchorAttestation
	start int
	size  int
}

// NewAuditRing creates a ring indexed by peer_id. If capacity<=0,
// a default per-peer capacity is used.
func NewAuditRing(capacity int) *AuditRing {
	if capacity <= 0 {
		capacity = defaultAuditRingCapacity
	}
	return &AuditRing{
		capacity: capacity,
		peers:    make(map[string]*peerRing),
	}
}

// AttachConfirmation wires quorum tracking into subsequent Append calls.
func (r *AuditRing) AttachConfirmation(idx *ConfirmationIndex) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.confirm = idx
	r.mu.Unlock()
}

// ConfirmationView returns the attached confirmation index, or nil.
func (r *AuditRing) ConfirmationView() ConfirmationView {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	idx := r.confirm
	r.mu.RUnlock()
	return idx
}

// Append inserts one attestation, dropping the oldest entry for that peer
// when the configured capacity is reached.
func (r *AuditRing) Append(a AnchorAttestation) {
	var confirm *ConfirmationIndex
	r.mu.Lock()
	confirm = r.confirm
	r.mu.Unlock()
	if confirm != nil {
		confirm.RecordAttestation(a)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	pr := r.peers[a.PeerID]
	if pr == nil {
		pr = &peerRing{buf: make([]AnchorAttestation, r.capacity)}
		r.peers[a.PeerID] = pr
	}

	a.MainnetBlockHash = append([]byte(nil), a.MainnetBlockHash...)

	if pr.size < len(pr.buf) {
		idx := (pr.start + pr.size) % len(pr.buf)
		pr.buf[idx] = a
		pr.size++
		return
	}

	pr.buf[pr.start] = a
	pr.start = (pr.start + 1) % len(pr.buf)
}

// List returns a snapshot in insertion order (oldest->newest) for one peer.
func (r *AuditRing) List(peerID string) []AnchorAttestation {
	r.mu.RLock()
	defer r.mu.RUnlock()

	pr := r.peers[peerID]
	if pr == nil || pr.size == 0 {
		return nil
	}
	out := make([]AnchorAttestation, 0, pr.size)
	for i := 0; i < pr.size; i++ {
		idx := (pr.start + i) % len(pr.buf)
		v := pr.buf[idx]
		v.MainnetBlockHash = append([]byte(nil), v.MainnetBlockHash...)
		out = append(out, v)
	}
	return out
}

// ListPeers returns known peer ids. Order is unspecified.
func (r *AuditRing) ListPeers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.peers))
	for id := range r.peers {
		out = append(out, id)
	}
	return out
}
