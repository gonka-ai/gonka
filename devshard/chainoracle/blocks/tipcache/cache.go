// Package tipcache is a BlockOracle tip fed by Observe (Comet NewBlock).
// Latest/Subscribe do not use HTTP /block/latest or /block/stream.
package tipcache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"devshard/chainoracle/blocks"
)

const subBufSize = 16

var errNoHeader = errors.New("blockoracle/tipcache: no header yet")

// Cache holds the latest observed header and fans it out to subscribers.
type Cache struct {
	staleAfter time.Duration

	mu     sync.RWMutex
	latest *blocks.Header
	subs   map[int]*subscription
	nextID int

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
		subs:       make(map[int]*subscription),
	}
}

// Observe records a committed header from the Comet NewBlock feed.
func (c *Cache) Observe(h *blocks.Header) {
	if c == nil || h == nil || h.Height <= 0 {
		return
	}
	cp := cloneHeader(h)
	c.mu.Lock()
	if c.latest != nil && cp.Height < c.latest.Height {
		c.mu.Unlock()
		return
	}
	c.latest = cp
	c.lastRecvUnix.Store(time.Now().UnixNano())
	var live []*subscription
	for _, sub := range c.subs {
		if cp.Height >= sub.from {
			live = append(live, sub)
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

func (c *Cache) At(ctx context.Context, height int64) (*blocks.Header, error) {
	h, err := c.Latest(ctx)
	if err != nil {
		return nil, err
	}
	if h.Height != height {
		return nil, errNoHeader
	}
	return h, nil
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

func cloneHeader(h *blocks.Header) *blocks.Header {
	if h == nil {
		return nil
	}
	cp := *h
	cp.BlockHash = append([]byte(nil), h.BlockHash...)
	return &cp
}

var _ blocks.BlockOracle = (*Cache)(nil)
