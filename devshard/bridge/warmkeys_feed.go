package bridge

// WarmKeyFeedEvent is a grant change or the BLOCK marker closing a processed
// block. Entry is nil for a marker. Changes and their marker may span pages.
type WarmKeyFeedEvent struct {
	Height int64
	Entry  *WarmKeyEntry
}

const (
	warmKeyFeedWindow    = 512
	maxWarmKeyFeedEvents = 32768
)

type warmKeyFeed struct {
	generation uint64
	first      int64 // first fully covered height (the first BLOCK is a boundary)
	last       int64
	pending    []WarmKeyFeedEvent
	changes    map[int64][]WarmKeyEntry
	stored     int
}

// ApplyWarmKeyFeedPage is called after all events on a trusted dAPI page have
// been dispatched. The first BLOCK only establishes a cursor boundary.
func (c *WarmKeyLogCopy) ApplyWarmKeyFeedPage(generation uint64, events []WarmKeyFeedEvent) {
	if generation == 0 {
		c.DisconnectWarmKeyFeed()
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.feed.generation != generation {
		if c.feed.generation != 0 {
			c.resetLocked()
		}
		c.feed.generation = generation
	}
	for _, e := range events {
		switch {
		case e.Height <= 0:
			c.restartFeedLocked(generation, 0)
		case e.Entry != nil:
			if c.feed.last != 0 {
				if e.Height == c.feed.last+1 {
					for c.feed.stored+len(c.feed.pending) >= maxWarmKeyFeedEvents && c.feed.first <= c.feed.last {
						c.foldOldestLocked()
					}
					if c.feed.stored+len(c.feed.pending) >= maxWarmKeyFeedEvents {
						c.restartFeedLocked(generation, 0)
					} else {
						c.feed.pending = append(c.feed.pending, e)
					}
				} else {
					c.restartFeedLocked(generation, 0)
				}
			}
		case c.feed.last == 0:
			c.feed.first, c.feed.last = e.Height+1, e.Height
		case e.Height != c.feed.last+1:
			c.restartFeedLocked(generation, e.Height)
		default:
			c.feed.last = e.Height
			if len(c.feed.pending) != 0 {
				if c.feed.changes == nil {
					c.feed.changes = make(map[int64][]WarmKeyEntry)
				}
				c.feed.stored += len(c.feed.pending)
				for _, event := range c.feed.pending {
					c.feed.changes[e.Height] = append(c.feed.changes[e.Height], *event.Entry)
				}
				c.feed.pending = nil
			}
			floor := c.feed.last - warmKeyFeedWindow + 1
			for floor > c.feed.first {
				c.foldOldestLocked()
			}
		}
	}
}

// foldOldestLocked advances already bootstrapped granters before discarding
// a closed block. Each grant keeps its start and end heights, so folding
// preserves decisions at all previously covered heights.
func (c *WarmKeyLogCopy) foldOldestLocked() {
	height := c.feed.first
	changes := c.feed.changes[height]
	for _, e := range changes {
		g := c.granters[e.Granter]
		if g != nil && g.complete && g.coveredHeight == height-1 {
			mergeWarmKeyEntry(g.confirmed, e)
			if g.confirmed[e.ID].GrantedAtHeight <= 0 {
				g.complete = false
			}
		}
	}
	for _, g := range c.granters {
		if g.complete && g.coveredHeight == height-1 && g.confirmedHeight > 0 {
			g.coveredHeight = height
		}
	}
	delete(c.feed.changes, height)
	c.feed.stored -= len(changes)
	c.feed.first++
}

func (c *WarmKeyLogCopy) restartFeedLocked(generation uint64, boundary int64) {
	c.resetLocked()
	c.feed.generation = generation
	if boundary > 0 {
		c.feed.first, c.feed.last = boundary+1, boundary
	}
}

// DisconnectWarmKeyFeed invalidates coverage once per outage. Reads from the
// chain may supply a fresh baseline while this feed is unavailable.
func (c *WarmKeyLogCopy) DisconnectWarmKeyFeed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.feed.generation != 0 {
		c.resetLocked()
	}
}

// ResetWarmKeyFallback handles a Comet websocket reconnect or gap. A
// continuous trusted dAPI stream provides independent coverage.
func (c *WarmKeyLogCopy) ResetWarmKeyFallback() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.feed.generation == 0 {
		c.resetLocked()
	}
}

func (c *WarmKeyLogCopy) coverageLocked(g *granterLog, height int64) bool {
	if g == nil || !g.complete || g.confirmedHeight <= 0 {
		return false
	}
	return height <= g.coveredHeight ||
		(c.feed.last >= height && c.feed.first <= g.coveredHeight+1)
}

func (c *WarmKeyLogCopy) historicalLocked(granter string, g *granterLog, height int64) map[uint64]WarmKeyEntry {
	out := &granterLog{entries: make(map[uint64]WarmKeyEntry, len(g.confirmed))}
	for id, entry := range g.confirmed {
		if entry.GrantedAtHeight > height {
			continue
		}
		if entry.RevokedAtHeight > height {
			entry.RevokedAtHeight, entry.End = 0, ""
		}
		out.entries[id] = entry
	}
	for h := g.coveredHeight + 1; h <= height; h++ {
		for _, entry := range c.feed.changes[h] {
			if entry.Granter == granter {
				out.apply(entry)
			}
		}
	}
	return out.entries
}
