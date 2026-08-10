package transport

import (
	"context"
	"time"

	"devshard/host"
)

// HopObserver receives Step 5g hop stamps while an SSE stream is parsed.
// Implementations must be safe for concurrent use with a single Send.
// Metrics only — never feed Decide / picker / quarantine (R8).
type HopObserver interface {
	// OnReqMs is called once when a receipt carries req_ms.
	OnReqMs(reqMs int64)
	// OnChunk is called for each inference data line paired with a pending
	// :devshard-ts batch entry.
	OnChunk(tier string, mlMs, wMs, recvMs int64)
	// OnChunkAbsent is called for an inference data line with no pending stamp
	// (old host, spool catch-up, or malformed comment).
	OnChunkAbsent()
}

type hopObserverCtxKey struct{}

// ContextWithHopObserver attaches o to ctx for parseSSEResponse.
func ContextWithHopObserver(ctx context.Context, o HopObserver) context.Context {
	if o == nil {
		return ctx
	}
	return context.WithValue(ctx, hopObserverCtxKey{}, o)
}

func hopObserverFromContext(ctx context.Context) HopObserver {
	if ctx == nil {
		return nil
	}
	o, _ := ctx.Value(hopObserverCtxKey{}).(HopObserver)
	return o
}

// hopParseState pairs :devshard-ts batches with following data lines.
type hopParseState struct {
	obs     HopObserver
	pending *host.DevshardTSBatch
	next    int
}

func (s *hopParseState) onComment(line string) {
	batch, ok := host.ParseDevshardTSComment(line)
	if !ok {
		return
	}
	s.pending = &batch
	s.next = 0
}

func (s *hopParseState) onDataLine() {
	recvMs := time.Now().UnixMilli()
	if s.obs == nil {
		if s.pending != nil {
			s.next++
			if s.next >= len(s.pending.ML) {
				s.pending = nil
				s.next = 0
			}
		}
		return
	}
	if s.pending == nil || s.next >= len(s.pending.ML) {
		s.obs.OnChunkAbsent()
		s.pending = nil
		s.next = 0
		return
	}
	tier := s.pending.T
	if tier == "" {
		tier = host.HopTierLive
	}
	s.obs.OnChunk(tier, s.pending.ML[s.next], s.pending.W[s.next], recvMs)
	s.next++
	if s.next >= len(s.pending.ML) {
		s.pending = nil
		s.next = 0
	}
}

func (s *hopParseState) onReqMs(reqMs int64) {
	if s.obs == nil || reqMs <= 0 {
		return
	}
	s.obs.OnReqMs(reqMs)
}
