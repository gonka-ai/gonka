package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"devshard/chainoracle/blocks"
	"devshard/chainoracle/blocks/client"
	"devshard/chainoracle/blocks/observer"
	"devshard/chainoracle/blocks/server"
	"devshard/chainoracle/blocks/verifier"
	"devshard/signing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// genMockValidators returns n generated (signer, address) pairs in the
// shapes needed by the observer and the verifier.
func genMockValidators(t *testing.T, n int) ([]observer.MockValidator, []verifier.Validator) {
	t.Helper()
	mocks := make([]observer.MockValidator, 0, n)
	verifiers := make([]verifier.Validator, 0, n)
	for i := 0; i < n; i++ {
		s, err := signing.GenerateKey()
		require.NoError(t, err)
		addr, err := blocks.AddressBytes(s.PublicKeyBytes())
		require.NoError(t, err)
		mocks = append(mocks, observer.MockValidator{Signer: s, Address: addr, Power: 1})
		verifiers = append(verifiers, verifier.Validator{Address: append([]byte(nil), addr...), Power: 1})
	}
	return mocks, verifiers
}

func newServedMock(t *testing.T) (*httptest.Server, *observer.Mock, *verifier.Verifier, func()) {
	t.Helper()
	mocks, verifiers := genMockValidators(t, 10)
	mock, err := observer.NewMock(observer.MockConfig{
		ChainID:       "gonka-test",
		Validators:    mocks,
		BlockInterval: 100 * time.Millisecond,
		Seed:          17,
		Start:         time.Unix(1_700_000_000, 0).UTC(),
		InitialHeight: 1,
	})
	require.NoError(t, err)

	vs, err := verifier.NewValidatorSet("gonka-test", verifiers)
	require.NoError(t, err)
	v := verifier.New(vs)

	e := echo.New()
	server.Mount(e.Group(""), mock)
	ts := httptest.NewServer(e)
	return ts, mock, v, func() { ts.Close() }
}

func TestClient_LatestAndAt_CacheCoherence(t *testing.T) {
	ts, mock, v, cleanup := newServedMock(t)
	defer cleanup()
	_, err := mock.AdvanceOne()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c, err := client.NewHTTP(ctx, client.HTTPConfig{
		BaseURL:          ts.URL,
		Verifier:         v,
		ResubscribeAfter: 20 * time.Millisecond,
		StaleAfter:       time.Second,
	})
	require.NoError(t, err)
	defer c.Close()

	latest, err := c.Latest(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), latest.Height)

	// Subsequent At(1) must not regress; fetched-or-cached both return
	// the same header.
	h, err := c.At(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, latest.Height, h.Height)
	require.Equal(t, latest.AppHash, h.AppHash)
}

func TestClient_RejectsTamperedHeader(t *testing.T) {
	// A hostile server hands out a header with a tampered AppHash.
	// The client MUST reject it and MUST NOT cache it (when a verifier
	// is pinned).
	mocks, verifiers := genMockValidators(t, 10)

	// Build a valid header and then flip one byte of AppHash.
	src, err := observer.NewMock(observer.MockConfig{
		ChainID:       "gonka-test",
		Validators:    mocks,
		BlockInterval: time.Second,
		Seed:          77,
		Start:         time.Unix(1_700_000_000, 0).UTC(),
		InitialHeight: 1,
	})
	require.NoError(t, err)
	good, err := src.AdvanceOne()
	require.NoError(t, err)

	hostile := *good
	hostile.AppHash = append([]byte(nil), good.AppHash...)
	hostile.AppHash[0] ^= 0x01

	mux := http.NewServeMux()
	mux.HandleFunc("/block/latest", func(w http.ResponseWriter, _ *http.Request) {
		payload, _ := json.Marshal(&hostile)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/block/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		payload, _ := json.Marshal(&hostile)
		fmt.Fprintf(w, "data: %s\n\n", payload)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	vs, err := verifier.NewValidatorSet("gonka-test", verifiers)
	require.NoError(t, err)
	v := verifier.New(vs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c, err := client.NewHTTP(ctx, client.HTTPConfig{
		BaseURL:          ts.URL,
		Verifier:         v,
		ResubscribeAfter: 20 * time.Millisecond,
		StaleAfter:       time.Second,
	})
	require.NoError(t, err)
	defer c.Close()

	// Latest() must fail since the synchronous fetch verifies too.
	_, err = c.Latest(ctx)
	require.Error(t, err)

	// Give the SSE goroutine a beat to ingest the tampered frame.
	require.Eventually(t, func() bool {
		return c.RejectedCount() >= 1
	}, time.Second, 20*time.Millisecond)
}

// TestClient_TrustModeAcceptsEverything confirms that a client built
// with Verifier=nil (host-side trust mode) accepts headers without
// cryptographic verification and caches the full header including
// Commit.Signatures. Even a tampered AppHash must flow through to the
// consumer, because verification is the consumer's (or a downstream
// auditor's) responsibility.
func TestClient_TrustModeAcceptsEverything(t *testing.T) {
	mocks, _ := genMockValidators(t, 10)

	src, err := observer.NewMock(observer.MockConfig{
		ChainID:       "gonka-test",
		Validators:    mocks,
		BlockInterval: time.Second,
		Seed:          99,
		Start:         time.Unix(1_700_000_000, 0).UTC(),
		InitialHeight: 1,
	})
	require.NoError(t, err)
	good, err := src.AdvanceOne()
	require.NoError(t, err)

	tampered := *good
	tampered.AppHash = append([]byte(nil), good.AppHash...)
	tampered.AppHash[0] ^= 0x01

	mux := http.NewServeMux()
	mux.HandleFunc("/block/latest", func(w http.ResponseWriter, _ *http.Request) {
		payload, _ := json.Marshal(&tampered)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/block/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		payload, _ := json.Marshal(&tampered)
		fmt.Fprintf(w, "data: %s\n\n", payload)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c, err := client.NewHTTP(ctx, client.HTTPConfig{
		BaseURL:          ts.URL,
		Verifier:         nil, // host trust mode
		ResubscribeAfter: 20 * time.Millisecond,
		StaleAfter:       time.Second,
	})
	require.NoError(t, err)
	defer c.Close()

	// Latest() succeeds despite the tampered AppHash.
	latest, err := c.Latest(ctx)
	require.NoError(t, err)
	require.Equal(t, tampered.AppHash, latest.AppHash)
	// Full signature set is forwarded intact.
	require.Equal(t, len(good.Commit.Signatures), len(latest.Commit.Signatures))

	// No rejections recorded in trust mode.
	require.Eventually(t, func() bool {
		h, _ := c.Latest(ctx)
		return h != nil
	}, time.Second, 20*time.Millisecond)
	require.Zero(t, c.RejectedCount())
}

func TestClient_ResubscribesAfterDisconnect(t *testing.T) {
	ts, mock, v, cleanup := newServedMock(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := client.NewHTTP(ctx, client.HTTPConfig{
		BaseURL:          ts.URL,
		Verifier:         v,
		ResubscribeAfter: 20 * time.Millisecond,
		StaleAfter:       time.Second,
	})
	require.NoError(t, err)
	defer c.Close()

	_, err = mock.AdvanceOne()
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		h, _ := c.Latest(ctx)
		return h != nil && h.Height >= 1
	}, 2*time.Second, 25*time.Millisecond)

	for i := 0; i < 3; i++ {
		_, err := mock.AdvanceOne()
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool {
		h, _ := c.Latest(ctx)
		return h != nil && h.Height >= 4
	}, 2*time.Second, 25*time.Millisecond)
}

func TestClient_NewInProcess_Identity(t *testing.T) {
	_, mock, _, cleanup := newServedMock(t)
	defer cleanup()
	_, err := mock.AdvanceOne()
	require.NoError(t, err)

	oracle := client.NewInProcess(mock)
	h, err := oracle.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(1), h.Height)
}

func TestClient_StaleAfterQuietPeriod(t *testing.T) {
	ts, _, v, cleanup := newServedMock(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := client.NewHTTP(ctx, client.HTTPConfig{
		BaseURL:          ts.URL,
		Verifier:         v,
		ResubscribeAfter: 20 * time.Millisecond,
		StaleAfter:       25 * time.Millisecond,
	})
	require.NoError(t, err)
	defer c.Close()

	// No header ever ingested → stale should be true.
	require.True(t, c.Stale())
	stale, ageMs, height, never := c.StaleDetails()
	require.True(t, stale)
	require.True(t, never)
	require.Equal(t, int64(0), ageMs)
	require.Equal(t, int64(0), height)
}
