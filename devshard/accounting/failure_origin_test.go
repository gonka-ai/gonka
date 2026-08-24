package accounting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFailureOriginFromDetail_HostResponseFamily(t *testing.T) {
	for _, detail := range []string{
		"empty_stream",
		"error_stream",
		"sse_truncated", // must not rely on Contains("stream") — "sse_truncated" lacks that substring
		"http_503",
		"not_finished",
	} {
		require.Equal(t, FailureHostResponse, FailureOriginFromDetail(detail), detail)
	}
	require.Equal(t, FailureTransportUnknown, FailureOriginFromDetail("eof_transport"))
	require.Equal(t, FailureClient, FailureOriginFromDetail("client_cancelled"))
}
