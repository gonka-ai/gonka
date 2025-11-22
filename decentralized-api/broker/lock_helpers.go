package broker

import (
	"decentralized-api/logging"
	"errors"
	"fmt"
	"net/http"

	"github.com/productscience/inference/x/inference/types"
)

// ActionErrorKind classifies action failures taken under a node lock
type ActionErrorKind int

const (
	// ActionErrorTransport indicates a transport-level failure (e.g. POST could not connect/timeout)
	ActionErrorTransport ActionErrorKind = iota
	// ActionErrorApplication indicates an application-level failure that should not be retried by default
	ActionErrorApplication
)

func (k ActionErrorKind) String() string {
	switch k {
	case ActionErrorTransport:
		return "transport"
	case ActionErrorApplication:
		return "application"
	default:
		return "unknown"
	}
}

// ActionError wraps an underlying error with a classification
type ActionError struct {
	Kind ActionErrorKind
	Err  error
}

func (e *ActionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Err.Error()
}

func (e *ActionError) Unwrap() error { return e.Err }

func NewTransportActionError(err error) *ActionError {
	if err == nil {
		err = errors.New("transport error")
	}
	return &ActionError{Kind: ActionErrorTransport, Err: err}
}

func NewApplicationActionError(err error) *ActionError {
	if err == nil {
		err = errors.New("application error")
	}
	return &ActionError{Kind: ActionErrorApplication, Err: err}
}

// DoWithLockedNodeHTTPRetry is a convenience helper for HTTP calls under a node lock.
// It centralizes retry and status re-check logic:
// - Transport errors (no HTTP response) trigger status re-check, node skip and retry.
// - HTTP 5xx responses trigger status re-check, node skip and retry.
// - HTTP 4xx responses are returned as-is without retry.
// - 2xx responses are returned.
func DoWithLockedNodeHTTPRetry(
	b *Broker,
	model string,
	skipNodeIDs []string,
	maxAttempts int,
	doPost func(node *Node) (*http.Response, *ActionError),
) (*http.Response, error) {
	var zero *http.Response
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	skip := make(map[string]struct{}, len(skipNodeIDs))
	orderedSkip := make([]string, 0, len(skipNodeIDs))
	for _, id := range skipNodeIDs {
		if id != "" {
			if _, seen := skip[id]; !seen {
				skip[id] = struct{}{}
				orderedSkip = append(orderedSkip, id)
			}
		}
	}

	var lastErr error
	attempts := 0

	logging.Info("HTTP retry helper: starting inference request", types.Inferences,
		"model", model,
		"max_attempts", maxAttempts,
		"initial_skip_count", len(orderedSkip))

	for attempts < maxAttempts {
		attempts++

		nodeChan := make(chan *Node, 2)
		if err := b.QueueMessage(LockAvailableNode{Model: model, Response: nodeChan, SkipNodeIDs: orderedSkip}); err != nil {
			logging.Info("HTTP retry helper: failed to queue LockAvailableNode", types.Inferences,
				"attempt", attempts,
				"error", err)
			return zero, err
		}
		node := <-nodeChan
		if node == nil {
			if lastErr != nil {
				logging.Info("HTTP retry helper: no node available, returning last error", types.Inferences,
					"attempt", attempts,
					"error", lastErr)
				return zero, lastErr
			}
			logging.Info("HTTP retry helper: no nodes available", types.Inferences,
				"attempt", attempts)
			return zero, ErrNoNodesAvailable
		}

		logging.Info("HTTP retry helper: acquired node lock", types.Inferences,
			"attempt", attempts,
			"node_id", node.Id)

		resp, aerr := doPost(node)

		// Decide outcome and retry policy
		retry := false
		triggerRecheck := false

		if aerr != nil {
			if aerr.Kind == ActionErrorTransport {
				// Transport error: retry and recheck
				retry = true
				triggerRecheck = true
				lastErr = fmt.Errorf("node %s transport failure: %w", node.Id, aerr)
				logging.Info("HTTP retry helper: transport error from node", types.Inferences,
					"attempt", attempts,
					"node_id", node.Id,
					"error_kind", aerr.Kind.String(),
					"retry", retry,
					"recheck", triggerRecheck,
					"error", aerr.Err)
			} else {
				// Application error: do not retry
				retry = false
				triggerRecheck = false
				lastErr = aerr
				logging.Info("HTTP retry helper: application error from node (no retry)", types.Inferences,
					"attempt", attempts,
					"node_id", node.Id,
					"error_kind", aerr.Kind.String(),
					"retry", retry,
					"recheck", triggerRecheck,
					"error", aerr.Err)
			}
		} else if resp != nil {
			if resp.StatusCode >= 500 {
				// Server error: retry and recheck
				retry = true
				triggerRecheck = true
				lastErr = fmt.Errorf("node %s server error: status=%d", node.Id, resp.StatusCode)
				logging.Info("HTTP retry helper: received 5xx from node", types.Inferences,
					"attempt", attempts,
					"node_id", node.Id,
					"http_status", resp.StatusCode,
					"retry", retry,
					"recheck", triggerRecheck)
			} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				// 4xx or other non-success (non-retryable)
				retry = false
				triggerRecheck = false
				logging.Info("HTTP retry helper: received non-success non-retryable status from node", types.Inferences,
					"attempt", attempts,
					"node_id", node.Id,
					"http_status", resp.StatusCode,
					"retry", retry,
					"recheck", triggerRecheck)
			} else {
				// Success path
				logging.Info("HTTP retry helper: received success from node", types.Inferences,
					"attempt", attempts,
					"node_id", node.Id,
					"http_status", resp.StatusCode)
			}
		} else {
			logging.Info("HTTP retry helper: no response and no error from node", types.Inferences,
				"attempt", attempts,
				"node_id", node.Id,
				"retry", retry,
				"recheck", triggerRecheck)
		}

		// Release lock with outcome immediately
		var outcome InferenceResult
		if aerr == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			outcome = InferenceSuccess{}
		} else {
			// Compose a concise message
			msg := ""
			if aerr != nil {
				msg = aerr.Error()
			} else if resp != nil {
				msg = fmt.Sprintf("http status %d", resp.StatusCode)
			} else {
				msg = "unknown error"
			}
			outcome = InferenceError{Message: msg}
		}
		_ = b.QueueMessage(ReleaseNode{NodeId: node.Id, Outcome: outcome, Response: make(chan bool, 2)})

		if retry {
			if triggerRecheck {
				b.TriggerStatusQuery(false)
			}
			if resp != nil && resp.Body != nil {
				// Ensure we don't leak the body before retrying
				_ = resp.Body.Close()
			}
			if _, seen := skip[node.Id]; !seen {
				skip[node.Id] = struct{}{}
				orderedSkip = append(orderedSkip, node.Id)
			}
			logging.Info("HTTP retry helper: retrying with next node", types.Inferences,
				"attempt", attempts,
				"node_id", node.Id,
				"next_attempt", attempts+1,
				"max_attempts", maxAttempts,
				"skip_count", len(orderedSkip),
				"recheck_triggered", triggerRecheck)
			// Continue to next attempt
			continue
		}

		// No retry: return
		if aerr != nil {
			logging.Info("HTTP retry helper: returning application error without retry", types.Inferences,
				"attempt", attempts,
				"node_id", node.Id,
				"error_kind", aerr.Kind.String(),
				"error", aerr.Err)
			return zero, aerr
		}
		logging.Info("HTTP retry helper: returning response without retry", types.Inferences,
			"attempt", attempts,
			"node_id", node.Id,
			"http_status", resp.StatusCode)
		return resp, nil
	}

	if lastErr != nil {
		logging.Info("HTTP retry helper: exhausted attempts, returning last error", types.Inferences,
			"max_attempts", maxAttempts,
			"error", lastErr)
		return zero, lastErr
	}
	logging.Info("HTTP retry helper: exhausted attempts, no nodes available", types.Inferences,
		"max_attempts", maxAttempts)
	return zero, ErrNoNodesAvailable
}
