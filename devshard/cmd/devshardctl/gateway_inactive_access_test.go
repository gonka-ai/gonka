package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// newInactiveDevshardGateway builds a store-backed gateway that knows about a
// single non-resident (not in memory), inactive devshard "77". Because no
// runtime is registered, any /devshard/77/... read takes the non-resident
// branch of handleDevshard.
func newInactiveDevshardGateway(t *testing.T) *Gateway {
	t.Helper()
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	settings := GatewaySettings{DefaultModel: "m"}
	devshards := []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{
			ID:            "77",
			PrivateKeyHex: "secret",
			Model:         "m",
			StoragePath:   filepath.Join(t.TempDir(), "escrow-77"),
		},
		Active: false,
	}}
	require.NoError(t, store.Initialize(settings, devshards))

	return NewManagedGateway(nil, NewGatewayLimiter(0, 0), settings, t.TempDir(), store, nil)
}

// A non-admin caller may read a non-resident devshard's /v1/status, but only
// the cheap, state-free metadata subset -- no snapshot/state is loaded.
func TestInactiveDevshardPublicStatusIsMetadataOnly(t *testing.T) {
	g := newInactiveDevshardGateway(t)

	req := httptest.NewRequest(http.MethodGet, "/devshard/77/v1/status", nil)
	rec := httptest.NewRecorder()
	g.handleDevshard(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "1", rec.Header().Get("X-Devshard-Metadata-Only"))

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "77", body["id"])
	require.Equal(t, "m", body["model"])
	require.Equal(t, false, body["active"])
	require.Equal(t, false, body["resident"])
	require.Equal(t, true, body["metadata_only"])

	// Fields that would require replaying diffs / loading a snapshot must be
	// absent: the whole point is that no state was hydrated.
	require.NotContains(t, body, "nonce")
	require.NotContains(t, body, "balance")
	require.NotContains(t, body, "phase")
}

// /v1/models is derivable from cheap registry config, so it is public for a
// non-resident devshard.
func TestInactiveDevshardPublicModelsIsMetadataOnly(t *testing.T) {
	g := newInactiveDevshardGateway(t)

	req := httptest.NewRequest(http.MethodGet, "/devshard/77/v1/models", nil)
	rec := httptest.NewRecorder()
	g.handleDevshard(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "1", rec.Header().Get("X-Devshard-Metadata-Only"))
	require.Contains(t, rec.Body.String(), `"m"`)
}

// A read that needs hydrated state (here /v1/requests/*) is refused for a
// non-admin caller on a non-resident devshard: it must look unknown and must
// not hydrate (no metadata-only response either).
func TestInactiveDevshardPublicStatefulReadRefused(t *testing.T) {
	g := newInactiveDevshardGateway(t)

	req := httptest.NewRequest(http.MethodGet, "/devshard/77/v1/requests/abc", nil)
	rec := httptest.NewRecorder()
	g.handleDevshard(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Empty(t, rec.Header().Get("X-Devshard-Metadata-Only"))
	require.Contains(t, rec.Body.String(), "unknown devshard")
}

// With admin auth present, the non-resident read path hydrates a transient
// runtime instead of returning the metadata-only response. We only assert it
// did NOT take the metadata-only branch (hydration may succeed or fail
// depending on on-disk storage, but either way it is not metadata-only).
func TestInactiveDevshardAdminReadHydrates(t *testing.T) {
	g := newInactiveDevshardGateway(t)

	req := httptest.NewRequest(http.MethodGet, "/devshard/77/v1/status", nil)
	req = req.WithContext(context.WithValue(req.Context(), adminAuthContextKey{}, true))
	rec := httptest.NewRecorder()
	g.handleDevshard(rec, req)

	require.Empty(t, rec.Header().Get("X-Devshard-Metadata-Only"),
		"admin read must hydrate, not serve the metadata-only response")
}

// An unknown (not-in-store) devshard must not reveal anything to a non-admin,
// even on the otherwise-public metadata paths.
func TestUnknownDevshardPublicReadIsNotFound(t *testing.T) {
	g := newInactiveDevshardGateway(t)

	for _, path := range []string{"/devshard/does-not-exist/v1/status", "/devshard/does-not-exist/v1/models"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		g.handleDevshard(rec, req)

		require.Equal(t, http.StatusNotFound, rec.Code, "path=%s", path)
		require.Empty(t, rec.Header().Get("X-Devshard-Metadata-Only"), "path=%s", path)
	}
}
