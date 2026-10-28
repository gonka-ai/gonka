package store

import (
	"context"
	"errors"
	"time"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	retryAttempts         = 10
	defaultRetryBackoff   = 200 * time.Millisecond
	sqlitePrimaryCodeMask = 0xFF
)

// WithRetry retries only a locked database, up to retryAttempts. See README.md, "Writing under a lock".
func (s *Store) WithRetry(ctx context.Context, fn func() error) error {
	var lastErr error
	for attempt := range retryAttempts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if lastErr = fn(); lastErr == nil {
			return nil
		}
		if !s.retryable(lastErr) {
			return lastErr
		}
		if attempt < retryAttempts-1 {
			timer := time.NewTimer(s.retryBackoff)
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

// isLockedError is a field on Store because the driver's error type cannot be constructed outside its own package. See README.md, "Writing under a lock".
func isLockedError(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	// Masked to the primary code, because the driver enables extended codes.
	code := sqliteErr.Code() & sqlitePrimaryCodeMask
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
}
