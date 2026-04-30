package mlnode

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"devshard/logging"
	"devshard/mlnode/gen"
)

// NodeLock acquires and releases ML node locks via node-manager.
type NodeLock interface {
	Acquire(ctx context.Context, model string, excludedNodeIDs []string) (*gen.AcquireMLNodeResponse, error)
	Release(ctx context.Context, lockID string, outcome gen.ReleaseOutcome) error
}

// DoWithNode runs do against an acquired ML node, retrying on transient failures.
//
// On each attempt:
//   - Acquires a node from lock (excluding previously failed nodes by node ID).
//   - Calls do with the node's endpoint.
//   - Releases the lock with the appropriate outcome.
//
// Retry decisions:
//   - Transport error (non-timeout)  → release(TRANSPORT_ERROR), retry with node excluded
//   - HTTP 5xx                       → release(TRANSPORT_ERROR), retry with node excluded
//   - Timeout (ctx deadline)         → release(TIMEOUT), stop
//   - HTTP 4xx                       → release(APPLICATION_ERROR), stop
//   - HTTP 2xx                       → release(SUCCESS), return response
//
// Returns the successful *http.Response, or an error if all attempts are exhausted
// or a non-retryable failure occurs.
func DoWithNode(
	ctx context.Context,
	lock NodeLock,
	model string,
	maxAttempts uint,
	do func(ctx context.Context, endpoint string) (*http.Response, error),
) (*http.Response, error) {
	if maxAttempts == 0 {
		return nil, errors.New("mlnode: maxAttempts must be > 0")
	}
	var excludedNodeIDs []string
	seen := make(map[string]struct{})
	var lastErr error

	for attempt := range maxAttempts {
		lease, err := lock.Acquire(ctx, model, excludedNodeIDs)
		if err != nil {
			return nil, fmt.Errorf("mlnode: acquire node (attempt %d): %w", attempt+1, err)
		}

		resp, outcome, err := runAttempt(ctx, lease.Endpoint, do)

		// Release with a detached context so a cancelled/expired request context
		// does not prevent the server-side lock from being freed.
		if releaseErr := lock.Release(context.WithoutCancel(ctx), lease.LockId, outcome); releaseErr != nil {
			// Non-fatal: the server will evict the lock via TTL, but log for observability.
			logging.Error("mlnode: release lock", "lock_id", lease.LockId, "err", releaseErr)
		}

		if err == nil {
			return resp, nil
		}
		lastErr = err

		// retry only on transport error
		if outcome != gen.ReleaseOutcome_TRANSPORT_ERROR {
			return nil, err
		}

		// Close body before retrying to avoid leaking the connection.
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}

		// Deduplicate excluded node IDs before passing to the next Acquire.
		if _, dup := seen[lease.NodeId]; !dup && lease.NodeId != "" {
			seen[lease.NodeId] = struct{}{}
			excludedNodeIDs = append(excludedNodeIDs, lease.NodeId)
		}
	}

	return nil, fmt.Errorf("mlnode: all %d attempts failed for model %q: %w", maxAttempts, model, lastErr)
}

// runAttempt calls do and classifies the result into a ReleaseOutcome + retry decision.
func runAttempt(
	ctx context.Context,
	endpoint string,
	do func(ctx context.Context, endpoint string) (*http.Response, error),
) (resp *http.Response, outcome gen.ReleaseOutcome, err error) {
	resp, err = do(ctx, endpoint)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			logging.Error("mlnode: context error during attempt", "endpoint", endpoint, "err", err)
			return nil, gen.ReleaseOutcome_TIMEOUT, err
		}
		return nil, gen.ReleaseOutcome_TRANSPORT_ERROR, err
	}

	switch {
	case resp.StatusCode >= 500:
		return resp, gen.ReleaseOutcome_TRANSPORT_ERROR, fmt.Errorf("mlnode: node returned HTTP %d", resp.StatusCode)
	case resp.StatusCode >= 400:
		return resp, gen.ReleaseOutcome_APPLICATION_ERROR, fmt.Errorf("mlnode: node returned HTTP %d", resp.StatusCode)
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return resp, gen.ReleaseOutcome_SUCCESS, nil
	default:
		return resp, gen.ReleaseOutcome_APPLICATION_ERROR, fmt.Errorf("mlnode: node returned HTTP %d", resp.StatusCode)
	}
}
