package transport

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Non-inference 429/503 (and transient dial failures) retry with exponential
// delay until this budget. Chat completions are not retried: a host 503 there
// is a live capacity signal.
const (
	nonInferenceRetryBudget  = 5 * time.Second
	nonInferenceRetryInitial = 50 * time.Millisecond
)

// IsUndeclaredVersionError reports a versiond-router catalog miss. Used to
// classify the error for clients and retries. Status must be 503 — a host
// must not look like a catalog miss by putting the phrase in some other body.
//
// Quarantine skipping is NOT this function: see SkipCatalogQuarantine.
func IsUndeclaredVersionError(statusCode int, body, devshardError string) bool {
	if statusCode != http.StatusServiceUnavailable {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(devshardError), DevshardErrorUndeclaredVersion) {
		return true
	}
	b := strings.ToLower(body)
	return strings.Contains(b, "not present in the governance routing catalog") ||
		strings.Contains(b, "not declared in versiond_versions")
}

// SkipCatalogQuarantine is the limiter exemption for a router-generated
// undeclared-version 503. It applies only to non-inference paths (height-sync
// seed, heartbeat, …). /chat/completions always records the 503 as a host
// fault, even when the router stamped X-Devshard-Router-Error: a host must
// not buy inference-path immunity, and chat is one-shot so it is not the
// seed retry loop that produced (0/0).
//
// Only X-Devshard-Router-Error counts. X-Devshard-Error and the body phrase
// are host-spoofable and do not skip quarantine.
func SkipCatalogQuarantine(path string, statusCode int, routerError string) bool {
	if isInferencePath(path) {
		return false
	}
	if statusCode != http.StatusServiceUnavailable {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(routerError), DevshardErrorUndeclaredVersion)
}

// UndeclaredVersionFromError returns the upstream 503 catalog miss wrapped in
// err, or nil if err is not that class of failure.
func UndeclaredVersionFromError(err error) *UpstreamStatusError {
	var status *UpstreamStatusError
	if !errors.As(err, &status) {
		return nil
	}
	if IsUndeclaredVersionError(status.StatusCode, status.Body, status.DevshardError) {
		return status
	}
	if status.StatusCode == http.StatusServiceUnavailable &&
		strings.EqualFold(strings.TrimSpace(status.RouterError), DevshardErrorUndeclaredVersion) {
		return status
	}
	return nil
}

func isInferencePath(path string) bool {
	return strings.Contains(path, "/chat/completions")
}

func isContextFinished(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// IsRetryableNonInference reports a 429/503 or transient dial that the
// non-inference retry loop (and the height-sync seed) should retry. Context
// cancellation and deadline expiry are not retryable: the caller already
// decided to stop. Catalog 503s are retryable because they are 503s, not
// because of their body.
func IsRetryableNonInference(err error) bool {
	if err == nil {
		return false
	}
	if isContextFinished(err) {
		return false
	}
	var status *UpstreamStatusError
	if errors.As(err, &status) {
		return status.StatusCode == http.StatusTooManyRequests ||
			status.StatusCode == http.StatusServiceUnavailable
	}
	return IsTransientWriteError(err)
}

// IsHeightSyncSeedPath reports POST /sessions/:id/height-sync (the cold-start
// seed). Distinct from /heightsync/repair. Seed failures are the gateway's
// own liveness probe and must not feed the participant limiter.
func IsHeightSyncSeedPath(path string) bool {
	return strings.Contains(path, "/height-sync")
}

func shouldObserveUpstreamStatus(path string, statusCode int, _, _, routerError string) bool {
	if IsHeightSyncSeedPath(path) {
		return false
	}
	if SkipCatalogQuarantine(path, statusCode, routerError) {
		return false
	}
	return statusCode > 0
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func nonInferenceRetryDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(nonInferenceRetryBudget)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		return dl
	}
	return deadline
}
