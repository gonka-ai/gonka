package inference

import (
	"bytes"
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

		_, err := fetchPayloadsHTTPWithRetry(context.Background(), srv.Client(), srv.URL, "val", 1, 10, "sig", 0)
		require.Error(t, err)
		assert.NotErrorIs(t, err, commonvalidation.ErrPayloadGone)
		assert.Equal(t, int32(payloadFetchAttempts), n.Load())
	})

	t.Run("404 does not retry", func(t *testing.T) {
		var n atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n.Add(1)
			http.NotFound(w, nil)
		}))
		t.Cleanup(srv.Close)

		_, err := fetchPayloadsHTTPWithRetry(context.Background(), srv.Client(), srv.URL, "val", 1, 10, "sig", 0)
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

		resp, err := fetchPayloadsHTTPWithRetry(context.Background(), srv.Client(), srv.URL, "val", 1, 10, "sig", 0)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, int32(2), n.Load())
	})

	t.Run("malformed 200 retries then restored response succeeds", func(t *testing.T) {
		var n atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if n.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"inference_id":"1"`))
				return
			}
			_ = json.NewEncoder(w).Encode(commonvalidation.PayloadResponse{
				InferenceId: "1",
			})
		}))
		t.Cleanup(srv.Close)

		resp, err := fetchPayloadsHTTPWithRetry(context.Background(), srv.Client(), srv.URL, "val", 1, 10, "sig", 0)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "1", resp.InferenceId)
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

		_, err := fetchPayloadsHTTPWithRetry(ctx, srv.Client(), srv.URL, "val", 1, 10, "sig", 0)
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

		_, err := fetchPayloadsHTTPWithRetry(cancelledCtx(), srv.Client(), srv.URL, "val", 1, 10, "sig", 0)
		require.Error(t, err)
		assert.Equal(t, int32(0), n.Load())
	})
}

// The executor URL comes from the chain, so a redirect must not be followed:
// it would let a registered executor point the validator at its own network.
func TestFetchPayloadsHTTPWithRetry_DoesNotFollowRedirect(t *testing.T) {
	prev := payloadFetchRetryBackoff
	payloadFetchRetryBackoff = 0
	t.Cleanup(func() { payloadFetchRetryBackoff = prev })

	var internalHits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		internalHits.Add(1)
		_ = json.NewEncoder(w).Encode(commonvalidation.PayloadResponse{InferenceId: "1"})
	}))
	t.Cleanup(internal.Close)

	executor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL, http.StatusFound)
	}))
	t.Cleanup(executor.Close)

	client := newPayloadFetchClient()
	_, err := fetchPayloadsHTTPWithRetry(context.Background(), client, executor.URL, "val", 1, 10, "sig", 0)
	require.Error(t, err)
	assert.Equal(t, int32(0), internalHits.Load())
	// A redirecting executor is a non-200 non-404 response: executor fault.
	assert.NotErrorIs(t, err, commonvalidation.ErrPayloadGone)
	assert.ErrorIs(t, tagExecutorPayloadFault(err), errExecutorPayloadFault)
}

func TestFetchPayloadsHTTPWithRetry_StopsOnTooLarge(t *testing.T) {
	prev := payloadFetchRetryBackoff
	payloadFetchRetryBackoff = 0
	t.Cleanup(func() { payloadFetchRetryBackoff = prev })

	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Never terminate the JSON document: the cap must stop the read.
		w.Write([]byte(`{"prompt_payload":"`))
		flusher, _ := w.(http.Flusher)
		chunk := bytes.Repeat([]byte("A"), 1<<20)
		for written := 0; written <= 3<<20; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)

	_, err := fetchPayloadsHTTPWithRetry(context.Background(), srv.Client(), srv.URL, "val", 1, 10, "sig", 2<<20)
	require.ErrorIs(t, err, commonvalidation.ErrPayloadTooLarge)
	// Retrying costs another full transfer for a deterministic failure.
	assert.Equal(t, int32(1), n.Load())
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
	_, err := fetchPayloadsHTTPWithRetry(ctx, srv.Client(), srv.URL, "val", 1, 10, "sig", 0)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "must not wait the full backoff")
	assert.Equal(t, int32(1), n.Load())
}

func TestPayloadFetchClient_HeaderTimeoutFailsFast(t *testing.T) {
	prevHeader := payloadFetchHeaderTimeout
	prevBackoff := payloadFetchRetryBackoff
	payloadFetchHeaderTimeout = 50 * time.Millisecond
	payloadFetchRetryBackoff = 0
	t.Cleanup(func() {
		payloadFetchHeaderTimeout = prevHeader
		payloadFetchRetryBackoff = prevBackoff
	})

	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		n.Add(1)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	start := time.Now()
	_, err := fetchPayloadsHTTPWithRetry(context.Background(), newPayloadFetchClient(), srv.URL, "val", 1, 10, "sig", 0)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second, "silent executor must fail on TTFB, not the 30s body timeout")
	assert.Equal(t, int32(payloadFetchAttempts), n.Load())
}

// net/http silently ignores ResponseHeaderTimeout on HTTP/2, so an executor
// that negotiated h2 would get the full body timeout to produce headers and the
// TTFB bound would not apply.
func TestPayloadFetchClient_PinsHTTP1SoHeaderTimeoutApplies(t *testing.T) {
	t.Parallel()
	client := newPayloadFetchClient()

	rt, ok := client.Transport.(ttfbRoundTripper)
	require.True(t, ok, "transport must be wrapped for TTFB measurement")
	transport, ok := rt.base.(*http.Transport)
	require.True(t, ok)

	assert.False(t, transport.ForceAttemptHTTP2)
	require.NotNil(t, transport.TLSNextProto, "nil TLSNextProto re-enables h2 negotiation")
	assert.Empty(t, transport.TLSNextProto)
	assert.Equal(t, payloadFetchHeaderTimeout, transport.ResponseHeaderTimeout)
	assert.Equal(t, payloadFetchTimeout, client.Timeout)
}

type stubRoundTripper struct {
	resp *http.Response
	err  error
}

func (s stubRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return s.resp, s.err
}

// TTFB is only recorded for round trips that produced headers. A blackholing
// executor must not contribute samples pinned at the header timeout, since that
// inflates the p99 the timeout is sized from.
func TestTTFBRoundTripper_PropagatesFailureWithoutResponse(t *testing.T) {
	t.Parallel()
	want := errors.New("dial tcp: i/o timeout")
	req, err := http.NewRequest(http.MethodGet, "http://executor.invalid", nil)
	require.NoError(t, err)

	resp, err := ttfbRoundTripper{base: stubRoundTripper{err: want}}.RoundTrip(req)
	require.ErrorIs(t, err, want)
	assert.Nil(t, resp)

	ok := &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}
	resp, err = ttfbRoundTripper{base: stubRoundTripper{resp: ok}}.RoundTrip(req)
	require.NoError(t, err)
	assert.Same(t, ok, resp)
}

func TestPayloadFetchClient_SlowBodyAfterHeadersSucceeds(t *testing.T) {
	prevHeader := payloadFetchHeaderTimeout
	payloadFetchHeaderTimeout = 50 * time.Millisecond
	t.Cleanup(func() { payloadFetchHeaderTimeout = prevHeader })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(150 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(commonvalidation.PayloadResponse{InferenceId: "1"})
	}))
	t.Cleanup(srv.Close)

	resp, err := fetchPayloadsHTTPWithRetry(context.Background(), newPayloadFetchClient(), srv.URL, "val", 1, 10, "sig", 0)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "1", resp.InferenceId)
}
