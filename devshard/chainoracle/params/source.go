package params

import (
	"context"
	"sync"
	"time"

	commrc "common/runtimeconfig"
)

// CachedSource implements runtimeconfig SnapshotSource, EpochSource, and
// Notifier from a SnapshotFetcher. Refresh polls the fetcher; when
// params_block_height advances, waiters on NotifyChan are woken.
type CachedSource struct {
	mu        sync.Mutex
	fetcher   SnapshotFetcher
	snap      commrc.Snapshot
	published runtimePublished
	ch        chan struct{}
}

// NewCachedSource seeds defaults then performs an initial Refresh. When fetcher
// is nil, the source stays at defaults and Refresh is a no-op.
func NewCachedSource(ctx context.Context, fetcher SnapshotFetcher, defaults commrc.Snapshot) (*CachedSource, error) {
	c := &CachedSource{
		fetcher: fetcher,
		snap:    defaults,
		ch:      make(chan struct{}),
	}
	if fetcher != nil {
		if err := c.Refresh(ctx); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *CachedSource) RuntimeConfigSnapshot(epochID uint64) commrc.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.snap
	s.CurrentEpochID = epochID
	return s
}

func (c *CachedSource) CurrentEpochID() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snap.CurrentEpochID
}

func (c *CachedSource) NotifyChan() (<-chan struct{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ch, true
}

// Refresh fetches the latest snapshot from chain. When params_block_height
// advances, subscribers are notified.
func (c *CachedSource) Refresh(ctx context.Context) error {
	if c.fetcher == nil {
		return nil
	}
	next, err := c.fetcher.FetchSnapshot(ctx)
	if err != nil {
		return err
	}
	next.ServedAt = time.Now().UTC()

	c.mu.Lock()
	defer c.mu.Unlock()
	if next.ParamsBlockHeight > c.snap.ParamsBlockHeight {
		c.snap = next
		close(c.ch)
		c.ch = make(chan struct{})
		return nil
	}
	// Same or regressed height: update fields in place without waking waiters.
	c.snap = next
	return nil
}

// SetSnapshot replaces the cached snapshot and wakes waiters when height advances.
// Used by testenv fault injection before mock-chain wiring is complete.
func (c *CachedSource) SetSnapshot(snap commrc.Snapshot) {
	if snap.ServedAt.IsZero() {
		snap.ServedAt = time.Now().UTC()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if snap.ParamsBlockHeight > c.snap.ParamsBlockHeight {
		c.snap = snap
		close(c.ch)
		c.ch = make(chan struct{})
		return
	}
	c.snap = snap
}

type runtimePublished struct {
	initialized bool
	epochID     uint64
	content     commrc.Snapshot
}

func laneCContentEqual(a, b commrc.Snapshot) bool {
	return a.LogprobsMode == b.LogprobsMode &&
		a.DevshardRequestsEnabled == b.DevshardRequestsEnabled &&
		a.MaxNonce == b.MaxNonce &&
		a.RefusalTimeout == b.RefusalTimeout &&
		a.ExecutionTimeout == b.ExecutionTimeout &&
		a.ValidationRate == b.ValidationRate &&
		a.VoteThresholdFactor == b.VoteThresholdFactor
}

// ApplyBlockIfChanged mirrors decentralized-api ApplyRuntimeConfigBlockIfChanged:
// params are read via gRPC at the observed block height; params_block_height advances
// only when lane-C content or epoch changes.
func (c *CachedSource) ApplyBlockIfChanged(blockHeight int64, epochID uint64, next commrc.Snapshot) bool {
	next.ServedAt = time.Now().UTC()
	next.CurrentEpochID = epochID

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.published.initialized &&
		c.published.epochID == epochID &&
		laneCContentEqual(c.published.content, next) {
		return false
	}

	if blockHeight > c.snap.ParamsBlockHeight {
		next.ParamsBlockHeight = blockHeight
	} else if next.ParamsBlockHeight <= c.snap.ParamsBlockHeight {
		next.ParamsBlockHeight = c.snap.ParamsBlockHeight
	}

	c.snap = next
	c.published = runtimePublished{
		initialized: true,
		epochID:     epochID,
		content:     next,
	}
	close(c.ch)
	c.ch = make(chan struct{})
	return true
}

// ApplyRevision publishes a snapshot when mock-chain admin bumped params_block_height
// without waiting for the next block tick (fault injection / citest).
func (c *CachedSource) ApplyRevision(next commrc.Snapshot) bool {
	next.ServedAt = time.Now().UTC()

	c.mu.Lock()
	defer c.mu.Unlock()

	if next.ParamsBlockHeight <= c.snap.ParamsBlockHeight &&
		c.published.initialized &&
		c.published.epochID == next.CurrentEpochID &&
		laneCContentEqual(c.published.content, next) {
		return false
	}

	if next.ParamsBlockHeight <= c.snap.ParamsBlockHeight {
		next.ParamsBlockHeight = c.snap.ParamsBlockHeight + 1
	}

	c.snap = next
	c.published = runtimePublished{
		initialized: true,
		epochID:     next.CurrentEpochID,
		content:     next,
	}
	close(c.ch)
	c.ch = make(chan struct{})
	return true
}
