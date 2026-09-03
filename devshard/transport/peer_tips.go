package transport

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"devshard/heightsync"
	"devshard/logging"
)

// defaultPeerTipFreshness matches MOCKDAPI_STALE_AFTER / proposal courier F for PoC.
const defaultPeerTipFreshness = 60 * time.Second

var _ heightsync.LazyPropagator = (*HeightSyncPeerTips)(nil)

type originTipEntry struct {
	sec  *heightsync.HeightSyncSection
	blob []byte
	sig  []byte
	// storedAt is the local receipt clock. It never feeds freshness — the
	// protocol only trusts the originator's own observation time — but it lets
	// an operator view age a claim that arrived without one.
	storedAt time.Time
}

// HeightSyncPeerTips stores verbatim originator Anchor sections observed from hosts
// and tracks per-recipient propagation for lazy carry-forward (spec §16).
type HeightSyncPeerTips struct {
	mu sync.Mutex

	tipsByOriginator map[string]*originTipEntry
	maxTip           *heightsync.HeightSyncSection
	lastPropagated   map[string]uint64

	// Freshness bounds MaxFresh and Carry; zero uses defaultPeerTipFreshness.
	Freshness time.Duration
	// RequireVerifiedBlob when true: MaxFresh/Carry only use entries stored via
	// RecordOriginWithBlob (spec §15 courier path). NewHeightSyncPeerTips defaults
	// this to true; tests that need unverified RecordOrigin entries opt out.
	RequireVerifiedBlob bool
}

// NewHeightSyncPeerTips creates an empty session-scoped peer-tip cache.
func NewHeightSyncPeerTips() *HeightSyncPeerTips {
	return &HeightSyncPeerTips{
		tipsByOriginator:    make(map[string]*originTipEntry),
		lastPropagated:      make(map[string]uint64),
		RequireVerifiedBlob: true,
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
		// Missing originator time is arbitrarily old (proposal §14 step 5),
		// matching inbound freshnessOK. A zero-ts cache entry must not drive Carry.
		return false
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
	ent := &originTipEntry{
		sec:      cp,
		blob:     append([]byte(nil), blob...),
		sig:      append([]byte(nil), sig...),
		storedAt: time.Now(),
	}
	s.tipsByOriginator[key] = ent
	s.recomputeMaxTipLocked()
}

// PeerTipCacheState is a point-in-time view of the courier peer-tip cache (debug / tests).
type PeerTipCacheState struct {
	CacheReady            bool
	VerifiedOriginators   int
	UnverifiedOriginators int
	MaxFreshHeight        int64
	BlockHashPrefix       string
	RequireVerifiedBlob   bool
}

// snapshotLocked builds cache state; caller must hold s.mu.
func (s *HeightSyncPeerTips) snapshotLocked(now time.Time) PeerTipCacheState {
	if s == nil {
		return PeerTipCacheState{}
	}
	if now.IsZero() {
		now = time.Now()
	}
	freshness := s.freshness()
	st := PeerTipCacheState{RequireVerifiedBlob: s.RequireVerifiedBlob}
	var maxFresh *heightsync.HeightSyncSection
	for _, ent := range s.tipsByOriginator {
		if !s.entryEligibleLocked(ent) {
			if ent != nil && ent.sec != nil && isValidPeerTip(ent.sec) {
				st.UnverifiedOriginators++
			}
			continue
		}
		if len(ent.blob) > 0 && len(ent.sig) > 0 {
			st.VerifiedOriginators++
		} else {
			st.UnverifiedOriginators++
		}
		if !s.isFreshLocked(ent.sec, now, freshness) {
			continue
		}
		sec := ent.sec
		if maxFresh == nil || sec.MainnetHeight > maxFresh.MainnetHeight {
			maxFresh = sec
		}
	}
	if maxFresh != nil {
		st.CacheReady = true
		st.MaxFreshHeight = maxFresh.MainnetHeight
		st.BlockHashPrefix = heightSyncHashPrefixForLog(maxFresh.MainnetBlockHashHex)
	}
	return st
}

// DecidePeerTipSnapshot implements heightsync.PeerTipCacheDebug for decide logging.
func (s *HeightSyncPeerTips) DecidePeerTipSnapshot(now time.Time) (cacheReady bool, verifiedOrigins int, maxFreshHeight int64) {
	st := s.Snapshot(now)
	return st.CacheReady, st.VerifiedOriginators, st.MaxFreshHeight
}

// Snapshot reports whether MaxFresh would return a tip and how many origins are cached.
func (s *HeightSyncPeerTips) Snapshot(now time.Time) PeerTipCacheState {
	if s == nil {
		return PeerTipCacheState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked(now)
}

func heightSyncHashPrefixForLog(hexStr string) string {
	h := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(hexStr), "0x"), "0X")
	if len(h) >= 8 {
		return strings.ToLower(h[:8])
	}
	return strings.ToLower(h)
}

func (s *HeightSyncPeerTips) logCache(event string, nonce uint64, extra ...any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	st := s.snapshotLocked(time.Now())
	s.mu.Unlock()
	kvs := []any{
		heightsync.LogFieldSubsystem, "heightsync",
		heightsync.LogFieldEvent, event,
		heightsync.LogFieldCacheReady, st.CacheReady,
		heightsync.LogFieldVerifiedOrigins, st.VerifiedOriginators,
		"unverified_origins", st.UnverifiedOriginators,
		heightsync.LogFieldHeight, st.MaxFreshHeight,
		heightsync.LogFieldBlockHashPrefix, st.BlockHashPrefix,
		"require_verified_blob", st.RequireVerifiedBlob,
	}
	if nonce > 0 {
		kvs = append(kvs, heightsync.LogFieldNonce, nonce)
	}
	kvs = append(kvs, extra...)
	logging.Debug("heightsync: peer_tip_cache", kvs...)
}

// LogCacheState emits a debug snapshot (for tests and operational visibility).
func (s *HeightSyncPeerTips) LogCacheState(event string, nonce uint64) {
	s.logCache(event, nonce)
}

// RecordOrigin stores a verbatim Anchor section keyed by originator identity.
// Unverified entries are ignored by MaxFresh when RequireVerifiedBlob is set.
func (s *HeightSyncPeerTips) RecordOrigin(sec *heightsync.HeightSyncSection) {
	if s == nil {
		return
	}
	if !isValidPeerTip(sec) {
		originator := ""
		if sec != nil {
			originator = strings.TrimSpace(sec.OriginatorSenderID)
		}
		s.logCache("record_origin_skip_invalid", 0, "originator", originator)
		return
	}
	originator := originatorCacheKey(sec)
	height := sec.MainnetHeight
	s.mu.Lock()
	s.storeOriginLocked(sec, nil, nil)
	requireVerified := s.RequireVerifiedBlob
	s.mu.Unlock()
	if requireVerified {
		s.logCache("record_origin_unverified", 0,
			"originator", originator,
			heightsync.LogFieldHeight, height)
	}
}

// RecordOriginWithBlob stores a verified response-leg Anchor and its signed blob (spec §15).
func (s *HeightSyncPeerTips) RecordOriginWithBlob(sec *heightsync.HeightSyncSection, blob, sig []byte) {
	if s == nil {
		return
	}
	if !isValidPeerTip(sec) || len(blob) == 0 || len(sig) == 0 {
		s.logCache("record_verified_skip", 0,
			"originator", originatorCacheKey(sec))
		return
	}
	cp := cloneHeightSyncSection(sec)
	cp.SenderSignature = append([]byte(nil), sig...)
	originator := originatorCacheKey(cp)
	height := cp.MainnetHeight
	s.mu.Lock()
	s.storeOriginLocked(cp, blob, sig)
	s.mu.Unlock()
	s.logCache("record_verified", 0,
		"originator", originator,
		heightsync.LogFieldHeight, height)
}

// OriginSignedBlobFor returns the stored signed blob for an originator at height h.
func (s *HeightSyncPeerTips) OriginSignedBlobFor(originator string, h int64) (blob, sig []byte, ok bool) {
	sec, blob, sig, ok := s.VerifiedAnchorFor(originator, h)
	_ = sec
	return blob, sig, ok
}

// VerifiedAnchorFor returns the cached section and its signed blob (spec §15).
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

// OriginTipView is one cached originator's latest height claim for gateway
// operator views (proposal §8.12).
type OriginTipView struct {
	Originator string
	Height     int64
	Age        time.Duration
	Verified   bool
	ObservedAt time.Time
	// ObservedAtKnown is false when the claim carries no originator
	// observation time. Such a claim is arbitrarily old to the protocol, so a
	// consumer must not read Age (then measured from local receipt) as
	// evidence of freshness.
	ObservedAtKnown bool
	// Eligible mirrors the admission rule the carry path applies: an
	// ineligible tip is cached but never used, so it must not be reported as
	// a live height either.
	Eligible bool
}

// PerOriginator returns a copy of each originator's latest tip. Age is relative
// to now. Slot identity is applied by the caller from the escrow roster.
func (s *HeightSyncPeerTips) PerOriginator(now time.Time) []OriginTipView {
	if s == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]OriginTipView, 0, len(s.tipsByOriginator))
	for key, ent := range s.tipsByOriginator {
		if ent == nil || ent.sec == nil {
			continue
		}
		ts := originatorObservedAtMs(ent.sec)
		observed := ent.storedAt
		known := ts > 0
		if known {
			observed = time.UnixMilli(ts)
		}
		age := time.Duration(0)
		if !observed.IsZero() && now.After(observed) {
			age = now.Sub(observed)
		}
		out = append(out, OriginTipView{
			Originator:      key,
			Height:          ent.sec.MainnetHeight,
			Age:             age,
			Verified:        len(ent.blob) > 0 && len(ent.sig) > 0,
			ObservedAt:      observed,
			ObservedAtKnown: known,
			Eligible:        s.entryEligibleLocked(ent),
		})
	}
	return out
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
		s.logCache("carry_miss", 0)
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
