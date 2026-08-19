package heightsync

import (
	"sync"
	"time"
)

type peerTip struct {
	height uint64
	at     time.Time
}

// PeerSeen is the bitmap of slots this host holds a height claim for, fresh within F.
type PeerSeen struct {
	mu        sync.Mutex
	slotsNum  uint32
	freshness time.Duration
	tips      map[uint32]peerTip
}

// NewPeerSeen constructs an empty bitmap. freshness ≤ 0 uses DefaultOriginatorFreshness.
func NewPeerSeen(slotsNum uint32, freshness time.Duration) *PeerSeen {
	if slotsNum == 0 {
		slotsNum = 1
	}
	if freshness <= 0 {
		freshness = DefaultOriginatorFreshness
	}
	return &PeerSeen{
		slotsNum:  slotsNum,
		freshness: freshness,
		tips:      make(map[uint32]peerTip),
	}
}

// MarkFresh records a height claim for slot (from Diff or a repair probe).
func (p *PeerSeen) MarkFresh(slot uint32, h uint64, at time.Time) {
	if p == nil || slot >= p.slotsNum {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tips[slot] = peerTip{height: h, at: at}
}

// Bytes returns the bitmap with bits expired past F cleared. Bit j is slot j.
func (p *PeerSeen) Bytes() []byte {
	return p.BytesAt(time.Now())
}

// BytesAt is Bytes with an explicit clock (tests).
func (p *PeerSeen) BytesAt(now time.Time) []byte {
	if p == nil {
		return nil
	}
	n := (int(p.slotsNum) + 7) / 8
	out := make([]byte, n)
	p.mu.Lock()
	defer p.mu.Unlock()
	for slot, tip := range p.tips {
		if now.Sub(tip.at) > p.freshness {
			delete(p.tips, slot)
			continue
		}
		out[slot/8] |= 1 << (slot % 8)
	}
	return out
}
