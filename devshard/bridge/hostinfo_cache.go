package bridge

import (
	"errors"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// DefaultHostInfoCacheTTL is how long a Participant InferenceUrl may be
// reused without another chain query. Operators can change the URL; one
// minute is long enough to absorb recover/bind fan-out and short enough to
// pick up a move.
const DefaultHostInfoCacheTTL = time.Minute

const maxHostInfoCacheEntries = 4096

type hostInfoCacheEntry struct {
	info      *HostInfo
	err       error
	expiresAt time.Time
}

// CachingHostInfoBridge is a MainnetBridge wrapper that caches GetHostInfo
// for DefaultHostInfoCacheTTL. Other methods pass through. Participant-not-found
// is cached so a bad address cannot hammer the query path; transient errors
// are not, so a chain blip does not pin a miss. Concurrent misses for one
// address share a single chain query.
type CachingHostInfoBridge struct {
	MainnetBridge
	ttl    time.Duration
	now    func() time.Time
	mu     sync.Mutex
	byAddr map[string]hostInfoCacheEntry
	flight singleflight.Group
}

// NewCachingHostInfo wraps inner. inner must be non-nil.
func NewCachingHostInfo(inner MainnetBridge) *CachingHostInfoBridge {
	return &CachingHostInfoBridge{
		MainnetBridge: inner,
		ttl:           DefaultHostInfoCacheTTL,
		now:           time.Now,
		byAddr:        make(map[string]hostInfoCacheEntry),
	}
}

func (b *CachingHostInfoBridge) clock() time.Time {
	if b == nil || b.now == nil {
		return time.Now()
	}
	return b.now()
}

// GetHostInfo returns a cached Participant URL when the entry is still
// fresh. The returned HostInfo is a copy.
func (b *CachingHostInfoBridge) GetHostInfo(address string) (*HostInfo, error) {
	if info, err, ok := b.cached(address); ok {
		return info, err
	}
	v, err, _ := b.flight.Do(address, func() (any, error) {
		if info, err, ok := b.cached(address); ok {
			return info, err
		}
		info, err := b.MainnetBridge.GetHostInfo(address)
		if err != nil && !errors.Is(err, ErrParticipantNotFound) {
			return info, err
		}
		now := b.clock()
		b.mu.Lock()
		defer b.mu.Unlock()
		b.evictLocked(now)
		b.byAddr[address] = hostInfoCacheEntry{
			info:      cloneHostInfo(info),
			err:       err,
			expiresAt: now.Add(b.ttl),
		}
		return info, err
	})
	info, _ := v.(*HostInfo)
	return cloneHostInfo(info), err
}

func (b *CachingHostInfoBridge) cached(address string) (*HostInfo, error, bool) {
	now := b.clock()
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.byAddr[address]
	if !ok || !now.Before(e.expiresAt) {
		return nil, nil, false
	}
	return cloneHostInfo(e.info), e.err, true
}

func (b *CachingHostInfoBridge) evictLocked(now time.Time) {
	for addr, e := range b.byAddr {
		if !now.Before(e.expiresAt) {
			delete(b.byAddr, addr)
		}
	}
	if len(b.byAddr) < maxHostInfoCacheEntries {
		return
	}
	for addr := range b.byAddr {
		delete(b.byAddr, addr)
		if len(b.byAddr) < maxHostInfoCacheEntries {
			return
		}
	}
}

func cloneHostInfo(h *HostInfo) *HostInfo {
	if h == nil {
		return nil
	}
	cp := *h
	return &cp
}
