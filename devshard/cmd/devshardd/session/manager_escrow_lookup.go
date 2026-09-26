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

// fetchEscrowForBind is the chain lookup used when owner chat finds no local
// session, and when Attach / BindGroupPeer has no fresh escrow_cache row.
// Eligibility (owner / group slot) is on the escrow record, so the query has
// to run before we know whether the peer belongs. Unique unknown or
// ineligible ids are charged against a per-peer (2/min) and process-wide
// budget before that query. A successful load that shows the peer is the
// creator or a slot member is refunded so first bind of a real escrow does
// not consume the unknown-id budget. Per origin IP is not keyed here: mixed
// fleets and hop-stamped X-Real-IP would collapse every client onto one
// 2/min slot. versiond applies that cap on inbound X-Real-IP after it sees
// a bind miss (X-Devshard-Error escrow_not_found / escrow_lookup_limited).
// Attach of a warmed id uses warmedEscrow instead (no query, no charge).
// RecoverSessions and create() with a prefetched escrow do not use this.
func (m *HostManager) fetchEscrowForBind(escrowID, peer string) (*bridge.EscrowInfo, error) {
	if m.bridge == nil {
		return nil, fmt.Errorf("get escrow: bridge is nil")
	}
	now := time.Now()
	if info, err, ok := m.cachedEscrowLookup(escrowID, now); ok {
		return info, err
	}
	chargedAt, err := m.chargeEscrowLookup(peer, now)
	if err != nil {
		return nil, err
	}
	info, err := m.bridge.GetEscrow(escrowID)
	if escrowLookupEligible(info, err, peer) {
		m.refundEscrowLookup(peer, chargedAt)
	}
	m.rememberEscrowLookup(escrowID, info, err, now)
	return info, err
}

// warmedEscrow returns a fresh escrow_cache row without a chain query. First
// Attach of a warmed escrow (this host in Slots, or any host that saw create)
// must not look like a cold miss. Unknown ids have no row and still go
// through fetchEscrowForBind. Owner chat does not use this: it still
// GetEscrow live (cache only if chain is down).
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
	if countRecent(m.escrowLookupFloor, now) >= unknownEscrowFloorPerMin {
		return time.Time{}, bridge.ErrEscrowLookupLimited
	}
	if peer != "" && countRecent(m.escrowLookupPeer[peer], now) >= unknownEscrowPerPeerPerMin {
		return time.Time{}, bridge.ErrEscrowLookupLimited
	}
	m.escrowLookupFloor = appendRecent(m.escrowLookupFloor, now)
	m.escrowLookupPeer = recordLookupTimes(m.escrowLookupPeer, peer, now)
	return now, nil
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
