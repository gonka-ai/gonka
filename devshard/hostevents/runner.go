package hostevents

import (
	"context"
	"strconv"
	"time"

	"common/nodemanager/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// devshardd only consumes escrow events. Maintenance windows are the gateway's
// concern (out of scope), so devshardd never subscribes to maintenance kinds.
var defaultSubscribe = []gen.HostEventKind{
	gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
	gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED,
}

// Run long-polls GetHostEvents until ctx cancel or Unimplemented (old dapi).
// Lazy escrow create remains available when this loop stops.
func Run(ctx context.Context, cfg Config, sink Sink) {
	if err := cfg.applyDefaults(); err != nil {
		if cfg.Log != nil {
			cfg.Log.Error("hostevents: invalid config", "err", err)
		}
		return
	}
	if sink == nil {
		cfg.Log.Error("hostevents: sink is required")
		return
	}

	var (
		cursor     uint64
		generation uint64
		backoff    time.Duration
		// retrySeq is the seq of the event whose dispatch failed and that the
		// cursor is currently rewound to; retries counts how many times it has
		// been redelivered.
		retrySeq uint64
		retries  int
	)

	for {
		if backoff > 0 {
			select {
			case <-ctx.Done():
				return
			case <-cfg.Clock.After(backoff):
			}
		}

		resp, err := pollOnce(ctx, cfg, cursor, generation)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if status.Code(err) == codes.Unimplemented {
				cfg.Log.Info("hostevents: GetHostEvents Unimplemented; stopping loop (lazy create remains)")
				return
			}
			backoff = nextBackoff(backoff, cfg.ErrorBackoffMin, cfg.ErrorBackoffMax)
			cfg.Log.Warn("hostevents: long-poll failed", "err", err, "backoff", backoff)
			continue
		}

		if cfg.LoadMap != nil {
			cfg.LoadMap.Replace(resp.GetEscrowLoad(), cfg.Clock.Now())
		}

		if resp.GetGeneration() != 0 {
			generation = resp.GetGeneration()
		}
		if resp.GetNeedsReset() {
			cfg.Log.Info("hostevents: needs_reset; re-hydrating open escrows",
				"generation", resp.GetGeneration(),
				"next_cursor", resp.GetNextCursor())
			sink.RehydrateOpenEscrows()
			cursor = resp.GetNextCursor()
			backoff, retrySeq, retries = 0, 0, 0
			continue
		}

		failed, ok := dispatchBatch(cfg, sink, resp.GetEvents())
		if !ok && failed.GetSeq() > 0 {
			// Settlement is the only durable signal a host gets when it missed
			// the chain event, so a failed dispatch must not be swallowed by
			// advancing past it. Rewind so the ring redelivers this event, and
			// hold the cursor here: later events stay queued rather than being
			// applied out of order ahead of a settlement we have not recorded.
			seq := failed.GetSeq()
			if seq != retrySeq {
				retrySeq, retries = seq, 0
			}
			retries++
			if retries <= cfg.MaxDispatchAttempts {
				cursor = seq - 1
				backoff = nextBackoff(backoff, cfg.ErrorBackoffMin, cfg.ErrorBackoffMax)
				cfg.Log.Warn("hostevents: dispatch failed, redelivering",
					"kind", failed.GetKind().String(),
					"seq", seq,
					"attempt", retries,
					"max_attempts", cfg.MaxDispatchAttempts,
					"backoff", backoff)
				continue
			}
			// Wedged on one event: skipping it keeps the rest of the stream
			// moving, but a skipped ESCROW_SETTLED is gone for good, so sweep
			// open escrows against the chain to re-derive what the event would
			// have told us. Operator-visible either way.
			cfg.Log.Error("hostevents: dispatch permanently failed, skipping event",
				"kind", failed.GetKind().String(),
				"seq", seq,
				"attempts", retries)
			sink.RehydrateOpenEscrows()
		}

		cursor = resp.GetNextCursor()
		backoff, retrySeq, retries = 0, 0, 0
	}
}

// dispatchBatch applies events in order and stops at the first failure,
// returning that event. Stopping preserves ordering for the redelivery the
// caller schedules; the remaining events are redelivered with it.
func dispatchBatch(cfg Config, sink Sink, events []*gen.HostEvent) (*gen.HostEvent, bool) {
	for _, ev := range events {
		if err := dispatch(cfg, sink, ev); err != nil {
			cfg.Log.Warn("hostevents: dispatch failed",
				"kind", ev.GetKind().String(),
				"seq", ev.GetSeq(),
				"err", err)
			return ev, false
		}
	}
	return nil, true
}

func pollOnce(ctx context.Context, cfg Config, cursor, generation uint64) (*gen.GetHostEventsResponse, error) {
	callCtx, cancel := context.WithTimeout(ctx, cfg.clientCallDeadline())
	defer cancel()
	return cfg.Client.GetHostEvents(callCtx, &gen.GetHostEventsRequest{
		Cursor:         cursor,
		MaxWaitSeconds: uint32(cfg.ServerMaxWait / time.Second),
		Subscribe:      defaultSubscribe,
		Generation:     generation,
	})
}

func dispatch(cfg Config, sink Sink, ev *gen.HostEvent) error {
	switch ev.GetKind() {
	case gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED:
		id := escrowIDString(ev)
		if id == "" {
			return nil
		}
		cfg.Log.Debug("hostevents: WarmEscrow", "escrow_id", id, "seq", ev.GetSeq())
		return sink.WarmEscrow(id)
	case gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED:
		id := escrowIDString(ev)
		if id == "" {
			return nil
		}
		cfg.Log.Debug("hostevents: OnEscrowSettled", "escrow_id", id, "seq", ev.GetSeq())
		return sink.OnEscrowSettled(id)
	default:
		return nil
	}
}

func escrowIDString(ev *gen.HostEvent) string {
	if ev.GetEscrow() == nil {
		return ""
	}
	return strconv.FormatUint(ev.GetEscrow().GetEscrowId(), 10)
}
