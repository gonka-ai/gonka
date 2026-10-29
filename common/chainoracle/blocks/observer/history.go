package observer

import (
	"bytes"
	"context"
	"sync"
	"time"

	"common/chainoracle/blocks"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
)

const (
	DefaultHistoryHeights = 131_072
	BlockchainInfoBatch   = 20
)

type blockchainInfoRPC interface {
	BlockchainInfo(ctx context.Context, minHeight, maxHeight int64) (*ctypes.ResultBlockchainInfo, error)
}

type historyEntry struct {
	hash     []byte
	prevHash []byte
	timeMs   int64
	fromDAPI bool
}

type StoredHeader struct {
	Height   int64
	Hash     []byte
	PrevHash []byte
	TimeMs   int64
	ChainID  string
	Verified bool
	Epoch    int64

	fromDAPI bool
}

const DefaultDeepHeights = 32_768

type History struct {
	window int64

	mu        sync.RWMutex
	chainID   string
	top       int64
	entries   map[int64]historyEntry
	deep      map[int64]historyEntry
	deepOrder []int64
	changed   chan struct{}
}

func NewHistory(window int64) *History {
	if window <= 0 {
		window = DefaultHistoryHeights
	}
	return &History{window: window, entries: make(map[int64]historyEntry), deep: make(map[int64]historyEntry), changed: make(chan struct{})}
}

func (h *History) Put(hdr *blocks.Header) bool {
	if hdr == nil || hdr.Time.IsZero() {
		return false
	}
	return h.PutStored(StoredHeader{Height: hdr.Height, Hash: hdr.BlockHash, TimeMs: hdr.Time.UnixMilli(), ChainID: hdr.ChainID})
}

func (h *History) PutStored(s StoredHeader) bool {
	if h == nil || s.Height <= 0 || s.TimeMs == 0 || len(s.Hash) == 0 {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if s.ChainID != "" {
		h.chainID = s.ChainID
	}
	e := historyEntry{hash: append([]byte(nil), s.Hash...), prevHash: append([]byte(nil), s.PrevHash...), timeMs: s.TimeMs, fromDAPI: s.fromDAPI}
	if h.top > 0 && s.Height <= h.top-h.window {
		if prev, had := h.deep[s.Height]; had {
			if len(e.prevHash) == 0 {
				e.prevHash = prev.prevHash
			}
			if prev.fromDAPI {
				e.fromDAPI = true
			}
		} else {
			h.deepOrder = append(h.deepOrder, s.Height)
		}
		h.deep[s.Height] = e
		if len(h.deepOrder) > DefaultDeepHeights {
			drop := len(h.deepOrder) - DefaultDeepHeights
			for _, old := range h.deepOrder[:drop] {
				delete(h.deep, old)
			}
			h.deepOrder = append([]int64(nil), h.deepOrder[drop:]...)
		}
		return true
	}
	prev, had := h.entries[s.Height]
	if had {
		if len(e.prevHash) == 0 {
			e.prevHash = prev.prevHash
		}
		if prev.fromDAPI {
			e.fromDAPI = true
		}
	}
	h.entries[s.Height] = e
	if s.Height > h.top {
		h.top = s.Height
		h.evictLocked()
	}
	close(h.changed)
	h.changed = make(chan struct{})
	return true
}

func (h *History) evictLocked() {
	floor := h.top - h.window
	if floor <= 0 || int64(len(h.entries)) <= h.window {
		return
	}
	for height := range h.entries {
		if height <= floor {
			delete(h.entries, height)
		}
	}
}

func (h *History) entry(height int64) (historyEntry, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lookupLocked(height)
}

func (h *History) lookupLocked(height int64) (historyEntry, bool) {
	if e, ok := h.entries[height]; ok {
		return e, true
	}
	e, ok := h.deep[height]
	return e, ok
}

func (h *History) Get(height int64) (*blocks.Header, bool) {
	if h == nil {
		return nil, false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	e, ok := h.lookupLocked(height)
	if !ok {
		return nil, false
	}
	return blocks.HashOnlyHeader(height, time.UnixMilli(e.timeMs).UTC(), h.chainID, e.hash), true
}

func (h *History) Changed() <-chan struct{} {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.changed
}

func (h *History) Top() int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.top
}

func (h *History) ChainID() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.chainID
}

func (h *History) lowestLinkedAbove(height int64) (int64, historyEntry, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for next := height + 1; next <= h.top; next++ {
		if e, ok := h.lookupLocked(next); ok {
			if len(e.prevHash) == 0 {
				return 0, historyEntry{}, false
			}
			return next, e, true
		}
	}
	return 0, historyEntry{}, false
}

func (h *History) Len() int {
	if h == nil {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.entries)
}

func storedFromHeader(hdr *cmttypes.Header) StoredHeader {
	return StoredHeader{
		Height:   hdr.Height,
		Hash:     hdr.Hash().Bytes(),
		PrevHash: hdr.LastBlockID.Hash.Bytes(),
		TimeMs:   hdr.Time.UnixMilli(),
		ChainID:  hdr.ChainID,
	}
}

func verifiedFromHeader(hdr *cmttypes.Header) StoredHeader {
	row := storedFromHeader(hdr)
	row.Verified = true
	return row
}

func headersFromBlockchainInfo(res *ctypes.ResultBlockchainInfo) []*cmttypes.Header {
	if res == nil {
		return nil
	}
	out := make([]*cmttypes.Header, 0, len(res.BlockMetas))
	for _, meta := range res.BlockMetas {
		if meta == nil {
			continue
		}
		hdr := meta.Header
		hash := hdr.Hash().Bytes()
		if len(hash) == 0 || len(meta.BlockID.Hash) > 0 && !bytes.Equal(hash, meta.BlockID.Hash.Bytes()) {
			continue
		}
		out = append(out, &hdr)
	}
	return out
}
