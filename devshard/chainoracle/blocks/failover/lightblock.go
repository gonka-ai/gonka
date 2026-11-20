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
	now := o.now()
	o.mu.Lock()
	preferDirect := now.Before(o.preferDirectProofUntil)
	o.mu.Unlock()
	first, second := nm, direct
	if preferDirect {
		first, second = direct, nm
	}
	var failures []error
	for i, source := range []lightproof.Source{first, second} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if source == nil {
			continue
		}
		attemptCtx := ctx
		cancel := func() {}
		usingNM := (i == 0) != preferDirect
		if i == 0 && second != nil {
			if usingNM {
				attemptCtx, cancel = context.WithTimeout(ctx, primaryAttemptBudget(ctx))
			} else if deadline, ok := ctx.Deadline(); ok {
				attemptCtx, cancel = context.WithTimeout(ctx, 2*(time.Until(deadline)/3))
			}
		}
		raw, err := source.LightBlockAt(attemptCtx, height)
		cancel()
		if err == nil && len(raw) > 0 && len(raw) <= lightproof.MaxBlockBytes {
			if usingNM && preferDirect {
				o.mu.Lock()
				o.preferDirectProofUntil = time.Time{}
				o.mu.Unlock()
			}
			return raw, nil
		}
		if usingNM && direct != nil && ctx.Err() == nil &&
			(permanentNMSkip(err) || transientNM(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, blocks.ErrProveNotImplemented)) {
			// Bootstrap and validator transitions share a deadline across
			// many heights. One stalled dAPI is enough to prefer Comet.
			o.mu.Lock()
			o.preferDirectProofUntil = now.Add(30 * time.Second)
			o.mu.Unlock()
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

func primaryAttemptBudget(ctx context.Context) time.Duration {
	budget := time.Second
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(deadline)/3)
	}
	return budget
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
