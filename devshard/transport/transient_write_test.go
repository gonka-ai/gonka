package transport

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"testing"
)

// Anything the peer answered is its verdict, so only an unanswered request may be retried.
func TestOnlyAnUnansweredRequestIsTransient(t *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
		want bool
	}{
		{"a pooled connection the peer had closed", fmt.Errorf("write tcp: %w", net.ErrClosed), true},
		{"a connection reset mid-write", fmt.Errorf("write tcp: %w", syscall.ECONNRESET), true},
		{"a broken pipe", fmt.Errorf("write tcp: %w", syscall.EPIPE), true},
		{"nothing listening", fmt.Errorf("dial tcp: %w", syscall.ECONNREFUSED), true},
		{"the peer rejected the vote", &UpstreamStatusError{StatusCode: http.StatusConflict, Body: "version conflict"}, false},
		{"the peer failed on it", &UpstreamStatusError{StatusCode: http.StatusInternalServerError, Body: "expected started, got 2"}, false},
		{"the peer refused the stamp", &UpstreamStatusError{StatusCode: http.StatusUnauthorized, Body: "timestamp drift"}, false},
		{"an unrelated failure", errors.New("boom"), false},
		{"no failure", nil, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := IsTransientWriteError(testCase.err); got != testCase.want {
				t.Fatalf("IsTransientWriteError(%v) = %v, want %v", testCase.err, got, testCase.want)
			}
		})
	}
}
