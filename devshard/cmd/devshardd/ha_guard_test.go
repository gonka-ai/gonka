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

func TestRequireHADeploymentStorage(t *testing.T) {
	t.Setenv(envHADeployment, "off")
	t.Setenv(mode.EnvStorageMode, "sqlite")
	require.NoError(t, requireHADeploymentStorage(),
		"single-instance deployment must not require postgres")

	t.Setenv(envHADeployment, "on")
	require.Error(t, requireHADeploymentStorage(),
		"HA deployment on sqlite must refuse to start")

	t.Setenv(mode.EnvStorageMode, "hybrid")
	t.Setenv("PGHOST", "pg")
	require.Error(t, requireHADeploymentStorage(),
		"hybrid keeps a local fallback and is not fail-closed")

	t.Setenv(mode.EnvStorageMode, "postgres")
	require.NoError(t, requireHADeploymentStorage())

	t.Setenv("PGHOST", "")
	require.Error(t, requireHADeploymentStorage(), "postgres without PGHOST")
}

func TestRequireHADeploymentStorageRejectsInvalidBoolean(t *testing.T) {
	t.Setenv(envHADeployment, "enabled")

	err := requireHADeploymentStorage()
	require.Error(t, err)
	require.Contains(t, err.Error(), envHADeployment)
	require.Contains(t, err.Error(), "invalid boolean value")
}
