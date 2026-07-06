package public

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestDevshardGroupReturnsDeprecated(t *testing.T) {
	e := echo.New()
	s := &Server{e: e}

	g := s.DevshardGroup()
	g.GET("/stats/shards", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "legacy"})
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/devshard/stats/shards", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusGone, rec.Code)
	require.Equal(t, "true", rec.Header().Get("Deprecation"))
	require.Contains(t, rec.Header().Get("Link"), "/devshard/{version}")

	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "deprecated", body["error"])
	require.Contains(t, body["message"], "/devshard/{version}")
}

func TestDevshardGroupReturnsDeprecatedForAllLegacyPaths(t *testing.T) {
	e := echo.New()
	s := &Server{e: e}

	_ = s.DevshardGroup()

	for _, path := range []string{
		"/v1/devshard",
		"/v1/devshard/sessions/escrow-1/unknown",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			require.Equal(t, http.StatusGone, rec.Code)
			require.Equal(t, "true", rec.Header().Get("Deprecation"))
		})
	}
}

func TestNewServerRegistersDeprecatedDevshardPaths(t *testing.T) {
	s := NewServer(nil, newTestConfigManager(t), nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/devshard/sessions/escrow-1/mempool", nil)
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusGone, rec.Code)
	require.Equal(t, "true", rec.Header().Get("Deprecation"))
}
