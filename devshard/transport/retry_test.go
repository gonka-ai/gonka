package transport

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
)

func TestIsUndeclaredVersionError(t *testing.T) {
	const catalogBody = "version v2 is not present in the governance routing catalog"
	const staticBody = "version v5 is not declared in VERSIOND_VERSIONS on this router"

	require.True(t, IsUndeclaredVersionError(http.StatusServiceUnavailable, catalogBody, ""))
	require.True(t, IsUndeclaredVersionError(http.StatusServiceUnavailable, staticBody, ""))
	require.True(t, IsUndeclaredVersionError(http.StatusServiceUnavailable, "", DevshardErrorUndeclaredVersion))
	require.True(t, IsUndeclaredVersionError(http.StatusServiceUnavailable, "unrelated", "UNDECLARED_VERSION"),
		"header match is case-insensitive")
	require.False(t, IsUndeclaredVersionError(http.StatusNotFound, catalogBody, ""))
	require.False(t, IsUndeclaredVersionError(http.StatusInternalServerError, catalogBody, DevshardErrorUndeclaredVersion),
		"a host must not spoof catalog immunity on a non-503")
	require.False(t, IsUndeclaredVersionError(http.StatusServiceUnavailable, "nginx limit", ""))
	require.False(t, IsUndeclaredVersionError(http.StatusServiceUnavailable, "", DevshardErrorRequestsDisabled))
	require.False(t, IsUndeclaredVersionError(http.StatusServiceUnavailable, "", ""))
}

func TestSkipCatalogQuarantine(t *testing.T) {
	require.True(t, SkipCatalogQuarantine("/sessions/1/height-sync", http.StatusServiceUnavailable, DevshardErrorUndeclaredVersion))
	require.True(t, SkipCatalogQuarantine("/sessions/1/height-sync", http.StatusServiceUnavailable, "UNDECLARED_VERSION"),
		"router header match is case-insensitive")
	require.False(t, SkipCatalogQuarantine("/sessions/1/chat/completions", http.StatusServiceUnavailable, DevshardErrorUndeclaredVersion),
		"chat 503s always quarantine")
	require.False(t, SkipCatalogQuarantine("/sessions/1/height-sync", http.StatusServiceUnavailable, ""))
	require.False(t, SkipCatalogQuarantine("/sessions/1/height-sync", http.StatusServiceUnavailable, DevshardErrorRequestsDisabled))
	require.False(t, SkipCatalogQuarantine("/sessions/1/height-sync", http.StatusNotFound, DevshardErrorUndeclaredVersion))
}

func TestUndeclaredVersionFromError(t *testing.T) {
	catalog := &UpstreamStatusError{
		StatusCode:    http.StatusServiceUnavailable,
		Body:          "version v2 is not present in the governance routing catalog",
		DevshardError: DevshardErrorUndeclaredVersion,
	}
	require.Equal(t, catalog, UndeclaredVersionFromError(fmt.Errorf("send: %w", catalog)))
	routerOnly := &UpstreamStatusError{
		StatusCode:  http.StatusServiceUnavailable,
		RouterError: DevshardErrorUndeclaredVersion,
	}
	require.Equal(t, routerOnly, UndeclaredVersionFromError(routerOnly))
	require.Nil(t, UndeclaredVersionFromError(&UpstreamStatusError{
		StatusCode: http.StatusServiceUnavailable,
		Body:       "nginx limit",
	}))
	require.Nil(t, UndeclaredVersionFromError(fmt.Errorf("dial: timeout")))
}

func TestIsRetryableNonInference(t *testing.T) {
	catalog := &UpstreamStatusError{
		StatusCode: http.StatusServiceUnavailable,
		Body:       "version v2 is not present in the governance routing catalog",
	}
	require.True(t, IsRetryableNonInference(catalog))
	require.True(t, IsRetryableNonInference(&UpstreamStatusError{StatusCode: http.StatusTooManyRequests}))
	require.True(t, IsRetryableNonInference(&UpstreamStatusError{StatusCode: http.StatusServiceUnavailable, Body: "busy"}))
	require.False(t, IsRetryableNonInference(nil))
	require.False(t, IsRetryableNonInference(context.Canceled))
	require.False(t, IsRetryableNonInference(context.DeadlineExceeded),
		"deadline expiry is the caller's stop signal, not a host miss to retry")
	require.False(t, IsRetryableNonInference(&UpstreamStatusError{StatusCode: http.StatusNotFound}))
	require.False(t, IsRetryableNonInference(&UpstreamStatusError{
		StatusCode: http.StatusNotFound,
		Body:       "version v2 is not present in the governance routing catalog",
	}), "catalog phrase on a non-503 is not retryable")
}

func TestHTTPClient_NonInferenceRetries503ThenObservesOnce(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	admission := &stubAdmissionController{}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n < 3 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	client := NewHTTPClient(server.URL, "escrow-1", signer, ClientConfig{
		QueryTimeout:   5 * time.Second,
		ParticipantKey: "shared-host",
		Admission:      admission,
		RoutePrefix:    "/",
	})

	err := client.post(context.Background(), "/sessions/escrow-1/height-sync", 5*time.Second, struct{}{}, &struct {
		OK bool `json:"ok"`
	}{})
	require.NoError(t, err)
	require.Equal(t, int32(3), hits.Load())
	require.Len(t, admission.observed, 1)
	require.Contains(t, admission.observed[0], ":200")
	require.Len(t, admission.calls, 1, "one logical request must consume one admission slot, not one per retry")
}

func TestHTTPClient_LocalAdmissionRejectionIsNotAHostFault(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	admission := &stubAdmissionController{err: fmt.Errorf("participant request budget exhausted")}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	client := NewHTTPClient(server.URL, "escrow-1", signer, ClientConfig{
		QueryTimeout:   5 * time.Second,
		ParticipantKey: "shared-host",
		Admission:      admission,
		RoutePrefix:    "/",
	})

	err := client.post(context.Background(), "/sessions/escrow-1/height-sync", 5*time.Second, struct{}{}, &struct{}{})
	require.Error(t, err)
	require.Zero(t, hits.Load(), "a rejected request must never reach the host")
	require.Empty(t, admission.observed,
		"our own limiter decision must not be reported as a host transport failure")
}

func TestHTTPClient_NonInferenceDoesNotObserveCatalog503(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	admission := &stubAdmissionController{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderDevshardRouterError, DevshardErrorUndeclaredVersion)
		http.Error(w, "version v2 is not present in the governance routing catalog", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	client := NewHTTPClient(server.URL, "escrow-1", signer, ClientConfig{
		QueryTimeout:   400 * time.Millisecond,
		ParticipantKey: "shared-host",
		Admission:      admission,
		RoutePrefix:    "/",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	err := client.post(ctx, "/sessions/escrow-1/height-sync", 400*time.Millisecond, struct{}{}, nil)
	require.Error(t, err)
	var upstream *UpstreamStatusError
	require.ErrorAs(t, err, &upstream)
	require.Equal(t, http.StatusServiceUnavailable, upstream.StatusCode)
	require.Equal(t, DevshardErrorUndeclaredVersion, upstream.RouterError)
	require.Empty(t, admission.observed, "router catalog 503 on seed must not be a participant fault")
}

func TestHTTPClient_NonInferenceObservesHostSpoofedUndeclaredHeader(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	admission := &stubAdmissionController{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderDevshardError, DevshardErrorUndeclaredVersion)
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	client := NewHTTPClient(server.URL, "escrow-1", signer, ClientConfig{
		QueryTimeout:   400 * time.Millisecond,
		ParticipantKey: "shared-host",
		Admission:      admission,
		RoutePrefix:    "/",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	err := client.post(ctx, "/sessions/escrow-1/height-sync", 400*time.Millisecond, struct{}{}, nil)
	require.Error(t, err)
	require.NotEmpty(t, admission.observed,
		"X-Devshard-Error on seed is host-spoofable and must be a participant fault")
}

func TestHTTPClient_InferenceObservesRouterUndeclaredHeader(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	admission := &stubAdmissionController{}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set(HeaderDevshardRouterError, DevshardErrorUndeclaredVersion)
		http.Error(w, "version v2 is not present in the governance routing catalog", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	client := NewHTTPClient(server.URL, "escrow-1", signer, ClientConfig{
		InferenceTimeout: DefaultClientConfig().InferenceTimeout,
		GossipTimeout:    DefaultClientConfig().GossipTimeout,
		VerifyTimeout:    DefaultClientConfig().VerifyTimeout,
		QueryTimeout:     DefaultClientConfig().QueryTimeout,
		ParticipantKey:   "shared-host",
		Admission:        admission,
	})

	_, err := client.Send(context.Background(), host.HostRequest{
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   testutil.TestMaxTokens,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.Error(t, err)
	require.Equal(t, int32(1), hits.Load())
	require.NotEmpty(t, admission.observed, "chat 503s always quarantine, even with X-Devshard-Router-Error")
}

func TestHTTPClient_CatalogPhraseOn404IsObserved(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	admission := &stubAdmissionController{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "version v2 is not present in the governance routing catalog", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	client := NewHTTPClient(server.URL, "escrow-1", signer, ClientConfig{
		QueryTimeout:   400 * time.Millisecond,
		ParticipantKey: "shared-host",
		Admission:      admission,
		RoutePrefix:    "/",
	})

	err := client.post(context.Background(), "/sessions/escrow-1/height-sync", 400*time.Millisecond, struct{}{}, nil)
	require.Error(t, err)
	require.NotEmpty(t, admission.observed, "catalog phrase on a non-503 is a host fault")
}

func TestHTTPClient_Inference503IsNotRetried(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	admission := &stubAdmissionController{}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "nginx limit", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	client := NewHTTPClient(server.URL, "escrow-1", signer, ClientConfig{
		InferenceTimeout: DefaultClientConfig().InferenceTimeout,
		GossipTimeout:    DefaultClientConfig().GossipTimeout,
		VerifyTimeout:    DefaultClientConfig().VerifyTimeout,
		QueryTimeout:     DefaultClientConfig().QueryTimeout,
		ParticipantKey:   "shared-host",
		Admission:        admission,
	})

	_, err := client.Send(context.Background(), host.HostRequest{
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   testutil.TestMaxTokens,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.Error(t, err)
	require.Equal(t, int32(1), hits.Load())
	require.Len(t, admission.observed, 1)
	require.Contains(t, admission.observed[0], ":503")
}
