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

func TestVersionsRouteUsesEnrichedDAPIHandler(t *testing.T) {
	s := NewServer(nil, newTestConfigManager(t), nil, nil, nil, nil)

	s.versionsCache.response = &versionsResponse{
		Timestamp:   "2026-01-01T00:00:00Z",
		APIVersion:  map[string]string{"version": "test"},
		NodeVersion: map[string]string{"version": "test"},
		MLNodes: []mlnodeVersionResponse{{
			NodeID:                 "node-1",
			Version:                "1.0.0",
			PoCValidationInference: true,
		}},
	}
	s.versionsCache.expiresAt = time.Now().Add(time.Hour)

	req := httptest.NewRequest(http.MethodGet, "/v1/versions", nil)
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"mlnodes"`)
	require.Contains(t, rec.Body.String(), `"poc_validation_inference":true`)
	require.Empty(t, rec.Header().Get("Deprecation"))
}
