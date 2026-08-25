package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"common/storage/mode"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestParseDevshardHAHeader(t *testing.T) {
	tests := []struct {
		name    string
		values  []string
		want    bool
		wantErr bool
	}{
		{name: "absent", want: false},
		{name: "empty marker", values: []string{""}, want: true},
		{name: "whitespace marker", values: []string{" \t"}, want: true},
		{name: "zero", values: []string{"0"}, want: false},
		{name: "f", values: []string{"f"}, want: false},
		{name: "false", values: []string{"false"}, want: false},
		{name: "no", values: []string{"no"}, want: false},
		{name: "off", values: []string{"off"}, want: false},
		{name: "one", values: []string{"1"}, want: true},
		{name: "t", values: []string{"t"}, want: true},
		{name: "true", values: []string{"true"}, want: true},
		{name: "yes", values: []string{"yes"}, want: true},
		{name: "on", values: []string{"on"}, want: true},
		{name: "case and whitespace", values: []string{"  YeS\t"}, want: true},
		{name: "invalid", values: []string{"enabled"}, wantErr: true},
		{name: "repeated", values: []string{"true", "false"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			if tt.values != nil {
				header[mode.HeaderDevshardHA] = tt.values
			}

			got, err := parseDevshardHAHeader(header)
			if tt.wantErr {
				require.Error(t, err)
				require.False(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}

	ha, err := parseDevshardHAHeader(nil)
	require.NoError(t, err)
	require.False(t, ha)
}

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

func TestHAStorageGuard_AllowsMalformedHeaderWithPostgres(t *testing.T) {
	t.Setenv(mode.EnvStorageMode, "postgres")
	t.Setenv("PGHOST", "db.example")

	e := echo.New()
	e.Use(haStorageGuard())
	e.GET("/x", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(mode.HeaderDevshardHA, "tru")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestHAStorageGuard_RejectsMalformedHeaderWithoutPostgres(t *testing.T) {
	t.Setenv(mode.EnvStorageMode, "sqlite")
	t.Setenv("PGHOST", "")

	for _, values := range [][]string{{"tru"}, {"true", "false"}} {
		e := echo.New()
		e.Use(haStorageGuard())
		e.GET("/x", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header[mode.HeaderDevshardHA] = values
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
		require.Contains(t, rec.Body.String(), mode.EnvStorageMode)
	}
}

func TestHAStorageGuard_CapturesStorageVerdictAtConstruction(t *testing.T) {
	t.Setenv(mode.EnvStorageMode, "sqlite")
	t.Setenv("PGHOST", "")
	middleware := haStorageGuard()

	// A running process cannot change its own inherited environment. Keep the
	// request path tied to the configuration used when its middleware was built.
	t.Setenv(mode.EnvStorageMode, "postgres")
	t.Setenv("PGHOST", "db.example")

	e := echo.New()
	e.Use(middleware)
	e.GET("/x", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(mode.HeaderDevshardHA, "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestRequireHADeploymentStorage(t *testing.T) {
	t.Setenv(mode.EnvHADeployment, "off")
	t.Setenv(mode.EnvStorageMode, "sqlite")
	require.NoError(t, requireHADeploymentStorage(),
		"single-instance deployment must not require postgres")

	t.Setenv(mode.EnvHADeployment, "on")
	err := requireHADeploymentStorage()
	require.Error(t, err)
	require.Contains(t, err.Error(), `GONKA_HA="on"`)
	require.Contains(t, err.Error(), mode.EnvStorageMode)

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
	t.Setenv(mode.EnvHADeployment, "enabled")

	err := requireHADeploymentStorage()
	require.Error(t, err)
	require.Contains(t, err.Error(), mode.EnvHADeployment)
	require.Contains(t, err.Error(), "invalid boolean value")
}

func TestBuildAppChecksHAStorageBeforeSideEffects(t *testing.T) {
	t.Setenv(mode.EnvHADeployment, "on")
	t.Setenv(mode.EnvStorageMode, "sqlite")
	dataDir := filepath.Join(t.TempDir(), "data")

	app, err := buildApp(context.Background(), runtimeConfig{DataDir: dataDir})
	require.Error(t, err)
	require.Nil(t, app)
	_, statErr := os.Stat(dataDir)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}
