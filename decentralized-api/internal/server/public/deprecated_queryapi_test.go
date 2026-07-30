package public

import (
	"net/http"
	"net/http/httptest"
	"testing"

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

	// First-class dapi handler (not dual-serve). Nil recorder panics inside
	// getVersions without setting Deprecation.
	req := httptest.NewRequest(http.MethodGet, "/v1/versions", nil)
	rec := httptest.NewRecorder()
	panicked := false
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		s.e.ServeHTTP(rec, req)
	}()
	require.True(t, panicked, "nil recorder should panic inside getVersions")
	require.Empty(t, rec.Header().Get("Deprecation"))
}
