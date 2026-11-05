package state

import (
	"context"
	"fmt"
	"time"

	"devshard/chainoracle/blocks/headertime"
	"devshard/types"
)

// headerHeightEnsure asks the map to fetch specific heights without blocking.
// *headertime.Source implements it. Prefer this over EnsureFrom for a single
// floor height (EnsureFrom queues the whole range to the tip).
type headerHeightEnsure interface {
	EnsureHeights(heights []uint64)
}

// withResolvedDecision runs fn while sm.mu is held. On a live apply that is
// about to settlement-drain, if floor TimeMs is still 0 and the map misses F,
// it waits for the map (outside the lock) before fn. Drain/seal clocks prefer
// the map when present (see floorTimeMsLocked). Replay never waits.
func (sm *StateMachine) withResolvedDecision(nonce uint64, _ []*types.DevshardTx, replay bool, fn func() error) error {
	if !replay {
		if err := sm.waitFloorTimeIfMissing(nonce); err != nil {
			return err
		}
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	prevReplay := sm.replayingPersisted
	if replay {
		sm.replayingPersisted = true
	}
	defer func() { sm.replayingPersisted = prevReplay }()
	return fn()
}

// waitFloorTimeIfMissing blocks until the header map stores time(F) when this
// live apply is about to run the Finalizing→Settlement drain and the floor
// point has no carried TimeMs. Ordinary applies do not wait. No-op when F is
// empty, carried time is set, the map already has the row, or drain will not
// run on this nonce.
//
// Outside the lock it may EnsureHeights(F) and wait on the map Watch /
// StoredAt poll. It never calls HeaderTimeMs or AwaitHeaderTimeMs (those
// start a filler fetch and can wait under misleading call sites).
// Caller must not hold sm.mu.
func (sm *StateMachine) waitFloorTimeIfMissing(nonce uint64) error {
	if sm == nil {
		return nil
	}
	sm.mu.RLock()
	if !sm.maySettlementDrainOnNonceLocked(nonce) {
		sm.mu.RUnlock()
		return nil
	}
	if !sm.floorReady || sm.heightSyncFloor == nil {
		sm.mu.RUnlock()
		return nil
	}
	p, known := sm.heightSyncFloor.PointAsOf(nonce)
	if !known || p.Height == 0 || p.TimeMs > 0 {
		sm.mu.RUnlock()
		return nil
	}
	store, hasStore := sm.storedHeadersLocked()
	if !hasStore {
		// Fallback HeaderTimeMs clocks are not a pollable map — cannot wait.
		// Drain fails closed with ErrFloorTimeMissing under the lock.
		sm.mu.RUnlock()
		return nil
	}
	if row, found := store.StoredAt(p.Height); found && row.TimeMs > 0 {
		sm.mu.RUnlock()
		return nil
	}
	height := p.Height
	src := sm.headerTimes
	wait := sm.headerWait
	sm.mu.RUnlock()

	if ensure, ok := src.(headerHeightEnsure); ok && ensure != nil {
		ensure.EnsureHeights([]uint64{height})
	}
	if wait <= 0 {
		wait = headertime.AnswerWait
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()

	var notify <-chan struct{}
	if w, ok := src.(HeaderTimeWatch); ok && w != nil {
		ch := make(chan struct{}, 1)
		unwatch := w.Watch(func() {
			select {
			case ch <- struct{}{}:
			default:
			}
		})
		if unwatch != nil {
			defer unwatch()
		}
		notify = ch
	}

	for {
		sm.mu.RLock()
		reader, ok := sm.storedHeadersLocked()
		var ms int64
		if ok {
			if row, found := reader.StoredAt(height); found {
				ms = row.TimeMs
			}
		}
		sm.mu.RUnlock()
		if ms > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: height %d", types.ErrHeaderPending, height)
		case <-notify:
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// maySettlementDrainOnNonceLocked reports that applying nonce would run
// drainLiveIntoSealedAccLocked (Finalizing deadline reached). Caller holds sm.mu.
func (sm *StateMachine) maySettlementDrainOnNonceLocked(nonce uint64) bool {
	if sm.state.Phase != types.PhaseFinalizing {
		return false
	}
	fin := sm.state.FinalizeNonce
	if fin == 0 {
		fin = nonce
	}
	group := uint64(len(sm.state.Group))
	return nonce >= fin+group
}
