// Package tipcache is a BlockOracle tip fed by Observe (Comet NewBlock).
// Latest/Subscribe do not use HTTP /block/latest or /block/stream.
// At() is served from the last HistoryWindow heights (Observe + Remember).
package tipcache

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"common/chainoracle/blocks"
)

const subBufSize = 16

// HistoryWindow is how far below the tip Observe/Remember retain for At().
// oldest = max(1, tip − HistoryWindow).
const HistoryWindow = blocks.HistoryWindow

var errNoHeader = errors.New("blockoracle/tipcache: no header yet")

// Cache holds the latest observed header, the last HistoryWindow heights,
// and fans new tips out to subscribers.
type Cache struct {
	staleAfter time.Duration

	mu        sync.RWMutex
	latest    *blocks.Header
	byHeight  map[int64]*blocks.Header
	minHeight int64
	subs      map[int]*subscription
	nextID    int

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
	old := c.latest
	replaced := old != nil && cp.Height == old.Height && !bytes.Equal(cp.BlockHash, old.BlockHash)
	advance := old == nil || cp.Height >= old.Height
	if advance {
		c.latest = cp
		c.lastRecvUnix.Store(time.Now().UnixNano())
	}
	c.storeLocked(cp)
	if old != nil && cp.Height > old.Height {
		c.evictRangeLocked(blocks.OldestHeight(old.Height), blocks.OldestHeight(cp.Height))
	}
	// Send under the lock so Subscribe cannot close sub.ch while we send.
	if advance {
		for _, sub := range c.subs {
			if !wakeSubscriber(sub.from, cp.Height, replaced) {
				continue
			}
			select {
			case sub.ch <- cloneHeader(cp):
			default:
			}
		}
	}
	c.mu.Unlock()
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

// StoredOldest is the lowest height actually in the map. After restart or a
// sparse Observe, that is the tip, not tip − HistoryWindow.
func (c *Cache) StoredOldest() int64 {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.minHeight
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
		floor := blocks.OldestHeight(c.latest.Height)
		if h.Height < floor {
			return
		}
	}
	c.byHeight[h.Height] = h
	if c.minHeight == 0 || h.Height < c.minHeight {
		c.minHeight = h.Height
	}
}

// evictRangeLocked drops [oldFloor, newFloor). Sequential Observe(+1) is one delete.
func (c *Cache) evictRangeLocked(oldFloor, newFloor int64) {
	if newFloor <= oldFloor {
		return
	}
	for height := oldFloor; height < newFloor; height++ {
		delete(c.byHeight, height)
	}
	c.refreshMinLocked(newFloor)
}

func (c *Cache) refreshMinLocked(from int64) {
	if c.minHeight != 0 {
		if _, ok := c.byHeight[c.minHeight]; ok && c.minHeight >= from {
			return
		}
	}
	c.minHeight = 0
	if c.latest == nil {
		return
	}
	if from < 1 {
		from = 1
	}
	for h := from; h <= c.latest.Height; h++ {
		if _, ok := c.byHeight[h]; ok {
			c.minHeight = h
			return
		}
	}
}

func wakeSubscriber(from, height int64, replaced bool) bool {
	if height >= from {
		return true
	}
	return replaced && from == height+1
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
