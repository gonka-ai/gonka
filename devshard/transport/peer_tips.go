package transport

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"devshard/heightsync"
)

// defaultPeerTipFreshness matches MOCKDAPI_STALE_AFTER / proposal courier F for PoC.
const defaultPeerTipFreshness = 60 * time.Second

var _ heightsync.LazyPropagator = (*HeightSyncPeerTips)(nil)

type originTipEntry struct {
	sec  *heightsync.HeightSyncSection
	blob []byte
	sig  []byte
}

// HeightSyncPeerTips stores verbatim originator Anchor sections observed from hosts
// and tracks per-recipient propagation for lazy carry-forward (PoC v2 Step 2).
type HeightSyncPeerTips struct {
	mu sync.Mutex

	tipsByOriginator map[string]*originTipEntry
	maxTip           *heightsync.HeightSyncSection
	lastPropagated   map[string]uint64

	// Freshness bounds MaxFresh and Carry; zero uses defaultPeerTipFreshness.
	Freshness time.Duration
	// RequireVerifiedBlob when true: MaxFresh/Carry only use entries stored via
	// RecordOriginWithBlob (Step 8 courier path). Default false for unit tests.
	RequireVerifiedBlob bool
}

// NewHeightSyncPeerTips creates an empty session-scoped peer-tip cache.
func NewHeightSyncPeerTips() *HeightSyncPeerTips {
	return &HeightSyncPeerTips{
		tipsByOriginator: make(map[string]*originTipEntry),
		lastPropagated:   make(map[string]uint64),
	}
}

func (s *HeightSyncPeerTips) freshness() time.Duration {
	if s == nil || s.Freshness <= 0 {
		return defaultPeerTipFreshness
	}
	return s.Freshness
}

func isValidPeerTip(sec *heightsync.HeightSyncSection) bool {
	return sec != nil && sec.MainnetHeight > 0 && strings.TrimSpace(sec.MainnetBlockHashHex) != ""
}

func originatorCacheKey(sec *heightsync.HeightSyncSection) string {
	if id := strings.TrimSpace(sec.OriginatorSenderID); id != "" {
		return id
	}
	return fmt.Sprintf("_height_%d", sec.MainnetHeight)
}

func cloneHeightSyncSection(sec *heightsync.HeightSyncSection) *heightsync.HeightSyncSection {
	if sec == nil {
		return nil
	}
	cp := *sec
	if len(sec.SenderSignature) > 0 {
		cp.SenderSignature = append([]byte(nil), sec.SenderSignature...)
	}
	return &cp
}

func originatorObservedAtMs(sec *heightsync.HeightSyncSection) int64 {
	if sec.OriginatorTimestampMs > 0 {
		return sec.OriginatorTimestampMs
	}
	return sec.TimestampUnixMs
}

func (s *HeightSyncPeerTips) isFreshLocked(sec *heightsync.HeightSyncSection, now time.Time, freshness time.Duration) bool {
	ts := originatorObservedAtMs(sec)
	if ts <= 0 {
		return true
	}
	return now.Sub(time.UnixMilli(ts)) <= freshness
}

func (s *HeightSyncPeerTips) entryEligibleLocked(ent *originTipEntry) bool {
	if ent == nil || !isValidPeerTip(ent.sec) {
		return false
	}
	if s.RequireVerifiedBlob && (len(ent.blob) == 0 || len(ent.sig) == 0) {
		return false
	}
	return true
}

func (s *HeightSyncPeerTips) recomputeMaxTipLocked() {
	var best *heightsync.HeightSyncSection
	for _, ent := range s.tipsByOriginator {
		if !s.entryEligibleLocked(ent) {
			continue
		}
		sec := ent.sec
		if best == nil || sec.MainnetHeight > best.MainnetHeight {
			best = sec
		}
	}
	s.maxTip = best
}

func (s *HeightSyncPeerTips) storeOriginLocked(sec *heightsync.HeightSyncSection, blob, sig []byte) {
	key := originatorCacheKey(sec)
	existing := s.tipsByOriginator[key]
	if existing != nil && sec.MainnetHeight < existing.sec.MainnetHeight {
		return
	}
	cp := cloneHeightSyncSection(sec)
	ent := &originTipEntry{sec: cp, blob: append([]byte(nil), blob...), sig: append([]byte(nil), sig...)}
	s.tipsByOriginator[key] = ent
	s.recomputeMaxTipLocked()
}

// RecordOrigin stores a verbatim Anchor section keyed by originator identity.
// Unverified entries are ignored by MaxFresh when RequireVerifiedBlob is set.
func (s *HeightSyncPeerTips) RecordOrigin(sec *heightsync.HeightSyncSection) {
	if s == nil || !isValidPeerTip(sec) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storeOriginLocked(sec, nil, nil)
}

// RecordOriginWithBlob stores a verified response-leg Anchor and its signed blob (Step 8).
func (s *HeightSyncPeerTips) RecordOriginWithBlob(sec *heightsync.HeightSyncSection, blob, sig []byte) {
	if s == nil || !isValidPeerTip(sec) || len(blob) == 0 || len(sig) == 0 {
		return
	}
	cp := cloneHeightSyncSection(sec)
	cp.SenderSignature = append([]byte(nil), sig...)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storeOriginLocked(cp, blob, sig)
}

// OriginSignedBlobFor returns the stored signed blob for an originator at height h.
func (s *HeightSyncPeerTips) OriginSignedBlobFor(originator string, h int64) (blob, sig []byte, ok bool) {
	sec, blob, sig, ok := s.VerifiedAnchorFor(originator, h)
	_ = sec
	return blob, sig, ok
}

// VerifiedAnchorFor returns the cached section and its signed blob (Step 8).
func (s *HeightSyncPeerTips) VerifiedAnchorFor(originator string, h int64) (*heightsync.HeightSyncSection, []byte, []byte, bool) {
	if s == nil || originator == "" || h <= 0 {
		return nil, nil, nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ent, ok := s.tipsByOriginator[originator]
	if !ok || ent.sec == nil || ent.sec.MainnetHeight != h || len(ent.blob) == 0 || len(ent.sig) == 0 {
		return nil, nil, nil, false
	}
	return cloneHeightSyncSection(ent.sec),
		append([]byte(nil), ent.blob...),
		append([]byte(nil), ent.sig...),
		true
}

// Update records an observed peer tip (alias for RecordOrigin).
func (s *HeightSyncPeerTips) Update(hs *heightsync.HeightSyncSection) {
	s.RecordOrigin(hs)
}

// MaxFresh returns the highest-height cached section whose originator observation
// is within freshness. Nil when the cache is empty or every entry is stale/ineligible.
func (s *HeightSyncPeerTips) MaxFresh(now time.Time, freshness time.Duration) *heightsync.HeightSyncSection {
	if s == nil {
		return nil
	}
	if freshness <= 0 {
		freshness = s.freshness()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var best *heightsync.HeightSyncSection
	for _, ent := range s.tipsByOriginator {
		if !s.entryEligibleLocked(ent) || !s.isFreshLocked(ent.sec, now, freshness) {
			continue
		}
		sec := ent.sec
		if best == nil || sec.MainnetHeight > best.MainnetHeight {
			best = sec
		}
	}
	return cloneHeightSyncSection(best)
}

// ShouldPropagateTo reports whether height h has not yet been propagated to recipient.
func (s *HeightSyncPeerTips) ShouldPropagateTo(recipient string, h uint64) bool {
	if s == nil || recipient == "" || h == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return h > s.lastPropagated[recipient]
}

// MarkPropagated records that recipient has been sent tip height h (monotonic).
func (s *HeightSyncPeerTips) MarkPropagated(recipient string, h uint64) {
	if s == nil || recipient == "" || h == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.lastPropagated[recipient]; ok && h <= prev {
		return
	}
	s.lastPropagated[recipient] = h
}

// Carry merges the freshest cached peer tip into sec without overwriting the carrier's
// timestamp; originator fields are taken from the cached origin section.
func (s *HeightSyncPeerTips) Carry(sec *heightsync.HeightSyncSection) {
	if s == nil || sec == nil {
		return
	}
	tip := s.MaxFresh(time.Now(), s.freshness())
	if tip == nil {
		return
	}
	mergePeerTipOriginPreserving(sec, tip)
}

func mergePeerTipOriginPreserving(dst, tip *heightsync.HeightSyncSection) {
	if dst == nil || tip == nil {
		return
	}
	if tip.MainnetHeight > dst.MainnetHeight {
		dst.ChainID = tip.ChainID
		dst.MainnetHeight = tip.MainnetHeight
		dst.MainnetBlockHashHex = tip.MainnetBlockHashHex
	}
	if tip.MainnetHeight >= dst.MainnetHeight && strings.TrimSpace(tip.OriginatorSenderID) != "" {
		dst.OriginatorSenderID = tip.OriginatorSenderID
		dst.OriginatorTimestampMs = tip.OriginatorTimestampMs
	}
	// Request leg omits sender_signature per asymmetric verification spec.
	dst.SenderSignature = nil
}
