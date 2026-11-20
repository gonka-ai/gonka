package failover

import (
	"context"
	"sync"
	"time"

	"common/chainoracle/blocks"
)

type requestObservationKey struct{}

type requestObservation struct {
	mu         sync.Mutex
	oracle     *Oracle
	header     *blocks.Header
	validUntil time.Time
}

// WithRequestProofObservation allows a local request to reuse the proof tip
// for its height-sync response while the originating observation is fresh.
func WithRequestProofObservation(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestObservationKey{}, &requestObservation{})
}

// LatestForHeightSync reads a response anchor tip. Other Latest consumers,
// including detached repair probes, still sample independently.
func (o *Oracle) LatestForHeightSync(ctx context.Context) (*blocks.Header, error) {
	h, scoped := o.requestProofObservation(ctx)
	if !scoped {
		return o.Latest(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if h != nil {
		return h, nil
	}
	// A height-sync anchor accompanies the receipt. Give a stalled dAPI
	// a short head start while leaving time for a fresh chain RPC.
	refresh, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	return o.LatestForProof(refresh)
}

func (o *Oracle) rememberRequestObservation(ctx context.Context, h *blocks.Header, validUntil time.Time) {
	scope, _ := ctx.Value(requestObservationKey{}).(*requestObservation)
	if scope == nil || h == nil || validUntil.IsZero() {
		return
	}
	localUntil := o.now().Add(time.Second)
	if localUntil.Before(validUntil) {
		validUntil = localUntil
	}
	scope.mu.Lock()
	scope.oracle, scope.header, scope.validUntil = o, cloneHeader(h), validUntil
	scope.mu.Unlock()
}

func (o *Oracle) requestProofObservation(ctx context.Context) (*blocks.Header, bool) {
	scope, _ := ctx.Value(requestObservationKey{}).(*requestObservation)
	if scope == nil {
		return nil, false
	}
	scope.mu.Lock()
	oracle, h, validUntil := scope.oracle, scope.header, scope.validUntil
	scope.mu.Unlock()
	if oracle == nil || oracle != o {
		return nil, false
	}
	if !o.now().Before(validUntil) || o.acceptProofTip(h, nil) != nil {
		return nil, true
	}
	return cloneHeader(h), true
}
