package transport

import (
	"context"
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
	require.True(t, IsUndeclaredVersionError("version v2 is not present in the governance routing catalog"))
	require.True(t, IsUndeclaredVersionError("version v5 is not declared in VERSIOND_VERSIONS on this router"))
	require.False(t, IsUndeclaredVersionError("nginx limit"))
	require.False(t, IsUndeclaredVersionError(""))
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
}

func TestHTTPClient_NonInferenceDoesNotObserveCatalog503(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	admission := &stubAdmissionController{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	require.True(t, IsUndeclaredVersionError(upstream.Body))
	require.Empty(t, admission.observed, "catalog 503 must not be reported as a participant fault")
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
