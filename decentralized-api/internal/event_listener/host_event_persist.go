package event_listener

import (
	"context"
	"errors"

	"common/logging"
	"decentralized-api/apiconfig"

	"github.com/productscience/inference/x/inference/types"
)

func bootHostEventFeed(ctx context.Context, f *HostEventFeed) {
	if f == nil || f.store == nil {
		return
	}
	f.writer = f.store.Writer()
	if !f.completeBoot(ctx) {
		f.bootPending.Store(true)
		f.stalled.Store(true)
		return
	}
	f.bootPending.Store(false)
}

// completeBoot compares the store with the head marker and restores the ring,
// or starts a new generation when they disagree. A Head failure or a marker
// I/O error returns false without resetting. A corrupt marker or a confirmed
// mismatch starts a new generation. The caller keeps bootPending set and runs
// this again on the next walk, before any append.
func (f *HostEventFeed) completeBoot(ctx context.Context) bool {
	head, err := f.store.Head(ctx)
	if err != nil {
		if errors.Is(err, ErrWriterLocked) {
			logging.Warn("host_event_feed: writer lock held; feed stalled", types.EventProcessing, "writer", f.writer)
		} else if bootCanceled(err) {
			logging.Warn("host_event_feed: boot canceled", types.EventProcessing, "error", err)
		} else {
			logging.Warn("host_event_feed: unreadable store", types.EventProcessing, "error", err)
		}
		return false
	}
	marker, hasMarker, mErr := readHeadMarker(f.markerPath)
	if mErr != nil && !errors.Is(mErr, errCorruptHeadMarker) {
		logging.Warn("host_event_feed: unreadable head marker", types.EventProcessing, "error", mErr)
		return false
	}
	if errors.Is(mErr, errCorruptHeadMarker) {
		logging.Warn("host_event_feed: corrupt head marker", types.EventProcessing, "error", mErr)
		hasMarker = true
		marker = headMarker{}
	}
	if !hasMarker && head.Empty {
		bindGeneration(f.store, f.ring.Generation())
		f.stalled.Store(false)
		return true
	}
	if !hasMarker || marker.Writer != f.writer || marker.Generation != head.Generation || head.Seq < marker.Seq {
		gen := nextGeneration(f.ring.Generation())
		if head.Generation >= gen {
			gen = nextGeneration(head.Generation)
		}
		if err := ctx.Err(); err != nil {
			logging.Warn("host_event_feed: boot canceled", types.EventProcessing, "error", err)
			return false
		}
		if err := f.store.Reset(ctx, gen); err != nil {
			if bootCanceled(err) {
				logging.Warn("host_event_feed: boot canceled", types.EventProcessing, "error", err)
			} else {
				logging.Warn("host_event_feed: reset store", types.EventProcessing, "error", err)
			}
			return false
		}
		f.stalled.Store(false)
		f.finishGeneration(ctx, gen)
		logging.Info("host_event_feed: store does not match head marker; new generation", types.EventProcessing, "generation", gen)
		return true
	}
	snap, err := f.store.Load(ctx)
	if err != nil {
		if bootCanceled(err) {
			logging.Warn("host_event_feed: boot canceled", types.EventProcessing, "error", err)
		} else {
			logging.Warn("host_event_feed: load", types.EventProcessing, "error", err)
		}
		return false
	}
	if snap.Generation == 0 {
		snap.Generation = f.ring.Generation()
	}
	f.ring.Restore(snap.Generation, snap.NextSeq, snap.Tail)
	f.emitted.Store(snap.EmittedHeight)
	bindGeneration(f.store, snap.Generation)
	f.stalled.Store(false)
	return true
}

func (f *HostEventFeed) finishGeneration(ctx context.Context, generation uint64) {
	f.ring.ResetGeneration(generation)
	f.emitted.Store(0)
	bindGeneration(f.store, generation)
	if err := f.writeMarker(generation, 0, 0); err != nil {
		f.stalled.Store(true)
		logging.Warn("host_event_feed: head marker", types.EventProcessing, "error", err)
	}
}

func (f *HostEventFeed) startNewGeneration(ctx context.Context) bool {
	next := nextGeneration(f.ring.Generation())
	if f.store != nil {
		head, err := f.store.Head(ctx)
		if err != nil {
			f.stalled.Store(true)
			logging.Warn("host_event_feed: head before new generation", types.EventProcessing, "error", err)
			return false
		}
		if !(head.Generation == next && head.Empty) {
			if head.Generation >= next {
				next = nextGeneration(head.Generation)
			}
			if err := f.store.Reset(ctx, next); err != nil {
				f.stalled.Store(true)
				logging.Warn("host_event_feed: reset store", types.EventProcessing, "error", err)
				return false
			}
		}
		bindGeneration(f.store, next)
		if err := f.writeMarker(next, 0, 0); err != nil {
			f.stalled.Store(true)
			logging.Warn("host_event_feed: head marker", types.EventProcessing, "error", err)
			return false
		}
	}
	f.ring.ResetGeneration(next)
	f.stalled.Store(false)
	return true
}

func (f *HostEventFeed) commitHeight(ctx context.Context, height int64, epoch uint64, events []apiconfig.HostEvent) bool {
	if len(events) == 0 {
		return false
	}
	if f.store == nil {
		f.emitted.Store(height)
		f.ring.AppendBatch(events)
		f.stalled.Store(false)
		return true
	}
	stored, err := f.store.AppendHeight(ctx, height, epoch, events, AppendExpect{
		Generation: f.ring.Generation(),
		NextSeq:    f.ring.Head() + 1,
		PrevHeight: height - 1,
	})
	if errors.Is(err, ErrFeedDiverged) {
		logging.Warn("host_event_feed: store diverged from ring; new generation", types.EventProcessing, "height", height, "error", err)
		if !f.startNewGeneration(ctx) {
			return false
		}
		if f.bo != nil {
			if tip := f.bo.currentBlockHeight.Load(); tip > 0 {
				f.emitted.Store(tip - 1)
			}
		}
		return false
	}
	if err != nil {
		logging.Warn("host_event_feed: store append", types.EventProcessing, "height", height, "error", err)
		f.noteWriterLost(err)
		return false
	}
	var seq uint64
	if n := len(stored); n > 0 {
		seq = stored[n-1].Seq
	}
	if err := f.writeMarker(f.ring.Generation(), seq, height); err != nil {
		logging.Warn("host_event_feed: head marker", types.EventProcessing, "height", height, "error", err)
		f.stalled.Store(true)
		return false
	}
	// Ring rows at H imply emitted ≥ H when Bounds is read. Append the
	// height in one lock so head is never inside H.
	f.emitted.Store(height)
	f.ring.AppendBatch(stored)
	if f.epochs != nil && f.epochs.src != nil {
		if idx, _, ok := f.epochs.src.LatestEpoch(); ok && (!f.pruned || idx != f.prunedEpoch) {
			if err := f.store.Prune(ctx, idx); err != nil {
				logging.Warn("host_event_feed: prune", types.EventProcessing, "error", err)
			} else {
				f.pruned = true
				f.prunedEpoch = idx
				if head, err := f.store.Head(ctx); err == nil && head.Seq < seq {
					if err := f.writeMarker(f.ring.Generation(), head.Seq, head.Height); err != nil {
						logging.Warn("host_event_feed: head marker after prune", types.EventProcessing, "error", err)
					}
				}
			}
		}
	}
	f.stalled.Store(false)
	return true
}

func (f *HostEventFeed) writeMarker(generation, seq uint64, height int64) error {
	if f.store == nil {
		return nil
	}
	m := headMarker{Writer: f.writer, Generation: generation, Seq: seq, Height: height}
	if f.writeHead != nil {
		return f.writeHead(m)
	}
	return writeHeadMarkerFile(f.markerPath, m)
}

func bindGeneration(store HostEventStore, generation uint64) {
	s, ok := store.(*dbStore)
	if !ok || generation == 0 {
		return
	}
	s.mu.Lock()
	s.generation = generation
	s.mu.Unlock()
}

func bootCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func nextGeneration(current uint64) uint64 {
	if current == 0 || current == ^uint64(0) {
		return 1
	}
	return current + 1
}
