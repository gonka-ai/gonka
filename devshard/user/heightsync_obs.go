package user

import (
	"strings"
	"time"

	"devshard/heightsync"
	"devshard/host"
	"devshard/transport"
	"devshard/types"
)

// heightSyncDefaultMinPublish bounds how often publishHeightSyncView does a
// full SnapshotHeightSync under load. Scrapes read the cache only.
const heightSyncDefaultMinPublish = 100 * time.Millisecond

// heightSyncPublished is one installed operator-view generation. seq rises
// monotonically so a stale concurrent snapshot cannot replace a newer one.
type heightSyncPublished struct {
	seq  uint64
	view heightsync.OperatorView
}

// CachedHeightSyncView returns the last published operator view. It never
// takes s.mu — gateway Collect and GET /v1/debug/heightsync must use this.
// An empty view means the session is unwired or has not published yet.
//
// The returned value shares the published view's slices and maps. That is safe
// because a published generation is immutable: SnapshotHeightSync builds every
// collection from scratch and publishHeightSyncView installs it whole, never
// editing one in place. Readers must keep it that way and treat the result as
// read-only.
func (s *Session) CachedHeightSyncView() heightsync.OperatorView {
	if s == nil {
		return heightsync.OperatorView{}
	}
	if p := s.heightSyncView.Load(); p != nil {
		return p.view
	}
	return heightsync.OperatorView{}
}

// HeightSyncWired reports whether courier height-sync was configured on this
// session (cadence K/slots or an injected peer-tip cache).
func (s *Session) HeightSyncWired() bool {
	return s != nil && s.heightSyncWired.Load()
}

// publishHeightSyncView marks the operator view dirty and attempts a throttled
// refresh after the producer lock is dropped. No-op when height-sync was never
// wired. Safe to call frequently: installs are coalesced under a publish mutex
// with a minimum interval and a monotonic seq on the atomic pointer.
func (s *Session) publishHeightSyncView() {
	if s == nil || !s.heightSyncWired.Load() {
		return
	}
	s.heightSyncDirty.Store(true)
	s.publishHeightSyncViewOpt(false)
}

func (s *Session) publishHeightSyncViewForce() {
	if s == nil || !s.heightSyncWired.Load() {
		return
	}
	s.heightSyncDirty.Store(true)
	s.publishHeightSyncViewOpt(true)
}

// publishHeightSyncViewOpt installs the view if it is dirty and the throttle
// allows it. It does not mark dirty itself, so the trailing-edge timer can call
// it without manufacturing work for a session that has nothing new to publish.
func (s *Session) publishHeightSyncViewOpt(force bool) {
	if s == nil || !s.heightSyncWired.Load() {
		return
	}

	s.heightSyncPublishMu.Lock()
	defer s.heightSyncPublishMu.Unlock()

	if !force {
		if !s.heightSyncDirty.Load() {
			return
		}
		minNs := s.heightSyncMinPublishNs.Load()
		if minNs == 0 {
			minNs = heightSyncDefaultMinPublish.Nanoseconds()
		}
		if minNs > 0 {
			now := time.Now().UnixNano()
			if last := s.heightSyncLastPubNs.Load(); last > 0 && now-last < minNs {
				// Coalesced. Arm the trailing edge so the newest state reaches
				// the cache when the interval expires: without it the last
				// update of a burst would sit unpublished until the next
				// mutation, which for an idle session is a heartbeat away.
				s.armHeightSyncFlushLocked(time.Duration(minNs - (now - last)))
				return
			}
		}
	}

	s.heightSyncDirty.Store(false)
	view := s.SnapshotHeightSync()
	seq := s.heightSyncPubSeq.Add(1)
	next := &heightSyncPublished{seq: seq, view: view}

	// Install only if we are still the newest generation. Another publisher
	// that finished a later snapshot under the mutex cannot lose to us; this
	// guards the Store against any future lock-free path and makes seq the
	// scrape-visible generation.
	for {
		cur := s.heightSyncView.Load()
		if cur != nil && cur.seq >= seq {
			s.heightSyncDirty.Store(true)
			return
		}
		if s.heightSyncView.CompareAndSwap(cur, next) {
			s.heightSyncLastPubNs.Store(time.Now().UnixNano())
			s.heightSyncPubCount.Add(1)
			// A mutation that landed during SnapshotHeightSync has already
			// re-marked dirty, so the next call (or the trailing timer) picks
			// it up. Nothing to do here.
			return
		}
	}
}

// armHeightSyncFlushLocked schedules one trailing publish in d. Caller must
// hold heightSyncPublishMu. At most one timer is outstanding: a burst of
// coalesced calls shares the one already armed, since each publish installs
// whatever the state is at that moment, not the state that armed it.
func (s *Session) armHeightSyncFlushLocked(d time.Duration) {
	if s.heightSyncFlushClosed || s.heightSyncFlush != nil {
		return
	}
	if d <= 0 {
		d = time.Millisecond
	}
	s.heightSyncFlush = time.AfterFunc(d, func() {
		s.heightSyncPublishMu.Lock()
		s.heightSyncFlush = nil
		closed := s.heightSyncFlushClosed
		s.heightSyncPublishMu.Unlock()
		if closed {
			return
		}
		s.publishHeightSyncViewOpt(false)
	})
}

// stopHeightSyncFlush cancels any pending trailing publish and refuses further
// ones, so a closed session leaves no timer behind.
func (s *Session) stopHeightSyncFlush() {
	if s == nil {
		return
	}
	s.heightSyncPublishMu.Lock()
	s.heightSyncFlushClosed = true
	t := s.heightSyncFlush
	s.heightSyncFlush = nil
	s.heightSyncPublishMu.Unlock()
	if t != nil {
		t.Stop()
	}
}

// SnapshotHeightSync copies operator-facing height-sync state. Read-only with
// respect to AnchorTally: sealing is tip-driven on the producer path, not here.
// Prefer CachedHeightSyncView for scrape paths.
func (s *Session) SnapshotHeightSync() heightsync.OperatorView {
	if s == nil {
		return heightsync.OperatorView{}
	}
	s.mu.Lock()
	now := s.nowLocked()
	cfg := s.heartbeat.Config()
	view := heightsync.OperatorView{
		DevshardID:           s.escrowID,
		Now:                  now,
		Freshness:            s.peerTipsFreshnessLocked(),
		IdleTimeout:          cfg.IdleTimeout,
		AckDeadlineBlocks:    cfg.AckDeadlineBlocks,
		AbandonedTurns:       uint64(s.heartbeat.AbandonedTurns()),
		SecondsSinceTurnover: s.heartbeat.SinceTurnover(now).Seconds(),
		Overlap:              s.overlap,
	}
	h, _, ok := s.observedHeightLocked()
	if ok {
		view.GatewayTip = h
	}
	view.Slots = make([]heightsync.SlotIdentity, len(s.group))
	view.Contacts = make([]heightsync.SlotContact, len(s.group))
	for i, slot := range s.group {
		view.Slots[i] = heightsync.SlotIdentity{Slot: slot.SlotID, ParticipantKey: slot.ValidatorAddress}
		contact := heightsync.SlotContact{Slot: slot.SlotID}
		if i < len(s.lastContact) && !s.lastContact[i].IsZero() {
			contact.LastAt = s.lastContact[i]
			contact.SinceContact = now.Sub(s.lastContact[i])
		}
		view.Contacts[i] = contact
		if bits := s.lastPeerSeen[slot.SlotID]; len(bits) > 0 {
			cp := append([]byte(nil), bits...)
			view.PeerSeen = append(view.PeerSeen, heightsync.PeerSeenRow{
				Observer: slot.SlotID,
				Bits:     cp,
				Count:    heightsync.PeerSeenPopcount(cp, uint32(len(s.group))),
			})
		}
		if st, ok := s.lastSyncState[slot.SlotID]; ok {
			view.SyncStates = append(view.SyncStates, heightsync.SlotSyncState{Slot: slot.SlotID, State: st})
		}
	}
	tips := s.peerTipsLocked()
	anchors := s.anchors
	s.mu.Unlock()

	view.CadenceEvents, view.CadenceCounts = s.heartbeat.CadenceSnapshot()
	if view.CadenceCounts == nil {
		view.CadenceCounts = map[string]uint64{}
	}

	if tips != nil {
		origin := tips.PerOriginator(now)
		byKey := make(map[string]transport.OriginTipView, len(origin))
		for _, t := range origin {
			byKey[strings.TrimSpace(t.Originator)] = t
		}
		view.Tips = make([]heightsync.OriginTip, 0, len(view.Slots))
		for _, slot := range view.Slots {
			tip, ok := byKey[strings.TrimSpace(slot.ParticipantKey)]
			if !ok {
				continue
			}
			// A claim is fresh only on the same terms the carry path uses: it
			// must be admissible, it must carry the host's own observation
			// time, and that time must be within the freshness budget. A host
			// that omits its timestamp would otherwise read as age 0 forever
			// and hide its own staleness.
			withinBudget := view.Freshness <= 0 || tip.Age <= view.Freshness
			fresh := tip.Eligible && tip.ObservedAtKnown && withinBudget
			view.Tips = append(view.Tips, heightsync.OriginTip{
				Slot:       slot.Slot,
				Originator: slot.ParticipantKey,
				Height:     uint64(tip.Height),
				Age:        tip.Age,
				Verified:   tip.Verified,
				Fresh:      fresh && tip.Height > 0,
				AgeKnown:   tip.ObservedAtKnown,
			})
		}
	}

	if anchors != nil {
		var late, future uint64
		view.AnchorsLastSealed, view.DebugHeights, view.BlocksWithoutAnchor, late, future, view.AnchorsPerBlock, view.TurnoversPerBlock = anchors.Snapshot()
		view.AnchorsLate = late
		view.AnchorsFuture = future
	}
	return view
}

// noteGatewayTipLocked seals the anchor tally to the gateway's current tip.
// Caller must hold s.mu. Snapshot/publish must not seal.
func (s *Session) noteGatewayTipLocked() {
	if s.anchors == nil {
		return
	}
	h, _, ok := s.observedHeightLocked()
	if ok && h > 0 {
		s.anchors.ObserveTip(h)
	}
}

func (s *Session) peerTipsFreshnessLocked() time.Duration {
	if tips := s.peerTipsLocked(); tips != nil && tips.Freshness > 0 {
		return tips.Freshness
	}
	return heightsync.DefaultOriginatorFreshness
}

func (s *Session) peerTipsLocked() *transport.HeightSyncPeerTips {
	if s.peerTips != nil {
		return s.peerTips
	}
	for _, c := range s.clients {
		if hc, ok := c.(interface {
			HeightSyncPeerTips() *transport.HeightSyncPeerTips
		}); ok {
			if pt := hc.HeightSyncPeerTips(); pt != nil {
				s.peerTips = pt
				return pt
			}
		}
	}
	return nil
}

func (s *Session) noteContactLocked(hostIdx int, at time.Time) {
	if hostIdx < 0 || hostIdx >= len(s.lastContact) {
		return
	}
	s.lastContact[hostIdx] = at
}

func (s *Session) noteAckObsLocked(ack *types.MsgHeightAck) {
	if ack == nil {
		return
	}
	// SlotId is host-chosen. Refuse anything outside the escrow roster so
	// maps and Prometheus labels stay O(slots_num), not O(uint32).
	if !s.knownSlotLocked(ack.GetSlotId()) {
		return
	}
	slotsNum := uint32(len(s.group))
	// Same PeerSeen framing as log-plane L1 (proposal §14). Oversized /
	// undersized bitmaps are dropped; sync_state and anchors still record for
	// a valid slot.
	if len(ack.GetPeerSeen()) > 0 && heightsync.PeerSeenByteLenValid(ack.GetPeerSeen(), slotsNum) {
		s.lastPeerSeen[ack.GetSlotId()] = append([]byte(nil), ack.GetPeerSeen()...)
	}
	s.lastSyncState[ack.GetSlotId()] = ack.GetSyncState().String()
	if s.anchors != nil && ack.GetObservedHeight() > 0 {
		s.anchors.Record(ack.GetObservedHeight(), heightsync.AnchorKindResponse)
	}
}

func (s *Session) knownSlotLocked(slot uint32) bool {
	for _, g := range s.group {
		if g.SlotID == slot {
			return true
		}
	}
	return false
}

func (s *Session) noteOverlapLocked(resp *host.HostResponse) {
	if resp == nil {
		return
	}
	s.overlap.Total++
	if resp.HasEnvelope {
		s.overlap.WithSection++
	}
	hasStamp := heightsync.StampPresent(resp.ObservedBlockHash)
	if hasStamp {
		s.overlap.WithStamp++
	}
	if resp.HasEnvelope && hasStamp && resp.EnvelopeHeight == resp.ObservedHeight {
		s.overlap.Agreed++
	}
}

// SetHeightSyncPeerTips injects the shared courier cache (tests / HTTP session).
// A non-nil cache marks the session as height-sync wired for operator scrapes.
func (s *Session) SetHeightSyncPeerTips(tips *transport.HeightSyncPeerTips) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.peerTips = tips
	s.mu.Unlock()
	if tips != nil {
		s.heightSyncWired.Store(true)
		s.publishHeightSyncViewForce()
	}
}

// NoteHeightSyncContact records sequencer contact with a slot (tests).
func (s *Session) NoteHeightSyncContact(hostIdx int, at time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.noteContactLocked(hostIdx, at)
	s.mu.Unlock()
	s.publishHeightSyncView()
}

// NoteExchangeOverlap records whether one exchange carried a section, a stamp, and agreement.
func (s *Session) NoteExchangeOverlap(hasSection, hasStamp, agreed bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.overlap.Total++
	if hasSection {
		s.overlap.WithSection++
	}
	if hasStamp {
		s.overlap.WithStamp++
	}
	if agreed {
		s.overlap.Agreed++
	}
	s.mu.Unlock()
	s.publishHeightSyncView()
}

// TestingOnlyHoldMu holds s.mu for scrape-isolation tests. Caller must invoke
// the returned release function.
func (s *Session) TestingOnlyHoldMu() (release func()) {
	s.mu.Lock()
	return s.mu.Unlock
}

// TestingOnlyHeightSyncPublishCount is the number of successful cache installs.
func (s *Session) TestingOnlyHeightSyncPublishCount() uint64 {
	if s == nil {
		return 0
	}
	return s.heightSyncPubCount.Load()
}

// TestingOnlySetHeightSyncMinPublish overrides the publish coalesce interval.
// Pass 0 to restore the default; a negative duration disables throttling.
func (s *Session) TestingOnlySetHeightSyncMinPublish(d time.Duration) {
	if s == nil {
		return
	}
	if d < 0 {
		s.heightSyncMinPublishNs.Store(-1)
		return
	}
	if d == 0 {
		s.heightSyncMinPublishNs.Store(0)
		return
	}
	s.heightSyncMinPublishNs.Store(d.Nanoseconds())
}

// TestingOnlyFlushHeightSyncView forces a cache install (tests).
func (s *Session) TestingOnlyFlushHeightSyncView() {
	if s == nil {
		return
	}
	s.publishHeightSyncViewForce()
}

// HeartbeatCadence returns a copy of the last-N cadence ring (tests).
func (s *Session) HeartbeatCadence() []heightsync.CadenceEvent {
	if s == nil {
		return nil
	}
	events, _ := s.heartbeat.CadenceSnapshot()
	return events
}
