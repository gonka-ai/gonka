package rpcserver

import (
	"container/list"
	"context"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"devshard/transport/rpcpb/rpcpbconnect"
)

// originIPHeader is the client address stamped by nginx ($remote_addr) or
// by proxy-router (src). The child does not use RemoteAddr: that is versiond.
const originIPHeader = "X-Real-IP"

// originAttachEntry is one origin's anonymous Attach budget. The list
// element is the LRU position (front = oldest).
type originAttachEntry struct {
	key    string
	bucket attachBucket
	el     *list.Element
}

// originAttachTable is the bounded set of origin buckets. It is only
// touched while PeerAuthHandler.attachMu is held.
type originAttachTable struct {
	byKey map[string]*originAttachEntry
	order *list.List
}

func newOriginAttachTable() *originAttachTable {
	return &originAttachTable{
		byKey: make(map[string]*originAttachEntry),
		order: list.New(),
	}
}

// attachOriginKey is the origin bucket key: one IPv4 address, or one
// IPv6 /64. An empty string means the header is missing or not an address,
// and the caller must not invent a shared key for that case.
func attachOriginKey(header http.Header) string {
	if header == nil {
		return ""
	}
	raw := strings.TrimSpace(header.Get(originIPHeader))
	if raw == "" {
		return ""
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		host, _, splitErr := net.SplitHostPort(raw)
		if splitErr != nil {
			return ""
		}
		addr, err = netip.ParseAddr(host)
		if err != nil {
			return ""
		}
	}
	if !addr.IsValid() {
		return ""
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return ""
	}
	return prefix.String()
}

// originShare is want, or a fraction of process when want would meet or
// exceed it. A process cap of 1 cannot be split, so the origin cap is 1
// and the process floor still refuses the next address.
func originShare(want, process float64) float64 {
	if process <= 0 || want < process {
		return want
	}
	if process <= 1 {
		return process
	}
	scaled := math.Floor(process / 5)
	if scaled < 1 {
		return 1
	}
	return scaled
}

func (h *PeerAuthHandler) originAttachLimit() (burst, perSec float64, maxIn int, unlimited bool) {
	pBurst, pPerSec, pIn, unlimited := h.attachLimit()
	if unlimited {
		return 0, 0, 0, true
	}
	return originShare(float64(defaultOriginAttachBurst), pBurst),
		originShare(float64(defaultOriginAttachRefillPerSec), pPerSec),
		int(originShare(float64(defaultOriginAttachInFlight), float64(pIn))),
		false
}

// acquireAttachVerify takes an origin token, when X-Real-IP parses, and
// then a process-floor token, both before VerifyAttach. The origin refusal
// does not spend the process token. A live session token does not call this.
// The returned function releases both in-flight slots.
func (h *PeerAuthHandler) acquireAttachVerify(header http.Header) (func(), error) {
	release := func() {}
	pBurst, pPerSec, pIn, unlimited := h.attachLimit()
	if unlimited {
		return release, nil
	}
	key := attachOriginKey(header)
	var oBurst, oPerSec float64
	var oIn int
	if key != "" {
		oBurst, oPerSec, oIn, _ = h.originAttachLimit()
	}
	now := h.now()
	h.attachMu.Lock()
	origin, originOK, processOK := h.takeAnonymousLocked(key, now, pBurst, pPerSec, pIn, oBurst, oPerSec, oIn)
	h.attachMu.Unlock()
	if !originOK {
		h.warnAttach(zoneAttachOrigin, key)
		return release, attachOriginExhausted(attachRetryAfterSec)
	}
	if !processOK {
		h.warnAttach(zoneAttachFloor, "process")
		return release, attachFloorExhausted(attachRetryAfterSec)
	}
	return func() { h.releaseAnonymous(origin) }, nil
}

func (h *PeerAuthHandler) warnAttach(zone rateLimitZone, key string) {
	if h == nil || h.limiter == nil {
		return
	}
	h.limiter.warnBanned(context.Background(), rpcpbconnect.PeerAuthServiceAttachProcedure, zone, key)
}

// takeAnonymousLocked spends the origin token first. A full origin table
// whose oldest entries are all in flight returns a nil origin and leaves
// that request on the process floor alone, so the table cannot refuse a
// new address. Caller holds attachMu.
func (h *PeerAuthHandler) takeAnonymousLocked(key string, now time.Time, pBurst, pPerSec float64, pIn int, oBurst, oPerSec float64, oIn int) (origin *originAttachEntry, originOK, processOK bool) {
	originOK = true
	if key != "" {
		entry := h.originEntryLocked(key)
		if entry != nil {
			entry.bucket.refill(now, oBurst, oPerSec)
			if entry.bucket.tokens < 1 || entry.bucket.inFlight >= oIn {
				return nil, false, false
			}
			origin = entry
		}
	}
	h.attachBucket.refill(now, pBurst, pPerSec)
	if h.attachBucket.tokens < 1 || h.attachBucket.inFlight >= pIn {
		return nil, true, false
	}
	if origin != nil {
		origin.bucket.tokens--
		origin.bucket.inFlight++
	}
	h.attachBucket.tokens--
	h.attachBucket.inFlight++
	return origin, true, true
}

func (h *PeerAuthHandler) releaseAnonymous(origin *originAttachEntry) {
	h.attachMu.Lock()
	defer h.attachMu.Unlock()
	if h.attachBucket.inFlight > 0 {
		h.attachBucket.inFlight--
	}
	if origin != nil && origin.bucket.inFlight > 0 {
		origin.bucket.inFlight--
	}
}

// originEntryLocked returns the entry for key, creating it when the table
// has room. Front-of-list idle entries are dropped first. Caller holds attachMu.
func (h *PeerAuthHandler) originEntryLocked(key string) *originAttachEntry {
	if h.origins == nil {
		h.origins = newOriginAttachTable()
	}
	if e := h.origins.byKey[key]; e != nil {
		h.origins.order.MoveToBack(e.el)
		return e
	}
	if h.origins.order.Len() >= maxOriginAttachEntries {
		h.evictIdleOriginsLocked()
	}
	if h.origins.order.Len() >= maxOriginAttachEntries {
		return nil
	}
	e := &originAttachEntry{key: key}
	e.el = h.origins.order.PushBack(e)
	h.origins.byKey[key] = e
	return e
}

func (h *PeerAuthHandler) evictIdleOriginsLocked() {
	n := 0
	for el := h.origins.order.Front(); el != nil && n < originAttachEvictBatch; n++ {
		next := el.Next()
		e := el.Value.(*originAttachEntry)
		if e.bucket.inFlight == 0 {
			delete(h.origins.byKey, e.key)
			h.origins.order.Remove(el)
		}
		el = next
	}
}
