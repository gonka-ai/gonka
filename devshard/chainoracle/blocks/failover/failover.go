// Package failover is the host/gateway BlockOracle. Latest is the Comet
// NewBlock cache, then NodeManager GetBlockHeader, then chain
// GetLatestBlock. Those are the same committed tip over three pipes:
// Comet WS, dapi unary gRPC, and chain gRPC. At prefers the last 100
// cached heights, then GetBlockHeader, then chain At (GetBlockByHeight).
// Old dapi (Unimplemented) falls through; a remaining At miss yields a
// dummy header so L6 does not mark. Dummy headers are not cached.
// HTTP GET /block is not used.
package failover

import (
	"context"
	"errors"
	"sync"

	"common/chainoracle/blocks"
	"common/logging"
)

// Oracle: tip is the Comet NewBlock cache (Latest primary and At window).
// nm is GetBlockHeader. chain is GetLatestBlock for Latest() and
// GetBlockByHeight for At outside the window. HTTP is never used.
type Oracle struct {
	tip   blocks.BlockOracle
	nm    blocks.BlockOracle
	chain blocks.BlockOracle

	mu      sync.Mutex
	lastOK  bool
	fetched bool
}

// New wraps tip (Comet cache), optional nm (GetBlockHeader), and optional
// chain (GetLatestBlock / GetBlockByHeight).
func New(tip blocks.BlockOracle, nm blocks.BlockOracle, chain blocks.BlockOracle) *Oracle {
	return &Oracle{tip: tip, nm: nm, chain: chain}
}

func usable(h *blocks.Header) bool {
	return h != nil && !blocks.IsDummyHeader(h)
}

func (o *Oracle) Latest(ctx context.Context) (*blocks.Header, error) {
	if o == nil {
		return nil, errors.New("blockoracle/failover: no tip")
	}
	if o.tip != nil {
		h, err := o.tip.Latest(ctx)
		logLatestTry("comet_cache", h, err)
		if err == nil && usable(h) {
			o.note(true)
			return h, nil
		}
	} else {
		logLatestTry("comet_cache", nil, errors.New("not wired"))
	}
	var nmErr error
	if o.nm != nil {
		h, err := o.nm.Latest(ctx)
		logLatestTry("get_block_header", h, err)
		if err == nil && usable(h) {
			o.note(true)
			return h, nil
		}
		nmErr = err
	} else {
		logLatestTry("get_block_header", nil, errors.New("not wired"))
	}
	var chainErr error
	if o.chain != nil {
		h, err := o.chain.Latest(ctx)
		logLatestTry("get_latest_block", h, err)
		if err == nil && usable(h) {
			o.note(true)
			return h, nil
		}
		chainErr = err
	} else {
		logLatestTry("get_latest_block", nil, errors.New("not wired"))
	}
	o.note(false)
	if chainErr != nil {
		return nil, chainErr
	}
	if nmErr != nil {
		return nil, nmErr
	}
	return nil, errors.New("blockoracle/failover: no tip")
}

func logLatestTry(source string, h *blocks.Header, err error) {
	if err == nil && usable(h) {
		logging.Debug("heightsync: latest try", "heightsync",
			"source", source,
			"ok", true,
			"height", h.Height,
			"hash_len", len(h.BlockHash),
			"chain_id", h.ChainID)
		return
	}
	kvs := []any{"source", source, "ok", false}
	switch {
	case err != nil:
		kvs = append(kvs, "error", err.Error())
	case h == nil:
		kvs = append(kvs, "error", "nil header")
	default:
		kvs = append(kvs, "error", "unusable header", "height", h.Height, "hash_len", len(h.BlockHash))
	}
	logging.Debug("heightsync: latest try", "heightsync", kvs...)
}

func (o *Oracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	if o == nil {
		return blocks.DummyHeader(height), nil
	}
	if h := o.fromWindow(ctx, height); h != nil {
		return h, nil
	}
	if o.nm != nil {
		h, err := o.nm.At(ctx, height)
		if err == nil && usable(h) {
			o.remember(h)
			return h, nil
		}
	}
	if o.chain != nil {
		h, err := o.chain.At(ctx, height)
		if err == nil && usable(h) {
			o.remember(h)
			return h, nil
		}
		if err != nil && o.nm == nil {
			return nil, err
		}
	}
	return blocks.DummyHeader(height), nil
}

func (o *Oracle) fromWindow(ctx context.Context, height int64) *blocks.Header {
	if o.tip == nil {
		return nil
	}
	h, err := o.tip.At(ctx, height)
	if err != nil || !usable(h) || h.Height != height {
		return nil
	}
	return h
}

func (o *Oracle) remember(h *blocks.Header) {
	if !usable(h) {
		return
	}
	if r, ok := o.tip.(interface{ Remember(*blocks.Header) }); ok {
		r.Remember(h)
	}
}

func (o *Oracle) Prove(ctx context.Context, path string, height int64) (*blocks.Proof, error) {
	if o == nil || o.nm == nil {
		return nil, blocks.ErrProveNotImplemented
	}
	p, err := o.nm.Prove(ctx, path, height)
	if err != nil {
		if errors.Is(err, blocks.ErrProveNotImplemented) || errors.Is(err, blocks.ErrHeaderNotFound) {
			return nil, blocks.ErrProveNotImplemented
		}
		return nil, err
	}
	return p, nil
}

func (o *Oracle) Subscribe(ctx context.Context, fromHeight int64) (<-chan *blocks.Header, error) {
	if o == nil || o.tip == nil {
		ch := make(chan *blocks.Header)
		close(ch)
		return ch, nil
	}
	return o.tip.Subscribe(ctx, fromHeight)
}

func (o *Oracle) note(ok bool) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.fetched = true
	o.lastOK = ok
	o.mu.Unlock()
}

// Stale is true when Latest() has been attempted and the Comet cache,
// GetBlockHeader, and GetLatestBlock all failed.
func (o *Oracle) Stale() bool {
	if o == nil {
		return true
	}
	if so, ok := o.tip.(interface{ Stale() bool }); ok && !so.Stale() {
		return false
	}
	o.mu.Lock()
	fetched, lastOK := o.fetched, o.lastOK
	o.mu.Unlock()
	if !fetched {
		return false
	}
	return !lastOK
}

// Legacy is kept for tests that distinguished old dapi. Tip no longer
// depends on dapi HTTP, so this is always false.
func (o *Oracle) Legacy() bool {
	return false
}
