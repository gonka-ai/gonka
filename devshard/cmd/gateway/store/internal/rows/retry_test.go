package rows

import (
	"context"
	"errors"
	"testing"
	"time"
)

var errTransient = errors.New("transient")

func alwaysRetryable() Retrier {
	return Retrier{Retryable: func(error) bool { return true }, Backoff: time.Millisecond}
}

// Test flow:
//  1. Build a retrier that calls every error transient, with a short backoff.
//  2. Subtest "succeeds on first try": run an operation that returns nil and assert it is called once.
//  3. Subtest "fails twice then succeeds": assert the operation is called three times and the result is nil.
//  4. Subtest "always fails": assert the operation is called exactly `retryAttempts` times and the last error is returned.
func TestRetrierRunsUntilSuccessOrTheAttemptsAreSpent(t *testing.T) {
	retrier := alwaysRetryable()

	t.Run("succeeds on first try", func(t *testing.T) {
		calls := 0
		err := retrier.Do(context.Background(), func() error {
			calls++
			return nil
		})
		if err != nil || calls != 1 {
			t.Fatalf("Do() = %v after %d calls, want nil after 1", err, calls)
		}
	})

	t.Run("fails twice then succeeds", func(t *testing.T) {
		calls := 0
		err := retrier.Do(context.Background(), func() error {
			calls++
			if calls < 3 {
				return errTransient
			}
			return nil
		})
		if err != nil || calls != 3 {
			t.Fatalf("Do() = %v after %d calls, want nil after 3", err, calls)
		}
	})

	t.Run("always fails", func(t *testing.T) {
		calls := 0
		err := retrier.Do(context.Background(), func() error {
			calls++
			return errTransient
		})
		if !errors.Is(err, errTransient) || calls != retryAttempts {
			t.Fatalf("Do() = %v after %d calls, want the last error after %d", err, calls, retryAttempts)
		}
	})
}

// Test flow:
//  1. Build a retrier with a backoff of an hour and an operation that cancels its own context and fails.
//  2. Assert `Do` returns `context.Canceled` from the wait, after one call, without sleeping the backoff.
func TestRetrierStopsWaitingWhenTheContextIsCancelled(t *testing.T) {
	retrier := Retrier{Retryable: func(error) bool { return true }, Backoff: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := retrier.Do(ctx, func() error {
		calls++
		cancel()
		return errTransient
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("Do() = %v after %d calls, want context.Canceled after 1", err, calls)
	}
}

// Test flow:
//  1. Cancel the context before calling `Do`.
//  2. Assert `Do` returns `context.Canceled` and the operation is never called.
func TestRetrierWithACancelledContextNeverCallsTheOperation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := alwaysRetryable().Do(ctx, func() error {
		calls++
		return nil
	})
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("Do() = %v after %d calls, want context.Canceled after 0", err, calls)
	}
}

// Test flow:
//  1. Build a retrier that calls no error transient.
//  2. Assert `Do` returns the first error immediately after one call.
func TestRetrierReturnsAPermanentErrorImmediately(t *testing.T) {
	retrier := Retrier{Retryable: func(error) bool { return false }, Backoff: time.Hour}
	calls := 0
	err := retrier.Do(context.Background(), func() error {
		calls++
		return errTransient
	})
	if !errors.Is(err, errTransient) || calls != 1 {
		t.Fatalf("Do() = %v after %d calls, want the error after 1", err, calls)
	}
}
