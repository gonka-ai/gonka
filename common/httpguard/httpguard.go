// Package httpguard provides a dial-time SSRF guard for HTTP clients that
// connect to participant-controlled URLs taken from chain state (devshard peer
// host URLs, executor payload endpoints derived from Participant.InferenceUrl).
//
// The guard hooks net.Dialer.Control, which the stdlib calls after DNS
// resolution and once per candidate IP, with address already "ip:port".
// Checking there vets every real dial target: each dual-stack candidate and
// each redirect hop's fresh dial. That is what defeats DNS rebinding, which a
// resolve-then-connect check cannot -- the gap between the lookup and the
// connect is exactly the attack.
//
// The on-chain registration gate (inference-chain x/inference/utils
// ValidateURLWithSSRFProtection) can only reject literal private IPs: it runs
// inside a stateless ValidateBasic, so it must stay deterministic and cannot
// resolve DNS. This guard is the enforcement point, and it reuses that gate's
// predicate (utils.IsPrivateIP) so both agree on what "private" means.
package httpguard

import (
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/productscience/inference/x/inference/utils"
)

// allowPrivate toggles the guard process-wide. Default false = secure.
//
// It is package-level rather than per-client because devshard's transport cache
// is global and keyed only by baseURL, so a per-client setting could not be
// threaded into an already-cached dialer. DialControl reads it on every dial,
// so the toggle applies to clients constructed before it is set (notably the
// package-level validation.PayloadRetrievalClient).
var allowPrivate atomic.Bool

// SetAllowPrivate configures whether guarded dialers may connect to
// private/internal addresses. Callers wire this once at startup from their
// environment (devshardd reads DEVSHARD_ALLOW_PRIVATE_ADDRESSES). Set true only
// in local dev / docker-compose / e2e, where hosts register docker-internal
// hostnames that resolve to private IPs.
func SetAllowPrivate(allow bool) {
	allowPrivate.Store(allow)
}

// AllowPrivate reports whether the guard is currently disabled.
func AllowPrivate() bool {
	return allowPrivate.Load()
}

// DialControl is a net.Dialer.Control hook that rejects connections to
// private/internal IP addresses unless SetAllowPrivate(true) was called.
func DialControl(_, address string, _ syscall.RawConn) error {
	if allowPrivate.Load() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		// address is already "ip:port" at this point; fail closed.
		return fmt.Errorf("ssrf guard: cannot parse dial address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("ssrf guard: unresolved dial address %q", address)
	}
	if utils.IsPrivateIP(ip) {
		return fmt.Errorf("ssrf guard: blocked dial to private address %s", ip)
	}
	return nil
}

// NewDialer returns a dialer carrying the guard, with the same timeouts the
// stdlib default transport uses.
func NewDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   DialControl,
	}
}

// NewNoRedirectClient builds a guarded *http.Client that also refuses to follow
// 3xx responses, so a public host cannot redirect a fetch to a private target.
// The caller observes the 3xx as a non-200 status. Redirect refusal is defense
// in depth: DialControl already re-checks each hop's dial.
//
// The transport is cloned from http.DefaultTransport so proxy/keep-alive/HTTP2
// behavior matches the stdlib.
func NewNoRedirectClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = NewDialer().DialContext
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
