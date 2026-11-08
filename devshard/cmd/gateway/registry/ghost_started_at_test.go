package registry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// Test flow:
//  1. Build a nonce stream whose clock is fixed at a burned-at instant.
//  2. Read the stream's ghost params.
//  3. Assert StartedAt equals the burned-at instant in Unix seconds.
//  4. Assert that once the default refusal timeout has elapsed, nowSeconds-StartedAt meets or exceeds the timeout, so a verifier can accept the refusal deadline.
func TestABurnedNonceCanReachItsRefusalDeadline(t *testing.T) {
	burnedAt := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	stream := nonceStream{model: "test-model", now: func() time.Time { return burnedAt }}

	params := stream.ghostParams()

	require.Equal(t, burnedAt.Unix(), params.StartedAt)
	waited := burnedAt.Add(types.DefaultRefusalTimeoutSeconds*time.Second + time.Second).Unix()
	require.GreaterOrEqual(t, waited-params.StartedAt, int64(types.DefaultRefusalTimeoutSeconds),
		"a verifier must be able to accept the timeout once the refusal window has passed")
}
