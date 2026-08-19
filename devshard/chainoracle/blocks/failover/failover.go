// Package failover is the host-side BlockOracle wrapper: prefer dapi
// /block/* and fall back to a direct-chain adapter on capability miss
// (old dapi) or transport failure (dapi down), without a process restart.
package failover

import (
	"context"
	"errors"
	"sync"
	"time"

	"common/chain"
	"devshard/chainoracle/blocks"
	blockclient "devshard/chainoracle/blocks/client"
)

const (
	modeHTTP   = iota // last /block/* succeeded
	modeChain         // last /block/* was a transport failure; re-probe after interval
	modeLegacy        // 404/501/Unimplemented; stay on chain until restart
)

// liveHTTP is implemented by blockclient.Client so failover can bypass the
// SSE cache and observe a mid-session dapi outage on the next Latest().
type liveHTTP interface {
	FetchLatest(ctx context.Context) (*blocks.Header, error)
	FetchAt(ctx context.Context, height int64) (*blocks.Header, error)
}

// Config tunes probe cadence. Zero ProbeInterval uses chain.DefaultRPCProbeInterval.
type Config struct {
	ProbeInterval time.Duration
	Now           func() time.Time
}

// Oracle prefers HTTP chainoracle (dapi / mock-dapi) and falls back to chain.
type Oracle struct {
	http  blocks.BlockOracle
	chain blocks.BlockOracle
	live  liveHTTP

	probeInterval time.Duration
	now           func() time.Time

	mu          sync.Mutex
	mode        int
	nextProbeAt time.Time
	lastOK      bool
	fetched     bool
	httpCloser  func()
}

// New wraps http (may be nil) and chain (may be nil). Either side may be missing;
// both missing makes Latest() fail and Stale() true.
func New(httpOracle, chainOracle blocks.BlockOracle, cfg Config, httpCloser func()) *Oracle {
	interval := cfg.ProbeInterval
	if interval <= 0 {
		interval = chain.DefaultRPCProbeInterval
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	o := &Oracle{
		http:          httpOracle,
		chain:         chainOracle,
		probeInterval: interval,
		now:           now,
		httpCloser:    httpCloser,
	}
	if live, ok := httpOracle.(liveHTTP); ok {
		o.live = live
	}
	if httpOracle == nil {
		o.mode = modeChain
	}
	return o
}

func (o *Oracle) Latest(ctx context.Context) (*blocks.Header, error) {
	return o.fetch(ctx, func(src blocks.BlockOracle, live bool) (*blocks.Header, error) {
		if live && o.live != nil {
			return o.live.FetchLatest(ctx)
		}
		if src == nil {
			return nil, errors.New("blockoracle/failover: no source")
		}
		return src.Latest(ctx)
	})
}

func (o *Oracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	return o.fetch(ctx, func(src blocks.BlockOracle, live bool) (*blocks.Header, error) {
		if live && o.live != nil {
			return o.live.FetchAt(ctx, height)
		}
		if src == nil {
			return nil, errors.New("blockoracle/failover: no source")
		}
		return src.At(ctx, height)
	})
}

func (o *Oracle) fetch(ctx context.Context, fn func(src blocks.BlockOracle, live bool) (*blocks.Header, error)) (*blocks.Header, error) {
	if o == nil {
		return nil, errors.New("blockoracle/failover: nil oracle")
	}

	o.mu.Lock()
	mode := o.mode
	probeDue := mode == modeChain && !o.nextProbeAt.After(o.now())
	o.mu.Unlock()

	if mode != modeLegacy && o.http != nil && (mode == modeHTTP || probeDue) {
		h, err := fn(o.http, true)
		if err == nil && h != nil {
			o.mu.Lock()
			o.mode = modeHTTP
			o.lastOK = true
			o.fetched = true
			o.mu.Unlock()
			return h, nil
		}
		if blockclient.IsCapabilityMiss(err) {
			o.markLegacy()
		} else {
			o.mu.Lock()
			o.mode = modeChain
			o.nextProbeAt = o.now().Add(o.probeInterval)
			o.mu.Unlock()
		}
	}

	if o.chain != nil {
		h, err := fn(o.chain, false)
		if err == nil && h != nil {
			o.mu.Lock()
			o.lastOK = true
			o.fetched = true
			o.mu.Unlock()
			return h, nil
		}
		o.mu.Lock()
		o.lastOK = false
		o.fetched = true
		o.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	o.mu.Lock()
	o.lastOK = false
	o.fetched = true
	o.mu.Unlock()
	return nil, errors.New("blockoracle/failover: no header")
}

func (o *Oracle) markLegacy() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.mode == modeLegacy {
		return
	}
	o.mode = modeLegacy
	closer := o.httpCloser
	o.httpCloser = nil
	if closer != nil {
		go closer()
	}
}

func (o *Oracle) Prove(ctx context.Context, path string, height int64) (*blocks.Proof, error) {
	if o == nil {
		return nil, blocks.ErrProveNotImplemented
	}
	o.mu.Lock()
	legacy := o.mode == modeLegacy
	o.mu.Unlock()
	if !legacy && o.http != nil {
		p, err := o.http.Prove(ctx, path, height)
		if err == nil {
			return p, nil
		}
		if errors.Is(err, blocks.ErrProveNotImplemented) || blockclient.IsCapabilityMiss(err) {
			return nil, blocks.ErrProveNotImplemented
		}
		return nil, err
	}
	if o.chain != nil {
		return o.chain.Prove(ctx, path, height)
	}
	return nil, blocks.ErrProveNotImplemented
}

func (o *Oracle) Subscribe(ctx context.Context, fromHeight int64) (<-chan *blocks.Header, error) {
	o.mu.Lock()
	legacy := o.mode == modeLegacy
	o.mu.Unlock()
	if !legacy && o.http != nil {
		return o.http.Subscribe(ctx, fromHeight)
	}
	if o.chain != nil {
		return o.chain.Subscribe(ctx, fromHeight)
	}
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

// Stale is true when a fetch has been attempted and neither HTTP nor chain
// has a usable tip. Before the first fetch it is false so callers hit Latest().
func (o *Oracle) Stale() bool {
	if o == nil {
		return true
	}
	o.mu.Lock()
	mode, lastOK, fetched := o.mode, o.lastOK, o.fetched
	o.mu.Unlock()
	if !fetched {
		return false
	}
	if !lastOK {
		return true
	}
	if mode == modeHTTP {
		if so, ok := o.http.(interface{ Stale() bool }); ok {
			return so.Stale()
		}
	}
	if so, ok := o.chain.(interface{ Stale() bool }); ok && mode != modeHTTP {
		return so.Stale()
	}
	return false
}

// Legacy reports a permanent capability miss (old dapi).
func (o *Oracle) Legacy() bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.mode == modeLegacy
}
