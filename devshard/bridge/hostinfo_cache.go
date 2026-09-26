package bridge

import (
	"errors"
	"sync"
	"time"
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
// are not, so a chain blip does not pin a miss.
type CachingHostInfoBridge struct {
	MainnetBridge
	ttl    time.Duration
	now    func() time.Time
	mu     sync.Mutex
	byAddr map[string]hostInfoCacheEntry
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
	now := b.clock()
	b.mu.Lock()
	if e, ok := b.byAddr[address]; ok && now.Before(e.expiresAt) {
		info := cloneHostInfo(e.info)
		err := e.err
		b.mu.Unlock()
		return info, err
	}
	b.mu.Unlock()

	info, err := b.MainnetBridge.GetHostInfo(address)
	if err != nil && !errors.Is(err, ErrParticipantNotFound) {
		return info, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.evictLocked(now)
	b.byAddr[address] = hostInfoCacheEntry{
		info:      cloneHostInfo(info),
		err:       err,
		expiresAt: now.Add(b.ttl),
	}
	return cloneHostInfo(info), err
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
