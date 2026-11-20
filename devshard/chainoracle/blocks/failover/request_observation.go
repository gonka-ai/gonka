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

func (o *Oracle) requestProofObservation(ctx context.Context) *blocks.Header {
	scope, _ := ctx.Value(requestObservationKey{}).(*requestObservation)
	if scope == nil {
		return nil
	}
	scope.mu.Lock()
	oracle, h, validUntil := scope.oracle, scope.header, scope.validUntil
	scope.mu.Unlock()
	if oracle != o || !o.now().Before(validUntil) || o.acceptProofTip(h, nil) != nil {
		return nil
	}
	return cloneHeader(h)
}
