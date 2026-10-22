package session

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"devshard/bridge"
	devshardbridge "devshard/cmd/devshardd/bridge"
	"devshard/storage"
)

const (
	defaultUnknownEscrowLookupTTL     = time.Minute
	defaultUnknownEscrowPerPeerPerMin = 2
	defaultUnknownEscrowFloorPerMin   = 300
	maxEscrowLookupCache              = 4096
	maxRosterPeerDecisions            = 256
	maxKnownCreators                  = 4096
)

var (
	unknownEscrowLookupTTL     = defaultUnknownEscrowLookupTTL
	unknownEscrowPerPeerPerMin = defaultUnknownEscrowPerPeerPerMin
	unknownEscrowFloorPerMin   = defaultUnknownEscrowFloorPerMin
)

type escrowLookupEntry struct {
	info      *bridge.EscrowInfo
	err       error
	expiresAt time.Time
}

// rosterEscrow is one retained chain escrow plus the warm-key decisions
// already made for it. A slot address is not stored: that check is the
// slot list. peers holds a definitive allow or deny from VerifyWarmKey.
type rosterEscrow struct {
	info  *bridge.EscrowInfo
	peers map[string]bool
}

// errPayloadPeerDenied is the unbound GetPayload gate: the handshake peer is
// not a slot and not a warm key for one. It is not a chain-lookup failure.
var errPayloadPeerDenied = errors.New("peer is not a known participant")

// errPayloadEpochClosed is a retained escrow whose epoch is older than
// current-1. The roster keeps it so the chain is not queried again. Attach
// and GetPayload both refuse it.
var errPayloadEpochClosed = errors.New("escrow epoch is not open for payload")

// fetchEscrowForBind is the chain lookup used when owner chat finds no local
// session, and when Attach / BindGroupPeer / unbound GetPayload has no roster
// hit and no fresh escrow_cache row. Unknown ids stay in the one-minute
// escrowLookups cache. Real escrows inside retention also go into rosterEscrows
// via loadRosterEscrow, which calls this on a miss.
//
// Eligibility (owner / group slot) is on the escrow record, so the query has
// to run before we know whether the peer belongs. Unique unknown or
// ineligible ids are charged against a per-peer (2/min) and process-wide
// budget before that query. A successful load that shows the peer is the
// creator or a slot member is refunded so first bind of a real escrow does
// not consume the unknown-id budget. GetPayload uses the same charge and
// refunds a warm-key peer that the slot check missed. Per origin IP is not
// keyed here: mixed fleets and hop-stamped X-Real-IP would collapse every
// client onto one 2/min slot. versiond applies that cap on inbound X-Real-IP
// after it sees a bind miss (X-Devshard-Error escrow_not_found; a full floor
// is not the caller's miss). Attach of a warmed id uses warmedEscrow instead
// (no query, no charge). Owner chat of a warmed id uses fetchOwnerEscrow.
// RecoverSessions and create() with a prefetched escrow do not use this.
//
// A known creator (a signer that already owns a session here or proved
// creatorship on an earlier lookup) is charged per signer only, so strangers
// filling the process floor cannot refuse its first chat.
func (m *HostManager) fetchEscrowForBind(escrowID, peer string) (*bridge.EscrowInfo, error) {
	info, _, err := m.fetchEscrowTracked(escrowID, peer)
	return info, err
}

// fetchOwnerEscrow is owner chat on an id whose fresh escrow_cache row names
// owner as creator. The row already proves the escrow exists, so the lookup
// is not an unknown-id probe and takes no charge. GetEscrow still runs so
// settlement and slot changes are read live; the bridge falls back to the
// row when the chain is down. A cached not-found from before the row landed
// is skipped.
func (m *HostManager) fetchOwnerEscrow(escrowID, owner string) (*bridge.EscrowInfo, error) {
	if m.bridge == nil {
		return nil, fmt.Errorf("get escrow: bridge is nil")
	}
	now := time.Now()
	if info, err, ok := m.cachedEscrowLookup(escrowID, now); ok && info != nil {
		return info, err
	}
	info, err := m.bridge.GetEscrow(escrowID)
	m.rememberEscrowLookup(escrowID, info, err, now)
	if escrowLookupEligible(info, err, owner) && info.CreatorAddress == owner {
		m.noteKnownCreator(owner, now)
	}
	return info, err
}

// fetchEscrowTracked is fetchEscrowForBind plus the timestamp still charged
// to peer. Zero means this call did not consume budget (cache hit, or the
// creator/slot refund already ran).
func (m *HostManager) fetchEscrowTracked(escrowID, peer string) (*bridge.EscrowInfo, time.Time, error) {
	if m.bridge == nil {
		return nil, time.Time{}, fmt.Errorf("get escrow: bridge is nil")
	}
	now := time.Now()
	if info, err, ok := m.cachedEscrowLookup(escrowID, now); ok {
		return info, time.Time{}, err
	}
	chargedAt, err := m.chargeEscrowLookup(peer, now)
	if err != nil {
		return nil, time.Time{}, err
	}
	info, err := m.bridge.GetEscrow(escrowID)
	switch {
	case escrowLookupEligible(info, err, peer):
		m.refundEscrowLookup(peer, chargedAt)
		chargedAt = time.Time{}
		if info.CreatorAddress == peer {
			m.noteKnownCreator(peer, now)
		}
	case errors.Is(err, bridge.ErrChainUnavailable) && m.isKnownCreator(peer):
		m.refundEscrowLookup(peer, chargedAt)
		chargedAt = time.Time{}
	}
	m.rememberEscrowLookup(escrowID, info, err, now)
	return info, chargedAt, err
}

// loadRosterEscrow returns a chain escrow from the retention roster, the
// durable escrow cache, or one rate-limited GetEscrow. Retained escrows
// (current epoch and the two before it) are stored in rosterEscrows so later
// calls do not reload them. Unknown ids stay on the one-minute cache inside
// fetchEscrowTracked. A pruned epoch is not stored and returns ErrEpochPruned.
// Settled escrows are returned for the caller to reject and are not stored.
// chargedAt is non-zero only when this call still holds an unknown-id charge.
func (m *HostManager) loadRosterEscrow(escrowID, peer string) (*bridge.EscrowInfo, time.Time, error) {
	if info, ok := m.cachedRoster(escrowID); ok {
		return info, time.Time{}, nil
	}
	if warmed := m.warmedEscrow(escrowID); warmed != nil {
		if warmed.Settled {
			return cloneEscrowInfo(warmed), time.Time{}, nil
		}
		if !m.rosterRetained(warmed.EpochID) {
			return cloneEscrowInfo(warmed), time.Time{}, storage.ErrEpochPruned
		}
		m.rememberRoster(escrowID, warmed)
		return cloneEscrowInfo(warmed), time.Time{}, nil
	}
	info, chargedAt, err := m.fetchEscrowTracked(escrowID, peer)
	if err != nil {
		return info, chargedAt, err
	}
	if info == nil {
		return nil, chargedAt, bridge.ErrEscrowNotFound
	}
	if info.Settled {
		return cloneEscrowInfo(info), chargedAt, nil
	}
	if !m.rosterRetained(info.EpochID) {
		return cloneEscrowInfo(info), chargedAt, storage.ErrEpochPruned
	}
	m.rememberRoster(escrowID, info)
	return cloneEscrowInfo(info), chargedAt, nil
}

// payloadRoster is the GetPayload gate when SessionServerExisting misses.
// It does not CreateSession. The handshake peer must be a slot or a warm
// key for one, and the escrow epoch must be current or current-1. A warm-key
// hit refunds the Attach unknown-id charge.
func (m *HostManager) payloadRoster(escrowID, peer string) (*bridge.EscrowInfo, error) {
	info, chargedAt, err := m.loadRosterEscrow(escrowID, peer)
	if err != nil {
		return nil, err
	}
	if info != nil && info.Settled {
		return nil, fmt.Errorf("%w: escrow %s", bridge.ErrEscrowSettled, escrowID)
	}
	// The validator Attaches on this same escrow, so GetPayload only runs
	// for an epoch the door admits: current and current-1. current-2 stays
	// cached and is not served. Reject it before any warm-key query.
	if !m.payloadEpochOpen(info.EpochID) {
		return nil, errPayloadEpochClosed
	}
	if !m.payloadPeerAllowed(escrowID, info, peer) {
		return nil, errPayloadPeerDenied
	}
	if !chargedAt.IsZero() && !escrowLookupEligible(info, nil, peer) {
		m.refundEscrowLookup(peer, chargedAt)
	}
	return info, nil
}

// payloadPeerAllowed reports whether peer may call unbound GetPayload.
// A slot address returns before any chain call. Any other peer uses the
// decision stored on the roster entry. The first miss scans warm keys once;
// concurrent callers for the same escrow and peer share that scan. A deny is
// stored. A scan that saw a query error and no matching grant is not stored.
func (m *HostManager) payloadPeerAllowed(escrowID string, info *bridge.EscrowInfo, peer string) bool {
	if info == nil || peer == "" {
		return false
	}
	for _, slot := range info.Slots {
		if slot == peer {
			return true
		}
	}
	if allowed, ok := m.cachedRosterPeer(escrowID, peer); ok {
		return allowed
	}
	if m.bridge == nil {
		return false
	}
	v, _, _ := m.rosterPeerSF.Do(escrowID+"\x00"+peer, func() (interface{}, error) {
		if allowed, ok := m.cachedRosterPeer(escrowID, peer); ok {
			return allowed, nil
		}
		allowed, definitive := m.scanRosterWarmKey(info, peer)
		if definitive {
			m.rememberRosterPeer(escrowID, peer, allowed)
		}
		return allowed, nil
	})
	allowed, _ := v.(bool)
	return allowed
}

// scanRosterWarmKey walks slots in order. allowed is true when one grant
// matches. definitive is false when a query failed and no grant matched, so
// the caller must not cache the deny.
func (m *HostManager) scanRosterWarmKey(info *bridge.EscrowInfo, peer string) (allowed, definitive bool) {
	if m == nil || m.bridge == nil || info == nil {
		return false, false
	}
	sawSlot := false
	uncertain := false
	for _, slot := range info.Slots {
		if slot == "" || slot == peer {
			continue
		}
		sawSlot = true
		ok, err := m.bridge.VerifyWarmKey(peer, slot)
		if err != nil {
			uncertain = true
			continue
		}
		if ok {
			return true, true
		}
	}
	if !sawSlot {
		return false, true
	}
	if uncertain {
		return false, false
	}
	return false, true
}

func (m *HostManager) rosterCurrentEpoch() uint64 {
	if m == nil {
		return 0
	}
	return currentEpochIDFromStore(m.store)
}

// rosterRetained reports whether epoch is the current epoch or one of the
// two before it. A zero clock (tests, store without an epoch) keeps the
// escrow: there is no horizon to close.
func (m *HostManager) rosterRetained(epoch uint64) bool {
	current := m.rosterCurrentEpoch()
	if current == 0 {
		return true
	}
	cutoff := m.pruneCutoff()
	if cutoff == 0 {
		cutoff = storage.RetentionCutoff(current, storage.DefaultEpochRetain)
	}
	return epoch >= cutoff
}

// attachDoorOpen is the Attach admission on top of roster eligibility.
// The current epoch and the one before it open the door. A validator
// Attaches on the same escrow it uses for GetPayload, so current-1 has to
// admit the handshake or that payload never runs. current-2 stays cached
// and does not admit. A zero clock leaves the door open.
func (m *HostManager) attachDoorOpen(epoch uint64) bool {
	return m.payloadEpochOpen(epoch)
}

// payloadEpochOpen is the current epoch and current-1. Unbound GetPayload
// serves those epochs only. A zero clock leaves the gate open.
func (m *HostManager) payloadEpochOpen(epoch uint64) bool {
	current := m.rosterCurrentEpoch()
	if current == 0 {
		return true
	}
	if epoch == current {
		return true
	}
	return epoch+1 == current
}

func (m *HostManager) cachedRoster(escrowID string) (*bridge.EscrowInfo, bool) {
	m.escrowLookupMu.Lock()
	entry := m.rosterEscrows[escrowID]
	if entry == nil || entry.info == nil {
		m.escrowLookupMu.Unlock()
		return nil, false
	}
	cloned := cloneEscrowInfo(entry.info)
	m.escrowLookupMu.Unlock()
	if m.rosterRetained(cloned.EpochID) {
		return cloned, true
	}
	m.escrowLookupMu.Lock()
	if cur := m.rosterEscrows[escrowID]; cur != nil && cur.info != nil && cur.info.EpochID == cloned.EpochID {
		delete(m.rosterEscrows, escrowID)
	}
	m.escrowLookupMu.Unlock()
	return nil, false
}

func (m *HostManager) rememberRoster(escrowID string, info *bridge.EscrowInfo) {
	if info == nil || info.Settled || !m.rosterRetained(info.EpochID) {
		return
	}
	m.escrowLookupMu.Lock()
	defer m.escrowLookupMu.Unlock()
	if m.rosterEscrows == nil {
		m.rosterEscrows = make(map[string]*rosterEscrow)
	}
	if existing := m.rosterEscrows[escrowID]; existing != nil {
		existing.info = cloneEscrowInfo(info)
		return
	}
	if len(m.rosterEscrows) >= maxEscrowLookupCache {
		m.evictOneRosterLocked(info.EpochID)
	}
	m.rosterEscrows[escrowID] = &rosterEscrow{info: cloneEscrowInfo(info)}
}

func (m *HostManager) cachedRosterPeer(escrowID, peer string) (bool, bool) {
	m.escrowLookupMu.Lock()
	defer m.escrowLookupMu.Unlock()
	entry := m.rosterEscrows[escrowID]
	if entry == nil || entry.peers == nil {
		return false, false
	}
	allowed, ok := entry.peers[peer]
	return allowed, ok
}

func (m *HostManager) rememberRosterPeer(escrowID, peer string, allowed bool) {
	if peer == "" {
		return
	}
	m.escrowLookupMu.Lock()
	defer m.escrowLookupMu.Unlock()
	entry := m.rosterEscrows[escrowID]
	if entry == nil {
		return
	}
	if entry.peers == nil {
		entry.peers = make(map[string]bool)
	}
	if _, exists := entry.peers[peer]; !exists && len(entry.peers) >= maxRosterPeerDecisions {
		for id := range entry.peers {
			delete(entry.peers, id)
			break
		}
	}
	entry.peers[peer] = allowed
}

func (m *HostManager) evictOneRosterLocked(keepEpoch uint64) {
	for id, entry := range m.rosterEscrows {
		if entry == nil || entry.info == nil || entry.info.EpochID != keepEpoch {
			delete(m.rosterEscrows, id)
			return
		}
	}
	for id := range m.rosterEscrows {
		delete(m.rosterEscrows, id)
		return
	}
}

func (m *HostManager) dropRosterBefore(cutoff uint64) {
	if cutoff == 0 {
		return
	}
	m.escrowLookupMu.Lock()
	defer m.escrowLookupMu.Unlock()
	for id, entry := range m.rosterEscrows {
		if entry == nil || entry.info == nil || entry.info.EpochID < cutoff {
			delete(m.rosterEscrows, id)
		}
	}
}

// warmedEscrow returns a fresh escrow_cache row without a chain query. First
// Attach of a warmed escrow (this host in Slots, or any host that saw create)
// must not look like a cold miss. Unknown ids have no row and still go
// through fetchEscrowForBind. Owner chat uses the row only to pick the
// uncharged lane; it still GetEscrow live (cache only if chain is down).
func (m *HostManager) warmedEscrow(escrowID string) *bridge.EscrowInfo {
	if m.store == nil {
		return nil
	}
	cached, err := m.store.GetEscrowCache(escrowID)
	if err != nil || !storage.EscrowCacheFresh(cached, time.Now()) {
		return nil
	}
	return devshardbridge.EscrowInfoFromCache(cached)
}

func (m *HostManager) cachedSlotURL(escrowID, addr string) string {
	if m.store == nil || addr == "" {
		return ""
	}
	cached, err := m.store.GetEscrowCache(escrowID)
	if err != nil || cached == nil {
		return ""
	}
	return strings.TrimSpace(cached.SlotURLs[addr])
}

func escrowLookupEligible(info *bridge.EscrowInfo, err error, peer string) bool {
	if info == nil || peer == "" {
		return false
	}
	if err != nil && !errors.Is(err, bridge.ErrEscrowSettled) {
		return false
	}
	if info.CreatorAddress != "" && peer == info.CreatorAddress {
		return true
	}
	for _, slot := range info.Slots {
		if slot == peer {
			return true
		}
	}
	return false
}

func (m *HostManager) cachedEscrowLookup(escrowID string, now time.Time) (*bridge.EscrowInfo, error, bool) {
	m.escrowLookupMu.Lock()
	defer m.escrowLookupMu.Unlock()
	e, ok := m.escrowLookups[escrowID]
	if !ok || !now.Before(e.expiresAt) {
		if ok {
			delete(m.escrowLookups, escrowID)
		}
		return nil, nil, false
	}
	return cloneEscrowInfo(e.info), e.err, true
}

func (m *HostManager) rememberEscrowLookup(escrowID string, info *bridge.EscrowInfo, err error, now time.Time) {
	if err != nil && !errors.Is(err, bridge.ErrEscrowNotFound) && !errors.Is(err, bridge.ErrEscrowSettled) {
		return
	}
	m.escrowLookupMu.Lock()
	defer m.escrowLookupMu.Unlock()
	if m.escrowLookups == nil {
		m.escrowLookups = make(map[string]escrowLookupEntry)
	}
	for id, e := range m.escrowLookups {
		if !now.Before(e.expiresAt) {
			delete(m.escrowLookups, id)
		}
	}
	if len(m.escrowLookups) >= maxEscrowLookupCache {
		for id := range m.escrowLookups {
			delete(m.escrowLookups, id)
			if len(m.escrowLookups) < maxEscrowLookupCache {
				break
			}
		}
	}
	m.escrowLookups[escrowID] = escrowLookupEntry{
		info:      cloneEscrowInfo(info),
		err:       err,
		expiresAt: now.Add(unknownEscrowLookupTTL),
	}
}

func (m *HostManager) chargeEscrowLookup(peer string, now time.Time) (time.Time, error) {
	m.escrowLookupMu.Lock()
	defer m.escrowLookupMu.Unlock()
	_, known := m.knownCreators[peer]
	known = known && peer != ""
	if !known && countRecent(m.escrowLookupFloor, now) >= unknownEscrowFloorPerMin {
		return time.Time{}, bridge.ErrEscrowLookupLimited
	}
	if peer != "" && countRecent(m.escrowLookupPeer[peer], now) >= unknownEscrowPerPeerPerMin {
		return time.Time{}, bridge.ErrEscrowLookupLimited
	}
	if !known {
		m.escrowLookupFloor = appendRecent(m.escrowLookupFloor, now)
	}
	m.escrowLookupPeer = recordLookupTimes(m.escrowLookupPeer, peer, now)
	return now, nil
}

// noteKnownCreator records addr as the creator of a real escrow on this host.
// The set is bounded; the entry seen longest ago is dropped first.
func (m *HostManager) noteKnownCreator(addr string, now time.Time) {
	if addr == "" {
		return
	}
	m.escrowLookupMu.Lock()
	defer m.escrowLookupMu.Unlock()
	if m.knownCreators == nil {
		m.knownCreators = make(map[string]time.Time)
	}
	if _, ok := m.knownCreators[addr]; !ok && len(m.knownCreators) >= maxKnownCreators {
		oldest, oldestAt := "", now
		for a, at := range m.knownCreators {
			if oldest == "" || at.Before(oldestAt) {
				oldest, oldestAt = a, at
			}
		}
		delete(m.knownCreators, oldest)
	}
	m.knownCreators[addr] = now
}

func (m *HostManager) isKnownCreator(addr string) bool {
	if addr == "" {
		return false
	}
	m.escrowLookupMu.Lock()
	defer m.escrowLookupMu.Unlock()
	_, ok := m.knownCreators[addr]
	return ok
}

func (m *HostManager) refundEscrowLookup(peer string, at time.Time) {
	if at.IsZero() {
		return
	}
	m.escrowLookupMu.Lock()
	defer m.escrowLookupMu.Unlock()
	m.escrowLookupFloor = removeTime(m.escrowLookupFloor, at)
	if peer != "" {
		m.escrowLookupPeer[peer] = removeTime(m.escrowLookupPeer[peer], at)
	}
}

func recordLookupTimes(times map[string][]time.Time, key string, now time.Time) map[string][]time.Time {
	if key == "" {
		return times
	}
	if times == nil {
		times = make(map[string][]time.Time)
	}
	if len(times) > maxEscrowLookupCache {
		times = make(map[string][]time.Time)
	}
	times[key] = appendRecent(times[key], now)
	return times
}

func removeTime(times []time.Time, at time.Time) []time.Time {
	for i := len(times) - 1; i >= 0; i-- {
		if times[i].Equal(at) {
			return append(times[:i], times[i+1:]...)
		}
	}
	return times
}

func countRecent(times []time.Time, now time.Time) int {
	cutoff := now.Add(-time.Minute)
	n := 0
	for _, ts := range times {
		if ts.After(cutoff) {
			n++
		}
	}
	return n
}

func appendRecent(times []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-time.Minute)
	kept := times[:0]
	for _, ts := range times {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	return append(kept, now)
}

func cloneEscrowInfo(e *bridge.EscrowInfo) *bridge.EscrowInfo {
	if e == nil {
		return nil
	}
	cp := *e
	if e.Slots != nil {
		cp.Slots = append([]string(nil), e.Slots...)
	}
	if e.AppHash != nil {
		cp.AppHash = append([]byte(nil), e.AppHash...)
	}
	return &cp
}
