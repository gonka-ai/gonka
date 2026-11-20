package failover

import (
	"context"
	"errors"
	"fmt"
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
	policy := alternativePolicy[[]byte]{headStart: primaryAttemptBudget(ctx)}
	if preferDirect {
		return withAlternative(ctx, attempt(direct), attempt(nm), policy)
	}
	policy.onFallback = func() {
		// Spend at most one primary delay on an unavailable dAPI during a
		// sequence of bootstrap or validator-transition reads.
		o.mu.Lock()
		o.preferDirectProofUntil = now.Add(30 * time.Second)
		o.mu.Unlock()
	}
	return withAlternative(ctx, attempt(nm), attempt(direct), policy)
}

// LatestForProof keeps a live Comet cache first. Without it, give dAPI
// a bounded opportunity to report the tip, then ask chain gRPC directly.
// A cached chain response can understate the tip during that transition.
func (o *Oracle) LatestForProof(ctx context.Context) (*blocks.Header, error) {
	if o == nil {
		return o.Latest(ctx)
	}
	now := o.now()
	o.mu.Lock()
	cometOn := o.cometConnected
	o.mu.Unlock()
	if cometOn && o.tip != nil && o.cometFresh() {
		if h, err := o.tip.Latest(ctx); err == nil && o.acceptProofTip(h, false) == nil {
			o.noteLatest(h, true)
			return h, nil
		}
	}
	if o.chain == nil {
		h, err := o.Latest(ctx)
		if err != nil {
			return nil, err
		}
		if err := o.checkProofTip(h); err != nil {
			return nil, err
		}
		return h, nil
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
		return h, err
	}
	policy := alternativePolicy[*blocks.Header]{
		headStart: budget,
		accept:    o.checkProofTip,
		prefer:    newerProofTip,
	}
	latest, err := withAlternative(ctx, readDAPI, o.fetchChainTip, policy)
	o.noteLatest(latest, err == nil)
	return latest, err
}

func newerProofTip(a, b *blocks.Header) *blocks.Header {
	if b.Height > a.Height {
		return b
	}
	return a
}

// RecentProofTipHeight is a lower bound for rejecting stale checkpoints.
// Admission must still obtain a live tip from LatestForProof.
func (o *Oracle) RecentProofTipHeight() int64 {
	if o == nil {
		return 0
	}
	now := o.now()
	cometHeight := o.recentCometHeight(now)
	o.mu.Lock()
	defer o.mu.Unlock()
	return max(cometHeight, o.recentRPCTipHeightLocked(now))
}

func (o *Oracle) recentCometHeight(now time.Time) int64 {
	if observed, ok := o.tip.(interface{ LastObservedHeight() (int64, time.Time) }); ok {
		height, received := observed.LastObservedHeight()
		if !received.IsZero() && now.Sub(received) <= CometMaxAge {
			return height
		}
	}
	return 0
}

func (o *Oracle) recentRPCTipHeightLocked(now time.Time) int64 {
	if now.Sub(o.proofTipAt) <= CometMaxAge {
		return o.proofTipHeight
	}
	return 0
}

func (o *Oracle) checkProofTip(h *blocks.Header) error {
	return o.acceptProofTip(h, true)
}

// A successful RPC can extend the recent floor. The Comet cache retains its
// own original receive timestamp, including when the socket disconnects.
func (o *Oracle) acceptProofTip(h *blocks.Header, rememberRPC bool) error {
	if !usable(h) || h.Height <= 0 {
		return blocks.ErrHeaderNotFound
	}
	now := o.now()
	cometHeight := o.recentCometHeight(now)
	o.mu.Lock()
	defer o.mu.Unlock()
	floor := max(cometHeight, o.recentRPCTipHeightLocked(now))
	if h.Height < floor {
		return fmt.Errorf("%w: proof tip %d trails recently observed height %d", blocks.ErrHeaderUnverified, h.Height, floor)
	}
	if rememberRPC {
		o.rememberRPCTipLocked(h.Height, now)
	}
	return nil
}

func (o *Oracle) rememberRPCTipLocked(height int64, observed time.Time) {
	if height >= o.recentRPCTipHeightLocked(observed) {
		o.proofTipHeight = height
		o.proofTipAt = observed
	}
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

type alternativePolicy[T any] struct {
	headStart  time.Duration
	onFallback func()
	accept     func(T) error
	prefer     func(T, T) T
}

type alternativeResult[T any] struct {
	value T
	err   error
}

// withAlternative starts an independent source after a short delay or an
// explicit error. A slow primary retains the caller's deadline if the
// secondary cannot answer. A policy can select between two ready results.
func withAlternative[T any](ctx context.Context, primary, secondary func(context.Context) (T, error), policy alternativePolicy[T]) (T, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	start := func(read func(context.Context) (T, error)) <-chan alternativeResult[T] {
		done := make(chan alternativeResult[T], 1)
		go func() {
			value, err := read(ctx)
			done <- alternativeResult[T]{value, err}
		}()
		return done
	}
	accept := func(got alternativeResult[T]) error {
		if got.err == nil && policy.accept != nil {
			return policy.accept(got.value)
		}
		return got.err
	}
	first := start(primary)
	timer := time.NewTimer(policy.headStart)
	defer timer.Stop()
	var zero T
	var failures []error
	select {
	case got := <-first:
		err := accept(got)
		if err == nil {
			return got.value, nil
		}
		failures = append(failures, err)
		first = nil
	case <-timer.C:
	case <-ctx.Done():
		return zero, ctx.Err()
	}
	if policy.onFallback != nil && ctx.Err() == nil {
		policy.onFallback()
	}
	second := start(secondary)
	for first != nil || second != nil {
		var got alternativeResult[T]
		var pending *<-chan alternativeResult[T]
		select {
		case got = <-first:
			first = nil
			pending = &second
		case got = <-second:
			second = nil
			pending = &first
		case <-ctx.Done():
			return zero, ctx.Err()
		}
		if got.err == nil && policy.prefer != nil {
			var otherErr error
			got, otherErr = preferReadyAlternative(got, pending, policy.prefer)
			if otherErr != nil {
				failures = append(failures, otherErr)
			}
		}
		err := accept(got)
		if err == nil {
			return got.value, nil
		}
		failures = append(failures, err)
	}
	return zero, errors.Join(failures...)
}

func preferReadyAlternative[T any](got alternativeResult[T], pending *<-chan alternativeResult[T], prefer func(T, T) T) (alternativeResult[T], error) {
	select {
	case other := <-*pending:
		*pending = nil
		if other.err != nil {
			return got, other.err
		}
		got.value = prefer(got.value, other.value)
	default:
	}
	return got, nil
}
