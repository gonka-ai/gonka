package tipcache

import (
	"context"

	"devshard/chainoracle/blocks"
)

// WithBootstrap is Latest from the Comet cache, falling back to a chain
// adapter until the first NewBlock arrives. Subscribe is cache-only.
type WithBootstrap struct {
	Live *Cache
	Boot blocks.BlockOracle
}

func (w *WithBootstrap) Latest(ctx context.Context) (*blocks.Header, error) {
	if w == nil {
		return nil, errNoHeader
	}
	if w.Live != nil {
		if h, err := w.Live.Latest(ctx); err == nil && h != nil {
			return h, nil
		}
	}
	if w.Boot != nil {
		return w.Boot.Latest(ctx)
	}
	return nil, errNoHeader
}

func (w *WithBootstrap) At(ctx context.Context, height int64) (*blocks.Header, error) {
	if w.Live != nil {
		if h, err := w.Live.At(ctx, height); err == nil {
			return h, nil
		}
	}
	if w.Boot != nil {
		return w.Boot.At(ctx, height)
	}
	return nil, errNoHeader
}

func (w *WithBootstrap) Prove(ctx context.Context, path string, height int64) (*blocks.Proof, error) {
	if w.Boot != nil {
		return w.Boot.Prove(ctx, path, height)
	}
	return nil, blocks.ErrProveNotImplemented
}

func (w *WithBootstrap) Subscribe(ctx context.Context, fromHeight int64) (<-chan *blocks.Header, error) {
	if w != nil && w.Live != nil {
		return w.Live.Subscribe(ctx, fromHeight)
	}
	if w != nil && w.Boot != nil {
		return w.Boot.Subscribe(ctx, fromHeight)
	}
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func (w *WithBootstrap) Stale() bool {
	if w == nil {
		return true
	}
	if w.Live != nil && !w.Live.Stale() {
		return false
	}
	if so, ok := w.Boot.(interface{ Stale() bool }); ok {
		return so.Stale()
	}
	return w.Live == nil || w.Live.Stale()
}

var _ blocks.BlockOracle = (*WithBootstrap)(nil)
