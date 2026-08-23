package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"devshard/observability"
	"devshard/types"
)

// ErrPersistExhausted is returned when AppendDiffWithRetry fails every attempt
// (excluding ErrDiffFork, which is not retried).
var ErrPersistExhausted = errors.New("diff persist retries exhausted")

const (
	// DefaultPersistMaxAttempts is the number of AppendDiff tries (including the first).
	DefaultPersistMaxAttempts = 4
	// DefaultPersistBaseDelay is the first backoff; doubles each retry.
	DefaultPersistBaseDelay = 50 * time.Millisecond
)

// AppendDiffWithRetry calls store.AppendDiff with bounded exponential backoff.
// Identical-replay success from Phase 1 AppendDiff ends the loop immediately.
// ErrDiffFork is returned without retry. On exhaustion wraps ErrPersistExhausted.
//
// result label on devshard_diff_persist_retry_total:
//   - "success" — succeeded after at least one failed attempt
//   - "exhausted" — all attempts failed
func AppendDiffWithRetry(ctx context.Context, store Storage, escrowID string, rec types.DiffRecord) error {
	return AppendDiffWithRetryUnlocked(ctx, store, escrowID, rec, nil, nil)
}

// AppendDiffWithRetryUnlocked is AppendDiffWithRetry with optional lock/unlock
// callbacks invoked around each backoff sleep, letting a caller release a mutex
// while it waits (the host holds h.mu across apply, so it unlocks during
// backoff; the gateway holds s.mu the whole time and passes nil). unlock is
// called before sleeping and lock after — including on the ctx-cancel path, so
// the caller always regains the lock before this returns. Both may be nil.
func AppendDiffWithRetryUnlocked(ctx context.Context, store Storage, escrowID string, rec types.DiffRecord, lock, unlock func()) error {
	if store == nil {
		return fmt.Errorf("AppendDiffWithRetry: nil store")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var last error
	for attempt := 1; attempt <= DefaultPersistMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		last = store.AppendDiff(escrowID, rec)
		if last == nil {
			if attempt > 1 {
				observability.IncDiffPersistRetry("success")
			}
			return nil
		}
		if errors.Is(last, ErrDiffFork) {
			return last
		}
		if attempt == DefaultPersistMaxAttempts {
			break
		}
		delay := DefaultPersistBaseDelay * time.Duration(1<<(attempt-1))
		timer := time.NewTimer(delay)
		if unlock != nil {
			unlock()
		}
		select {
		case <-ctx.Done():
			timer.Stop()
			if lock != nil {
				lock()
			}
			return ctx.Err()
		case <-timer.C:
		}
		if lock != nil {
			lock()
		}
	}
	observability.IncDiffPersistRetry("exhausted")
	return fmt.Errorf("%w: %w", ErrPersistExhausted, last)
}
