package inference

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"common/httpguard"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests in this package serve executors from httptest on loopback, which the
// dial-time SSRF guard rejects; production leaves it on.
func TestMain(m *testing.M) {
	httpguard.SetAllowPrivate(true)
	os.Exit(m.Run())
}

// An executor InferenceUrl whose host resolves to a private address must not be
// dialed by the validator's payload fetch.
func TestPayloadFetchClient_BlocksPrivateDial(t *testing.T) {
	prev := payloadFetchRetryBackoff
	payloadFetchRetryBackoff = 0
	t.Cleanup(func() { payloadFetchRetryBackoff = prev })

	var hits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(internal.Close)

	httpguard.SetAllowPrivate(false)
	t.Cleanup(func() { httpguard.SetAllowPrivate(true) })

	// "localhost" is a hostname, so the registration gate's literal-IP check
	// alone would not stop it; only the dial-time guard does.
	url := "http://localhost:" + internal.URL[len("http://127.0.0.1:"):]
	for _, u := range []string{internal.URL, url} {
		_, err := fetchPayloadsHTTPWithRetry(context.Background(), newPayloadFetchClient(), u, "val", 1, 10, "sig", 0)
		require.Error(t, err, u)
		assert.Contains(t, err.Error(), "ssrf guard", u)
	}
	assert.Equal(t, int32(0), hits.Load())
}
