package rows

import (
	"context"
	"time"
)

const (
	retryAttempts       = 10
	DefaultRetryBackoff = 200 * time.Millisecond
)

// Retrier repeats an operation while the backend calls its error transient. See ../../README.md, "Writing under a lock".
type Retrier struct {
	Retryable func(error) bool
	Backoff   time.Duration
}

// Do runs operation up to retryAttempts times, stopping on success, a permanent error or a done context.
func (r Retrier) Do(ctx context.Context, operation func() error) error {
	var lastErr error
	for attempt := range retryAttempts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if lastErr = operation(); lastErr == nil {
			return nil
		}
		if !r.Retryable(lastErr) {
			return lastErr
		}
		if attempt < retryAttempts-1 {
			timer := time.NewTimer(r.Backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return lastErr
}
