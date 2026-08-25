package inference

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	commonvalidation "common/validation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchPayloadsHTTPWithRetry(t *testing.T) {
	prev := payloadFetchRetryBackoff
	payloadFetchRetryBackoff = 0
	t.Cleanup(func() { payloadFetchRetryBackoff = prev })

	t.Run("500 retries twice", func(t *testing.T) {
		var n atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n.Add(1)
			http.Error(w, "fail", http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		_, err := fetchPayloadsHTTPWithRetry(context.Background(), srv.Client(), srv.URL, "val", 1, 10, "sig")
		require.Error(t, err)
		assert.False(t, errors.Is(err, commonvalidation.ErrPayloadGone))
		assert.Equal(t, int32(payloadFetchAttempts), n.Load())
	})

	t.Run("404 does not retry", func(t *testing.T) {
		var n atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n.Add(1)
			http.NotFound(w, nil)
		}))
		t.Cleanup(srv.Close)

		_, err := fetchPayloadsHTTPWithRetry(context.Background(), srv.Client(), srv.URL, "val", 1, 10, "sig")
		require.ErrorIs(t, err, commonvalidation.ErrPayloadGone)
		assert.Equal(t, int32(1), n.Load())
	})

	t.Run("success on second attempt", func(t *testing.T) {
		var n atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if n.Add(1) == 1 {
				http.Error(w, "fail", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(commonvalidation.PayloadResponse{
				InferenceId: "1",
			})
		}))
		t.Cleanup(srv.Close)

		resp, err := fetchPayloadsHTTPWithRetry(context.Background(), srv.Client(), srv.URL, "val", 1, 10, "sig")
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, int32(2), n.Load())
	})

	t.Run("cancelled context stops immediately", func(t *testing.T) {
		var n atomic.Int32
		ctx, cancel := context.WithCancel(context.Background())
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			n.Add(1)
			cancel()
		}))
		t.Cleanup(srv.Close)

		_, err := fetchPayloadsHTTPWithRetry(ctx, srv.Client(), srv.URL, "val", 1, 10, "sig")
		require.Error(t, err)
		assert.LessOrEqual(t, n.Load(), int32(payloadFetchAttempts))
		assert.GreaterOrEqual(t, n.Load(), int32(1))
	})

	t.Run("already cancelled does not hit executor", func(t *testing.T) {
		var n atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			n.Add(1)
		}))
		t.Cleanup(srv.Close)

		_, err := fetchPayloadsHTTPWithRetry(cancelledCtx(), srv.Client(), srv.URL, "val", 1, 10, "sig")
		require.Error(t, err)
		assert.Equal(t, int32(0), n.Load())
	})
}

func TestFetchPayloadsHTTPWithRetry_BackoffRespectsContext(t *testing.T) {
	prev := payloadFetchRetryBackoff
	payloadFetchRetryBackoff = 30 * time.Second
	t.Cleanup(func() { payloadFetchRetryBackoff = prev })

	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		http.Error(w, "fail", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after the first attempt so the backoff select exits on ctx.Done.
	go func() {
		for n.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()

	start := time.Now()
	_, err := fetchPayloadsHTTPWithRetry(ctx, srv.Client(), srv.URL, "val", 1, 10, "sig")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "must not wait the full backoff")
	assert.Equal(t, int32(1), n.Load())
}
