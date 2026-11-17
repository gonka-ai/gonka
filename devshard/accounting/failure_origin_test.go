package accounting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFailureOriginFromDetail_HostResponseFamily(t *testing.T) {
	for _, detail := range []string{
		"empty_stream",
		"error_stream",
		"http_503",
		"not_finished",
	} {
		require.Equal(t, FailureHostResponse, FailureOriginFromDetail(detail), detail)
	}
	// "sse_truncated" is not a named reason and does not contain "stream", so it
	// stays transport_unknown, the same label as devshard-0.2.x-v6. Naming it
	// would also change TimeoutReasonFromString.
	require.Equal(t, FailureTransportUnknown, FailureOriginFromDetail("sse_truncated"))
	require.Equal(t, TimeoutReasonUnknown, TimeoutReasonFromString(TimeoutVoteCollectionFailed, "sse_truncated"))
	require.Empty(t, TimeoutReasonFromString(TimeoutApplied, "sse_truncated"))
	require.Equal(t, FailureTransportUnknown, FailureOriginFromDetail("eof_transport"))
	require.Equal(t, FailureClient, FailureOriginFromDetail("client_cancelled"))
}
