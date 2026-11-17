package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Test flow:
//  1. Build a test store with a fast retry backoff and a `retryable` function that always says yes, so every error the subtests return drives the retry ladder.
//  2. Subtest "succeeds on first try": run a function that returns nil and assert it is called once.
//  3. Subtest "fails twice then succeeds": run a function that fails twice then succeeds and assert it is called three times.
//  4. Subtest "always fails returns last error after exhausting attempts": run a function that always fails and assert it is called exactly `retryAttempts` times and returns the last error.
//  5. Subtest "cancelled context aborts before exhausting attempts": cancel the context mid-backoff and assert the retry aborts with `context.Canceled` before exhausting attempts.
func TestWithRetry(t *testing.T) {
	testStore := openTestStore(t)
	testStore.retryBackoff = time.Millisecond
	testStore.retryable = func(error) bool { return true }

	t.Run("succeeds on first try", func(t *testing.T) {
		calls := 0
		err := testStore.WithRetry(context.Background(), func() error {
			calls++
			return nil
		})
		if err != nil {
			t.Fatalf("WithRetry() = %v, want nil", err)
		}
		if calls != 1 {
			t.Fatalf("fn called %d times, want 1", calls)
		}
	})

	t.Run("fails twice then succeeds", func(t *testing.T) {
		calls := 0
		err := testStore.WithRetry(context.Background(), func() error {
			calls++
			if calls < 3 {
				return errors.New("transient")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("WithRetry() = %v, want nil", err)
		}
		if calls != 3 {
			t.Fatalf("fn called %d times, want 3", calls)
		}
	})

	t.Run("always fails returns last error after exhausting attempts", func(t *testing.T) {
		calls := 0
		sentinelErr := errors.New("permanent")
		err := testStore.WithRetry(context.Background(), func() error {
			calls++
			return sentinelErr
		})
		if !errors.Is(err, sentinelErr) {
			t.Fatalf("WithRetry() = %v, want %v", err, sentinelErr)
		}
		if calls != retryAttempts {
			t.Fatalf("fn called %d times, want exactly %d", calls, retryAttempts)
		}
	})

	t.Run("cancelled context aborts before exhausting attempts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		go func() {
			time.Sleep(testStore.retryBackoff / 2)
			cancel()
		}()
		err := testStore.WithRetry(ctx, func() error {
			calls++
			return errors.New("transient")
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WithRetry() = %v, want context.Canceled", err)
		}
		if calls >= retryAttempts {
			t.Fatalf("fn called %d times, want fewer than %d (must abort promptly, not exhaust attempts)", calls, retryAttempts)
		}
	})
}

// Test flow:
//  1. Build a test store and cancel the context before calling `WithRetry`.
//  2. Assert `WithRetry` returns `context.Canceled` and the function was never called.
func TestWithRetryAlreadyCancelledNeverCallsFn(t *testing.T) {
	testStore := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	err := testStore.WithRetry(ctx, func() error {
		calls++
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WithRetry() = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("fn called %d times, want 0 (ctx was already cancelled)", calls)
	}
}

// Test flow:
//  1. Build a test store and call `WithRetry` around `SetDevshardActive` for a nonexistent escrow, a permanent error.
//  2. Assert `WithRetry` returns `ErrDevshardNotFound` and the function was called only once, since a missing row answers the same way every time.
func TestWithRetryReturnsAPermanentErrorImmediately(t *testing.T) {
	testStore := openTestStore(t)
	testStore.retryBackoff = time.Millisecond
	calls := 0

	err := testStore.WithRetry(context.Background(), func() error {
		calls++
		return testStore.SetDevshardActive(context.Background(), "no-such-escrow", false)
	})

	if !errors.Is(err, ErrDevshardNotFound) {
		t.Fatalf("WithRetry = %v, want ErrDevshardNotFound", err)
	}
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1: a missing row answers the same way every time", calls)
	}
}
