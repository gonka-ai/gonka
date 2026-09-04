// Package tipcache is a BlockOracle tip fed by Observe (Comet NewBlock).
// Latest/Subscribe do not use HTTP /block/latest or /block/stream.
// At() is served from the last HistoryWindow heights (Observe + Remember).
package tipcache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"common/chainoracle/blocks"
)

const subBufSize = 16

// HistoryWindow is how many recent heights Observe/Remember retain for At().
const HistoryWindow = 100

var errNoHeader = errors.New("blockoracle/tipcache: no header yet")

// Cache holds the latest observed header, the last HistoryWindow heights,
// and fans new tips out to subscribers.
type Cache struct {
	staleAfter time.Duration

	mu       sync.RWMutex
	latest   *blocks.Header
	byHeight map[int64]*blocks.Header
	subs     map[int]*subscription
	nextID   int

	lastRecvUnix atomic.Int64
}

type subscription struct {
	ch     chan *blocks.Header
	from   int64
	cancel context.CancelFunc
}

// New returns an empty cache. staleAfter ≤ 0 defaults to 10s.
func New(staleAfter time.Duration) *Cache {
	if staleAfter <= 0 {
		staleAfter = 10 * time.Second
	}
	return &Cache{
		staleAfter: staleAfter,
		byHeight:   make(map[int64]*blocks.Header),
		subs:       make(map[int]*subscription),
	}
}

// Observe records a committed header from the Comet NewBlock feed.
// Heights at or above the current tip become the tip; older heights in
// the last HistoryWindow are kept for At() and do not move Latest().
func (c *Cache) Observe(h *blocks.Header) {
	if c == nil || h == nil || h.Height <= 0 || blocks.IsDummyHeader(h) {
		return
	}
	cp := cloneHeader(h)
	c.mu.Lock()
	advance := c.latest == nil || cp.Height >= c.latest.Height
	if advance {
		c.latest = cp
		c.lastRecvUnix.Store(time.Now().UnixNano())
	}
	c.storeLocked(cp)
	var live []*subscription
	if advance {
		for _, sub := range c.subs {
			if cp.Height >= sub.from {
				live = append(live, sub)
			}
		}
	}
	c.mu.Unlock()
	for _, sub := range live {
		select {
		case sub.ch <- cloneHeader(cp):
		default:
		}
	}
}

// Remember stores a non-dummy header in the last HistoryWindow for At().
// It never moves Latest() or freshness; use Observe for Comet NewBlock.
func (c *Cache) Remember(h *blocks.Header) {
	if c == nil || h == nil || h.Height <= 0 || blocks.IsDummyHeader(h) {
		return
	}
	cp := cloneHeader(h)
	c.mu.Lock()
	c.storeLocked(cp)
	c.mu.Unlock()
}

func (c *Cache) Latest(context.Context) (*blocks.Header, error) {
	if c == nil {
		return nil, errNoHeader
	}
	c.mu.RLock()
	h := c.latest
	c.mu.RUnlock()
	if h == nil {
		return nil, errNoHeader
	}
	return cloneHeader(h), nil
}

func (c *Cache) At(_ context.Context, height int64) (*blocks.Header, error) {
	if c == nil {
		return nil, errNoHeader
	}
	c.mu.RLock()
	h := c.byHeight[height]
	c.mu.RUnlock()
	if h == nil {
		return nil, errNoHeader
	}
	return cloneHeader(h), nil
}

func (c *Cache) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (c *Cache) Subscribe(ctx context.Context, fromHeight int64) (<-chan *blocks.Header, error) {
	subCtx, cancel := context.WithCancel(ctx)
	sub := &subscription{
		ch:     make(chan *blocks.Header, subBufSize),
		from:   fromHeight,
		cancel: cancel,
	}
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.subs[id] = sub
	latest := c.latest
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.subs, id)
			c.mu.Unlock()
			cancel()
			close(sub.ch)
		}()
		if latest != nil && latest.Height >= fromHeight {
			select {
			case <-subCtx.Done():
				return
			case sub.ch <- cloneHeader(latest):
			}
		}
		<-subCtx.Done()
	}()
	return sub.ch, nil
}

// Stale is true when nothing has been observed, or the last Observe is older
// than staleAfter.
func (c *Cache) Stale() bool {
	if c == nil {
		return true
	}
	last := c.lastRecvUnix.Load()
	if last == 0 {
		return true
	}
	return time.Since(time.Unix(0, last)) > c.staleAfter
}

func (c *Cache) storeLocked(h *blocks.Header) {
	if h == nil {
		return
	}
	if c.byHeight == nil {
		c.byHeight = make(map[int64]*blocks.Header)
	}
	if c.latest != nil {
		floor := c.latest.Height - (HistoryWindow - 1)
		if h.Height < floor {
			return
		}
	}
	c.byHeight[h.Height] = h
	c.evictLocked()
}

func (c *Cache) evictLocked() {
	if c.latest != nil {
		floor := c.latest.Height - (HistoryWindow - 1)
		for height := range c.byHeight {
			if height < floor {
				delete(c.byHeight, height)
			}
		}
		return
	}
	for len(c.byHeight) > HistoryWindow {
		var minimum int64
		first := true
		for height := range c.byHeight {
			if first || height < minimum {
				minimum = height
				first = false
			}
		}
		delete(c.byHeight, minimum)
	}
}

func cloneHeader(h *blocks.Header) *blocks.Header {
	if h == nil {
		return nil
	}
	cp := *h
	cp.BlockHash = append([]byte(nil), h.BlockHash...)
	return &cp
}

var _ blocks.BlockOracle = (*Cache)(nil)
