package main

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"common/probe"

	"devshard/observability"
)

// buildServer creates the Echo instance for devshardd session traffic only.
// Tier A read-only /v1/ routes are served by edge-api (see edge-api/).
func buildServer(lifecycle *lifecycleState) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())
	e.Use(haStorageGuard())
	e.Use(lifecycle.middleware)

	observability.RegisterRuntimeCollectors()
	e.GET("/metrics", echo.WrapHandler(observability.MetricsHandler()))
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	// Child-only clock contract. Gateway probes {RoutePrefix}/clock; versiond
	// strips the version segment. Do not mount this on versiond's mux.
	e.GET("/clock", echo.WrapHandler(probe.Handler(nil)))

	return e
}

func buildAdminServer(lifecycle *lifecycleState, storageReady func() bool) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())

	e.GET("/ready", func(c echo.Context) error {
		status := lifecycle.Status()
		storeReady := storageReady == nil || storageReady()
		if !status.Ready || status.Draining || !storeReady {
			return c.JSON(http.StatusServiceUnavailable, readyResponse(status, storeReady))
		}
		return c.JSON(http.StatusOK, readyResponse(status, storeReady))
	})
	e.POST("/drain", func(c echo.Context) error {
		lifecycle.StartDrain()
		return c.JSON(http.StatusOK, lifecycle.Status())
	})
	e.GET("/drain/status", func(c echo.Context) error {
		return c.JSON(http.StatusOK, lifecycle.Status())
	})

	return e
}

// readyStatus augments drainStatus with storage readiness for the /ready probe.
type readyStatus struct {
	drainStatus
	StorageReady bool `json:"storage_ready"`
}

func readyResponse(status drainStatus, storeReady bool) readyStatus {
	return readyStatus{drainStatus: status, StorageReady: storeReady}
}
