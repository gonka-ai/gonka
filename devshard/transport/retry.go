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

// IsUndeclaredVersionError reports a versiond-router catalog miss. The
// participant never saw the request; it is not a host throttle.
func IsUndeclaredVersionError(body string) bool {
	b := strings.ToLower(body)
	return strings.Contains(b, "not present in the governance routing catalog") ||
		strings.Contains(b, "not declared in versiond_versions")
}

func isInferencePath(path string) bool {
	return strings.Contains(path, "/chat/completions")
}

func isContextFinished(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func isRetryableNonInference(err error) bool {
	if err == nil {
		return false
	}
	if isContextFinished(err) {
		return false
	}
	var status *UpstreamStatusError
	if errors.As(err, &status) {
		if IsUndeclaredVersionError(status.Body) {
			return true
		}
		return status.StatusCode == http.StatusTooManyRequests ||
			status.StatusCode == http.StatusServiceUnavailable
	}
	return IsTransientWriteError(err)
}

func shouldObserveUpstreamStatus(statusCode int, body string) bool {
	if IsUndeclaredVersionError(body) {
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
