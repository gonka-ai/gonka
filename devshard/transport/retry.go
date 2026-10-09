package transport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"devshard/transport/rpcpb/rpcpbconnect"
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

// IsEscrowLookupLimited is a 429 from a host (or its versiond) whose
// unknown-escrow lookup budget refused a first bind. The budget refills over
// a minute, so the call is not retried inside the 5 s non-inference budget.
func IsEscrowLookupLimited(statusCode int, devshardError string) bool {
	return statusCode == http.StatusTooManyRequests &&
		strings.EqualFold(strings.TrimSpace(devshardError), DevshardErrorEscrowLookupLimited)
}

func isEscrowLookupLimitedError(err error) bool {
	var status *UpstreamStatusError
	if errors.As(err, &status) {
		return IsEscrowLookupLimited(status.StatusCode, status.DevshardError)
	}
	var ce *connect.Error
	if errors.As(err, &ce) && ce.Code() == connect.CodeResourceExhausted {
		return IsEscrowLookupLimited(http.StatusTooManyRequests, ce.Meta().Get(HeaderDevshardError))
	}
	return false
}

func isInferencePath(path string) bool {
	return strings.Contains(path, "/chat/completions")
}

func isContextFinished(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// IsRetryableNonInference reports a 429/503, Connect Unavailable /
// ResourceExhausted, or transient dial that the non-inference retry loop
// (and the height-sync seed) should retry. Connect Unauthenticated is not
// retryable here; rpcRetry allows one extra attempt. Context cancellation
// and deadline expiry are not retryable. Catalog 503s are retryable
// because they are 503s, not because of their body.
//
// ResourceExhausted from a message-size cap is not retryable: connect-go
// uses that code for WithReadMaxBytes / WithSendMaxBytes, the same code
// Phase 4 uses for real rate limits. See isConnectMessageTooLarge.
// escrow_lookup_limited is not retryable: see IsEscrowLookupLimited.
func IsRetryableNonInference(err error) bool {
	if err == nil {
		return false
	}
	if isContextFinished(err) || isEscrowLookupLimitedError(err) {
		return false
	}
	var status *UpstreamStatusError
	if errors.As(err, &status) {
		return status.StatusCode == http.StatusTooManyRequests ||
			status.StatusCode == http.StatusServiceUnavailable
	}
	switch connect.CodeOf(err) {
	case connect.CodeResourceExhausted:
		return !isConnectMessageTooLarge(err)
	case connect.CodeUnavailable:
		return true
	}
	return IsTransientWriteError(err)
}

// isConnectMessageTooLarge reports a ResourceExhausted that is a configured
// size cap, not a quota. connect-go has no distinct code for oversize
// (always CodeResourceExhausted: "message size %d is larger than configured
// max", "exceeds sendMaxBytes", MaxBytesReader). Remapping those to
// InvalidArgument would lie to callers and metrics: the RPC was well-formed,
// just too big, and gRPC maps 413 the same way. Rate-limit ResourceExhausted
// ("too many sessions", "too many attach attempts", HTTP 429) stays retryable.
func isConnectMessageTooLarge(err error) bool {
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		return false
	}
	msg := err.Error()
	var ce *connect.Error
	if errors.As(err, &ce) {
		msg = ce.Message()
	}
	return strings.Contains(msg, "larger than configured max") ||
		strings.Contains(msg, "exceeds sendMaxBytes") ||
		strings.Contains(msg, "exceeds getURLMaxBytes") ||
		strings.Contains(msg, "http.MaxBytesReader") ||
		strings.Contains(msg, "bandwidth exhausted") ||
		strings.Contains(msg, "attach request too large")
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

// connectAdmissionPath is the path the participant limiter already classifies
// for HTTP. Connect procedure names are not those paths.
func connectAdmissionPath(escrowID, procedure string) string {
	switch procedure {
	case rpcpbconnect.SessionServiceChatProcedure:
		return "/sessions/" + escrowID + "/chat/completions"
	case rpcpbconnect.SessionServiceGetDiffsProcedure:
		return "/sessions/" + escrowID + "/diffs"
	case rpcpbconnect.SessionServiceGetMempoolProcedure:
		return "/sessions/" + escrowID + "/mempool"
	case rpcpbconnect.SessionServiceGetStateProcedure:
		return "/sessions/" + escrowID + "/state"
	case rpcpbconnect.SessionServiceGetSignaturesProcedure:
		return "/sessions/" + escrowID + "/signatures"
	case rpcpbconnect.SessionServiceVerifyTimeoutProcedure:
		return "/sessions/" + escrowID + "/verify-timeout"
	case rpcpbconnect.SessionServiceVerifyErrorMissProcedure:
		return "/sessions/" + escrowID + "/verify-error-miss"
	case rpcpbconnect.SessionServiceChallengeReceiptProcedure:
		return "/sessions/" + escrowID + "/challenge-receipt"
	case rpcpbconnect.SessionServiceSeedHeightSyncProcedure:
		return "/sessions/" + escrowID + "/height-sync"
	case rpcpbconnect.SessionServiceRepairHeightSyncProcedure:
		return "/sessions/" + escrowID + "/heightsync/repair"
	case rpcpbconnect.GossipServiceNonceProcedure:
		return "/sessions/" + escrowID + "/gossip/nonce"
	case rpcpbconnect.GossipServiceTxsProcedure:
		return "/sessions/" + escrowID + "/gossip/txs"
	case rpcpbconnect.PayloadServiceGetPayloadProcedure:
		return "/sessions/" + escrowID + "/payloads"
	default:
		return procedure
	}
}

// connectResultStatus maps a server Connect error back to the HTTP status
// mapInferenceError started from, plus X-Devshard-Error when the server set it.
// Dial, reset, EOF, and deadlines are not application results.
func connectResultStatus(err error) (status int, devshardCode string, ok bool) {
	status, devshardCode, _, ok = ConnectApplicationStatus(err)
	return status, devshardCode, ok
}

// ConnectApplicationStatus reads a *connect.Error the way an HTTP status line
// is read: the Connect code, X-Devshard-Error, and the message. Dial, reset,
// EOF, and a deadline are not application results. Classifiers use this so a
// Connect refusal is the same decision as the HTTP status it replaced.
func ConnectApplicationStatus(err error) (status int, devshardCode, message string, ok bool) {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return 0, "", "", false
	}
	if ce.Code() == connect.CodeDeadlineExceeded || isChatTransportFault(err) {
		return 0, "", "", false
	}
	devshardCode = ce.Meta().Get(HeaderDevshardError)
	message = ce.Message()
	switch ce.Code() {
	case connect.CodeResourceExhausted:
		if isConnectMessageTooLarge(err) {
			return http.StatusRequestEntityTooLarge, devshardCode, message, true
		}
		return http.StatusTooManyRequests, devshardCode, message, true
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable, devshardCode, message, true
	case connect.CodePermissionDenied:
		return http.StatusForbidden, devshardCode, message, true
	case connect.CodeInvalidArgument:
		return http.StatusBadRequest, devshardCode, message, true
	case connect.CodeUnauthenticated:
		return http.StatusUnauthorized, devshardCode, message, true
	case connect.CodeNotFound:
		return http.StatusNotFound, devshardCode, message, true
	case connect.CodeUnimplemented:
		return http.StatusNotImplemented, devshardCode, message, true
	case connect.CodeAlreadyExists:
		return http.StatusConflict, devshardCode, message, true
	case connect.CodeFailedPrecondition:
		return http.StatusPreconditionFailed, devshardCode, message, true
	default:
		return http.StatusInternalServerError, devshardCode, message, true
	}
}

// isChatTransportFault is a call that never received an application status:
// dial, connection reset, HTTP/2 framing, or EOF.
func isChatTransportFault(err error) bool {
	if err == nil || isContextDone(err) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return isRPCH2TransportMiss(err)
}

// connectRetryAfter is the wait advertised on a Connect error (delta-seconds
// or HTTP-date). Zero means the meta was missing or unparsable.
func connectRetryAfter(err error) time.Duration {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return 0
	}
	raw := strings.TrimSpace(ce.Meta().Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if n, convErr := strconv.Atoi(raw); convErr == nil {
		if n <= 0 {
			return 0
		}
		return time.Duration(n) * time.Second
	}
	when, parseErr := http.ParseTime(raw)
	if parseErr != nil {
		return 0
	}
	d := time.Until(when)
	if d < 0 {
		return 0
	}
	return d
}

func isQuotaResourceExhausted(err error) bool {
	return connect.CodeOf(err) == connect.CodeResourceExhausted && !isConnectMessageTooLarge(err)
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
