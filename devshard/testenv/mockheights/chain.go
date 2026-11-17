// Package mockheights is an in-process chain for tests. Progressing a height
// records that height, its block hash, and the wall clock at that moment.
// It does not write a header-time map and it does not poll.
package mockheights

import (
	"sync"
	"time"
)

// DefaultHash is the block hash the floor rig carries when a test does not set one.
var DefaultHash = []byte{0xaa}

// Block is one progressed height.
type Block struct {
	Height int64
	Hash   []byte
	TimeMs int64
}

// Chain records a wall-clock stamp the first time a height is progressed.
type Chain struct {
	mu    sync.Mutex
	hash  []byte
	tip   int64
	at    map[int64]Block
	watch []func(Block)
}

// New returns a chain whose hash is DefaultHash.
func New() *Chain {
	return &Chain{hash: append([]byte(nil), DefaultHash...), at: map[int64]Block{}}
}

// SetHash is the hash recorded for heights progressed after this call.
func (c *Chain) SetHash(hash []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hash = append([]byte(nil), hash...)
}

// Watch is called after a height is recorded for the first time in the current series.
func (c *Chain) Watch(fn func(Block)) {
	if fn == nil {
		return
	}
	c.mu.Lock()
	c.watch = append(c.watch, fn)
	c.mu.Unlock()
}

// Progress records height if this series has not seen it. A height below the
// current tip starts a new series and drops the old stamps. A repeat of a
// recorded height keeps the first timestamp.
func (c *Chain) Progress(height int64) (Block, bool) {
	if height <= 0 {
		return Block{}, false
	}
	c.mu.Lock()
	if c.tip > 0 && height < c.tip {
		c.at = map[int64]Block{}
		c.tip = 0
	}
	if b, ok := c.at[height]; ok {
		c.mu.Unlock()
		return b, false
	}
	b := Block{Height: height, Hash: append([]byte(nil), c.hash...), TimeMs: time.Now().UnixMilli()}
	c.at[height] = b
	if height > c.tip {
		c.tip = height
	}
	watch := make([]func(Block), len(c.watch))
	copy(watch, c.watch)
	c.mu.Unlock()
	for _, fn := range watch {
		fn(b)
	}
	return b, true
}

// At returns the record for height in the current series.
func (c *Chain) At(height int64) (Block, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.at[height]
	if !ok {
		return Block{}, false
	}
	b.Hash = append([]byte(nil), b.Hash...)
	return b, true
}
