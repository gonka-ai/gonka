package failover

import (
	"context"
	"errors"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/lightproof"
)

// LightBlockAt does not apply the short At lookback window. The consumer
// authenticates evidence from either source against its pinned checkpoint.
func (o *Oracle) LightBlockAt(ctx context.Context, height int64) ([]byte, error) {
	if o == nil || height <= 0 {
		return nil, blocks.ErrHeaderNotFound
	}
	nm, _ := o.nm.(lightproof.Source)
	direct := o.lightBlocks
	if direct == nil {
		direct, _ = o.chain.(lightproof.Source)
	}
	if nm == nil && direct == nil {
		return nil, blocks.ErrProveNotImplemented
	}
	attempt := func(src lightproof.Source) func(context.Context) ([]byte, error) {
		return func(ctx context.Context) ([]byte, error) {
			raw, err := src.LightBlockAt(ctx, height)
			if err == nil && (len(raw) == 0 || len(raw) > lightproof.MaxBlockBytes) {
				return nil, blocks.ErrHeaderUnverified
			}
			return raw, err
		}
	}
	if direct == nil {
		return attempt(nm)(ctx)
	}
	if nm == nil {
		return attempt(direct)(ctx)
	}
	now := o.now()
	o.mu.Lock()
	preferDirect := now.Before(o.preferDirectProofUntil)
	o.mu.Unlock()
	if preferDirect {
		return withAlternative(ctx, primaryAttemptBudget(ctx), attempt(direct), attempt(nm), nil)
	}
	return withAlternative(ctx, primaryAttemptBudget(ctx), attempt(nm), attempt(direct), func() {
		// Spend at most one primary delay on an unavailable dAPI during a
		// sequence of bootstrap or validator-transition reads.
		o.mu.Lock()
		o.preferDirectProofUntil = now.Add(30 * time.Second)
		o.mu.Unlock()
	})
}

// LatestForProof keeps a live Comet cache first. Without it, give dAPI
// a bounded opportunity to report the tip, then ask chain gRPC directly.
// A cached chain response can understate the tip during that transition.
func (o *Oracle) LatestForProof(ctx context.Context) (*blocks.Header, error) {
	if o == nil || o.chain == nil {
		return o.Latest(ctx)
	}
	now := o.now()
	o.mu.Lock()
	cometOn := o.cometConnected
	preferDirect := now.Before(o.preferDirectTipUntil)
	o.mu.Unlock()
	if cometOn && o.tip != nil && o.cometFresh() {
		if h, err := o.tip.Latest(ctx); err == nil && usable(h) {
			o.noteLatest(h, true)
			return h, nil
		}
	}
	budget := 2 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(deadline)/2)
	}
	readDAPI := func(ctx context.Context) (*blocks.Header, error) {
		h, err, _ := o.tryNM(ctx, now)
		if err == nil && !usable(h) {
			err = blocks.ErrHeaderNotFound
		}
		if err == nil && preferDirect {
			o.mu.Lock()
			o.preferDirectTipUntil = time.Time{}
			o.mu.Unlock()
		}
		return h, err
	}
	readChain := func(ctx context.Context) (*blocks.Header, error) {
		h, err := o.queryChainTip(ctx, o.now())
		if err == nil && !preferDirect && ctx.Err() == nil {
			// Reuse the working route, never its height. A stalled dAPI
			// should cost only one grace period during a sequence of reads.
			until := o.now().Add(ChainRefresh)
			o.mu.Lock()
			o.preferDirectTipUntil = until
			o.mu.Unlock()
		}
		return h, err
	}
	if preferDirect {
		readDAPI, readChain = readChain, readDAPI
	}
	latest, err := withAlternative(ctx, budget, readDAPI, readChain, nil)
	o.noteLatest(latest, err == nil)
	return latest, err
}

// CanonicalHeaderForProof selects the configured local Comet endpoint for
// v7 reconciliation. An unavailable endpoint falls back to ordinary header
// lookup. A successful Comet answer is compared to the escrow checkpoint
// regardless of how quickly another endpoint might have answered.
func (o *Oracle) CanonicalHeaderForProof(ctx context.Context, height int64) (*blocks.Header, error) {
	if o == nil || o.lightBlocks == nil {
		return nil, blocks.ErrProveNotImplemented
	}
	rpcCtx, cancel := context.WithTimeout(ctx, primaryAttemptBudget(ctx))
	raw, err := o.lightBlocks.LightBlockAt(rpcCtx, height)
	cancel()
	if errors.Is(err, blocks.ErrHeaderUnverified) {
		return nil, err
	}
	if err != nil {
		return nil, blocks.ErrProveNotImplemented
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

func primaryAttemptBudget(ctx context.Context) time.Duration {
	budget := time.Second
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(deadline)/3)
	}
	return budget
}

// withAlternative starts an independent source after a short delay or an
// explicit error. A slow primary retains the caller's deadline if the
// secondary cannot answer. The caller must authenticate the selected value.
func withAlternative[T any](ctx context.Context, headStart time.Duration, primary, secondary func(context.Context) (T, error), onFallback func()) (T, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		value T
		err   error
	}
	start := func(read func(context.Context) (T, error)) <-chan result {
		done := make(chan result, 1)
		go func() {
			value, err := read(ctx)
			done <- result{value, err}
		}()
		return done
	}
	first := start(primary)
	timer := time.NewTimer(headStart)
	defer timer.Stop()
	var zero T
	var failures []error
	select {
	case got := <-first:
		if got.err == nil {
			return got.value, nil
		}
		failures = append(failures, got.err)
		first = nil
	case <-timer.C:
	case <-ctx.Done():
		return zero, ctx.Err()
	}
	if onFallback != nil && ctx.Err() == nil {
		onFallback()
	}
	second := start(secondary)
	for first != nil || second != nil {
		select {
		case got := <-first:
			if got.err == nil {
				return got.value, nil
			}
			failures = append(failures, got.err)
			first = nil
		case got := <-second:
			if got.err == nil {
				return got.value, nil
			}
			failures = append(failures, got.err)
			second = nil
		case <-ctx.Done():
			return zero, ctx.Err()
		}
	}
	return zero, errors.Join(failures...)
}
