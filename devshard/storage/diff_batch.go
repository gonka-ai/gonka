package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"devshard/observability"
	"devshard/types"
)

// ErrStorageBusy is returned when a write could not reach the store before the
// caller's deadline: every connection was held, or every attempt hit a
// transient failure. Nothing was applied in memory, so the caller may resend.
var ErrStorageBusy = errors.New("storage busy")

// ErrConnWait marks a failure to obtain a pooled connection. No statement ran.
var ErrConnWait = errors.New("wait for storage connection")

const (
	// DefaultPersistBudget bounds a batch write when the caller has no
	// earlier deadline. It matches the Postgres per-operation budget.
	DefaultPersistBudget = postgresOpTimeout
	// DefaultPersistMaxDelay caps the backoff between batch attempts.
	DefaultPersistMaxDelay = time.Second
)

// DiffBatchWriter writes a run of diffs for one escrow as a single unit.
//
// The batch is all-or-nothing. A nonce that is already durable with identical
// content is a no-op (its signatures are upserted); one with different
// content fails the whole batch with ErrDiffFork. ctx bounds only the wait for
// a connection: once the write has started it runs to completion under the
// store's own operation budget, so a caller that gives up mid-commit never
// leaves a half-applied batch.
type DiffBatchWriter interface {
	AppendDiffBatch(ctx context.Context, escrowID string, recs []types.DiffRecord) error
}

// PersistDiffBatch writes recs through store, retrying until it succeeds, the
// caller cancels, or the deadline passes. The deadline is the caller's, capped
// at DefaultPersistBudget.
//
// Results:
//   - nil: every record is durable.
//   - ErrDiffFork, ErrSessionNotFound: returned as-is, never retried.
//   - ctx.Err() when the caller canceled; nothing new was committed unless a
//     commit raced the cancel, which an identical resend tolerates.
//   - ErrStorageBusy when the deadline passed while waiting for a connection
//     or retrying transient failures.
//   - ErrPersistExhausted after DefaultPersistMaxAttempts unclassified failures.
//
// Stores without DiffBatchWriter fall back to one AppendDiff per record; a
// retry resumes at the first record that failed.
func PersistDiffBatch(ctx context.Context, store Storage, escrowID string, recs []types.DiffRecord) error {
	if store == nil {
		return fmt.Errorf("PersistDiffBatch: nil store")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(recs) == 0 {
		return nil
	}
	deadline := time.Now().Add(DefaultPersistBudget)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	wctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	writer, batched := store.(DiffBatchWriter)
	done := 0
	delay := DefaultPersistBaseDelay
	unclassified := 0
	attempt := 0
	var last error
	for {
		attempt++
		if batched {
			last = writer.AppendDiffBatch(wctx, escrowID, recs)
		} else {
			for done < len(recs) {
				if last = store.AppendDiff(escrowID, recs[done]); last != nil {
					break
				}
				done++
			}
		}
		if last == nil {
			if attempt > 1 {
				observability.IncDiffPersistRetry("success")
			}
			return nil
		}
		if errors.Is(last, ErrDiffFork) || errors.Is(last, ErrSessionNotFound) {
			return last
		}
		if err := ctx.Err(); err != nil && errors.Is(err, context.Canceled) {
			return err
		}
		if !IsTransientStorageErr(last) {
			unclassified++
			if unclassified >= DefaultPersistMaxAttempts {
				observability.IncDiffPersistRetry("exhausted")
				slog.ErrorContext(ctx, "diff_persist_exhausted",
					"escrow_id", escrowID,
					"from", recs[0].Nonce,
					"to", recs[len(recs)-1].Nonce,
					"attempts", attempt,
					"error", last,
				)
				return fmt.Errorf("%w: %w", ErrPersistExhausted, last)
			}
		}
		if err := sleepJittered(wctx, delay); err != nil {
			return persistGiveUp(ctx, escrowID, recs, attempt, last)
		}
		delay *= 2
		if delay > DefaultPersistMaxDelay {
			delay = DefaultPersistMaxDelay
		}
	}
}

func persistGiveUp(ctx context.Context, escrowID string, recs []types.DiffRecord, attempts int, last error) error {
	if err := ctx.Err(); err != nil && errors.Is(err, context.Canceled) {
		return err
	}
	observability.IncDiffPersistRetry("busy")
	slog.WarnContext(ctx, "diff_persist_storage_busy",
		"escrow_id", escrowID,
		"from", recs[0].Nonce,
		"to", recs[len(recs)-1].Nonce,
		"attempts", attempts,
		"error", last,
	)
	return fmt.Errorf("%w: %w", ErrStorageBusy, last)
}

// sleepJittered waits d scaled by a random factor in [0.5, 1.0).
func sleepJittered(ctx context.Context, d time.Duration) error {
	d = d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// IsTransientStorageErr reports failures that a later attempt can clear
// without any change to the request: no connection free, a dropped
// connection, a serialization or deadlock abort, a server-side timeout, an
// admin shutdown, or a busy SQLite writer.
func IsTransientStorageErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrConnWait) || errors.Is(err, ErrStorageIndexRebuilding) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return transientPgCode(pgErr.Code)
	}
	if pgconn.SafeToRetry(err) || pgconn.Timeout(err) {
		return true
	}
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "conn closed") ||
		strings.Contains(msg, "SQLITE_BUSY") ||
		strings.Contains(msg, "database is locked")
}

func transientPgCode(code string) bool {
	if strings.HasPrefix(code, "08") || strings.HasPrefix(code, "53") {
		return true
	}
	switch code {
	case "40001", "40P01", "57014", "57P01", "57P02", "57P03", "55P03":
		return true
	}
	return false
}
