package validation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	devshardlogging "devshard/logging"
	"devshard/observability"

	"github.com/stretchr/testify/require"
)

func TestFetchPayloadsHTTPPropagatesRequestID(t *testing.T) {
	requestID := "req-payload-test"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, requestID, r.Header.Get(observability.RequestIDHeader))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"inference_id":"42","prompt_payload":"cHJvbXB0","response_payload":"cmVzcA==","executor_signature":"sig"}`))
	}))
	t.Cleanup(server.Close)

	ctx := devshardlogging.WithRequestID(context.Background(), requestID)
	payloads, err := FetchPayloadsHTTP(ctx, server.Client(), server.URL, "validator", 1, 2, "sig")

	require.NoError(t, err)
	require.Equal(t, "42", payloads.InferenceId)
}
