package public

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDeprecatedQueryAPIRoutes_StatusMarksDeprecation(t *testing.T) {
	s := NewServer(nil, newTestConfigManager(t), nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "true", rec.Header().Get("Deprecation"))
	require.Contains(t, rec.Header().Get("Link"), "edge-api")
	require.Contains(t, rec.Body.String(), `"status":"ok"`)
}

func TestDeprecatedLegacyDapiOnlyRoutes_BridgeLatestMarksDeprecation(t *testing.T) {
	s := NewServer(nil, newTestConfigManager(t), nil, &BridgeQueue{}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/bridge/block/latest?chain=ethereum", nil)
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "true", rec.Header().Get("Deprecation"))
	require.Contains(t, rec.Header().Get("Link"), "edge-api")
}

func TestVersionsRoute_FirstClassNotDeprecated(t *testing.T) {
	s := NewServer(nil, newTestConfigManager(t), nil, nil, nil, nil)

	// Pre-fill the versions cache so getVersions serves from cache without
	// touching the nil recorder. If the deprecated queryapi mount were serving
	// /v1/versions instead, it would return 404 (and no mlnodes).
	s.versionsCache.response = &versionsResponse{
		Timestamp:   "2026-01-01T00:00:00Z",
		APIVersion:  map[string]string{"version": "test"},
		NodeVersion: map[string]string{"version": "test"},
		MLNodes:     []mlnodeVersionResponse{},
	}
	s.versionsCache.expiresAt = time.Now().Add(time.Hour)

	req := httptest.NewRequest(http.MethodGet, "/v1/versions", nil)
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"mlnodes"`)
	require.Empty(t, rec.Header().Get("Deprecation"))
}
