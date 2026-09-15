// Package failover is the host/gateway BlockOracle: Latest/Subscribe come from
// the Comet NewBlock cache, falling back to direct chain when that cache is
// empty (WS not yet connected, or down). At prefers the last 100 cached
// heights, then dapi GET /block/:height, then chain At when dapi is down.
// Old dapi (404) yields a dummy header so L6 does not mark. Dummy headers
// are not cached.
package failover

import (
	"context"
	"errors"
	"sync"

	"common/chainoracle/blocks"
	blockclient "devshard/chainoracle/blocks/client"
)

// History is unary height lookup (L6) and prove (Strong). Optional.
type History interface {
	At(ctx context.Context, height int64) (*blocks.Header, error)
	Prove(ctx context.Context, path string, height int64) (*blocks.Proof, error)
}

// Oracle prefers the Comet tip for Latest/Subscribe. HTTP is never used
// for tip motion. chain is the GetLatestBlock / GetBlockByHeight fallback.
type Oracle struct {
	tip   blocks.BlockOracle
	hist  History
	chain blocks.BlockOracle

	mu      sync.Mutex
	lastOK  bool
	fetched bool
}

// New wraps tip (Comet cache), optional hist (dapi At/Prove), and optional
// chain (direct fallback when Comet or dapi is down).
func New(tip blocks.BlockOracle, hist History, chain blocks.BlockOracle) *Oracle {
	return &Oracle{tip: tip, hist: hist, chain: chain}
}

func (o *Oracle) Latest(ctx context.Context) (*blocks.Header, error) {
	if o == nil {
		return nil, errors.New("blockoracle/failover: no tip")
	}
	if o.tip != nil {
		h, err := o.tip.Latest(ctx)
		if err == nil && h != nil {
			o.note(true)
			return h, nil
		}
	}
	if o.chain != nil {
		h, err := o.chain.Latest(ctx)
		if err == nil && h != nil {
			o.note(true)
			return h, nil
		}
		o.note(false)
		if err != nil {
			return nil, err
		}
	}
	o.note(false)
	return nil, errors.New("blockoracle/failover: no tip")
}

func (o *Oracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	if o == nil {
		return blocks.DummyHeader(height), nil
	}
	if h := o.fromWindow(ctx, height); h != nil {
		return h, nil
	}
	if o.hist != nil {
		h, err := o.hist.At(ctx, height)
		if err == nil && h != nil {
			o.remember(h)
			return h, nil
		}
		if err != nil && blockclient.IsCapabilityMiss(err) {
			return blocks.DummyHeader(height), nil
		}
		if o.chain == nil {
			if err != nil {
				return nil, err
			}
			return blocks.DummyHeader(height), nil
		}
	}
	if o.chain != nil && o.hist != nil {
		h, err := o.chain.At(ctx, height)
		if err == nil && h != nil {
			o.remember(h)
			return h, nil
		}
		if err != nil {
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
	if err != nil || h == nil || h.Height != height || blocks.IsDummyHeader(h) {
		return nil
	}
	return h
}

func (o *Oracle) remember(h *blocks.Header) {
	if h == nil || blocks.IsDummyHeader(h) {
		return
	}
	if r, ok := o.tip.(interface{ Remember(*blocks.Header) }); ok {
		r.Remember(h)
	}
}

func (o *Oracle) Prove(ctx context.Context, path string, height int64) (*blocks.Proof, error) {
	if o == nil || o.hist == nil {
		return nil, blocks.ErrProveNotImplemented
	}
	p, err := o.hist.Prove(ctx, path, height)
	if err != nil {
		if errors.Is(err, blocks.ErrProveNotImplemented) || blockclient.IsCapabilityMiss(err) {
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

// Stale is true when Latest() has been attempted and both Comet and chain failed.
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
// depends on dapi, so this is always false.
func (o *Oracle) Legacy() bool {
	return false
}
