package observability_test

import (
	"testing"

	"devshard/observability"

	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Filter a header set holding allowlisted headers, a lowercase allowlisted header and two headers off the allowlist.
// 2. Check the allowlisted ones are kept under their canonical names and the others are dropped.
func TestFilterResponseHeaders(t *testing.T) {
	kept := observability.FilterResponseHeaders(map[string][]string{
		"Content-Type":                  {"application/json"},
		"X-Devshard-Signature":          {"secret"},
		"X-Request-Id":                  {"req-1"},
		"Authorization":                 {"Bearer x"},
		"x-envoy-upstream-service-time": {"42"},
	})
	require.Equal(t, "application/json", kept["Content-Type"])
	require.Equal(t, "req-1", kept["X-Request-Id"])
	require.Equal(t, "42", kept["X-Envoy-Upstream-Service-Time"])
	require.NotContains(t, kept, "X-Devshard-Signature")
	require.NotContains(t, kept, "Authorization")
}
