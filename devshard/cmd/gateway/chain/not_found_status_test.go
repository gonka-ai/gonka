package chain

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Test flow:
//  1. Classify a typed NotFound, a node's Unknown carrying the SDK's own "tx not found", a misrouted proxy's 404 surfaced as Unimplemented, and a bare error mentioning not found.
//  2. Assert only the first two read as an absent transaction: anything else is a transport failure that must keep the intent it was checking.
func TestOnlyTheChainsOwnNotFoundReadsAsAnAbsentTransaction(t *testing.T) {
	for name, testCase := range map[string]struct {
		err  error
		want bool
	}{
		"typed not found":            {status.Error(codes.NotFound, "tx not found: ABC"), true},
		"unknown carrying the sdk's": {status.Error(codes.Unknown, "tx not found: ABC"), true},
		"proxy 404":                  {status.Error(codes.Unimplemented, "unexpected HTTP status code received from server: 404 (Not Found)"), false},
		"bare text":                  {errors.New("route not found"), false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := isNotFoundStatus(testCase.err); got != testCase.want {
				t.Fatalf("isNotFoundStatus(%v) = %v, want %v", testCase.err, got, testCase.want)
			}
		})
	}
}
