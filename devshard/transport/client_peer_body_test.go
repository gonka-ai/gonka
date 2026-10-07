package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/types"
)

// A group member answering signatures, mempool or verify with an endless
// body must not make the gateway buffer it.
func TestHTTPClient_PeerRepliesAreBounded(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		chunk := []byte(strings.Repeat(" ", 1<<20))
		for i := int64(0); i < 4*MaxJSONResponseBytes/int64(len(chunk)); i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	t.Cleanup(ts.Close)

	cfg := DefaultClientConfig()
	cfg.RoutePrefix = testRoutePrefix
	client := NewHTTPClient(ts.URL, "escrow-1", testutil.MustGenerateKey(t), cfg)
	ctx := context.Background()

	_, err := client.GetSignatures(ctx, 1)
	require.ErrorIs(t, err, ErrResponseBodyTooLarge)

	_, err = client.GetMempool(ctx)
	require.ErrorIs(t, err, ErrResponseBodyTooLarge)

	_, _, _, _, _, err = client.VerifyTimeout(ctx, 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, &host.InferencePayload{}, nil, host.TimeoutArtifacts{})
	require.ErrorIs(t, err, ErrResponseBodyTooLarge)

	_, _, err = client.ChallengeReceipt(ctx, 1, &host.InferencePayload{}, nil)
	require.ErrorIs(t, err, ErrResponseBodyTooLarge)
}
