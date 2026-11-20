package failover

import (
	"context"
	"errors"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/lightproof"
)

// LightBlockAt does not apply the short At lookback window: escrow bootstrap
// and validator transitions can precede that cache. Every caller authenticates
// the returned evidence against its own checkpoint.
func (o *Oracle) LightBlockAt(ctx context.Context, height int64) ([]byte, error) {
	if o == nil || height <= 0 {
		return nil, blocks.ErrHeaderNotFound
	}
	nm, _ := o.nm.(lightproof.Source)
	direct := o.lightBlocks
	if direct == nil {
		direct, _ = o.chain.(lightproof.Source)
	}
	var failures []error
	for i, source := range []lightproof.Source{nm, direct} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if source == nil {
			continue
		}
		attemptCtx := ctx
		cancel := func() {}
		if i == 0 && direct != nil {
			// Leave most of the caller's budget for Comet commit and validator
			// reads, including when a connected dAPI stalls.
			attemptCtx, cancel = boundedPrimaryAttempt(ctx)
		}
		raw, err := source.LightBlockAt(attemptCtx, height)
		cancel()
		if err == nil && len(raw) > 0 && len(raw) <= lightproof.MaxBlockBytes {
			return raw, nil
		}
		if err == nil {
			err = blocks.ErrHeaderUnverified
		}
		failures = append(failures, err)
	}
	if len(failures) == 0 {
		return nil, blocks.ErrProveNotImplemented
	}
	return nil, errors.Join(failures...)
}

func boundedPrimaryAttempt(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := time.Second
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(deadline)/3)
	}
	return context.WithTimeout(ctx, budget)
}

func (o *Oracle) directLightHeader(ctx context.Context, height int64) (*blocks.Header, error) {
	raw, err := o.lightBlocks.LightBlockAt(ctx, height)
	if err != nil {
		return nil, err
	}
	lb, err := lightproof.Decode(raw)
	if err != nil {
		return nil, err
	}
	if lb.Height != height {
		return nil, blocks.ErrHeaderUnverified
	}
	return blocks.HashOnlyHeader(lb.Height, lb.Time, lb.ChainID, lb.Hash()), nil
}
