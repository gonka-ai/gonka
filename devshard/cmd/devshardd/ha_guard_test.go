package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"common/storage/mode"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestHAStorageGuard_AllowsWithoutHeader(t *testing.T) {
	t.Setenv(mode.EnvStorageMode, "sqlite")
	t.Setenv("PGHOST", "")

	e := echo.New()
	e.Use(haStorageGuard())
	e.GET("/x", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestHAStorageGuard_RejectsHAWithoutPostgresMode(t *testing.T) {
	t.Setenv(mode.EnvStorageMode, "hybrid")
	t.Setenv("PGHOST", "db.example")

	e := echo.New()
	e.Use(haStorageGuard())
	e.GET("/x", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(mode.HeaderDevshardHA, "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), mode.EnvStorageMode)
}

func TestHAStorageGuard_AllowsHAWithPostgres(t *testing.T) {
	t.Setenv(mode.EnvStorageMode, "postgres")
	t.Setenv("PGHOST", "db.example")

	e := echo.New()
	e.Use(haStorageGuard())
	e.GET("/x", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(mode.HeaderDevshardHA, "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestHAStorageGuard_RejectsMalformedHeader(t *testing.T) {
	t.Setenv(mode.EnvStorageMode, "postgres")
	t.Setenv("PGHOST", "db.example")

	e := echo.New()
	e.Use(haStorageGuard())
	e.GET("/x", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(mode.HeaderDevshardHA, "tru")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), mode.HeaderDevshardHA)
}
