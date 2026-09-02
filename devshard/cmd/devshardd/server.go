package main

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

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

	return e
}

func buildAdminServer(lifecycle *lifecycleState, storageReady, recoveryComplete func() bool) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())

	e.GET("/ready", func(c echo.Context) error {
		status := lifecycle.Status()
		storeReady := storageReady == nil || storageReady()
		recovered := recoveryComplete == nil || recoveryComplete()
		if !status.Ready || status.Draining || !storeReady || !recovered {
			return c.JSON(http.StatusServiceUnavailable, readyResponse(status, storeReady, recovered))
		}
		return c.JSON(http.StatusOK, readyResponse(status, storeReady, recovered))
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

// readyStatus augments drainStatus with storage readiness and session recovery
// progress for the /ready probe.
type readyStatus struct {
	drainStatus
	StorageReady     bool `json:"storage_ready"`
	RecoveryComplete bool `json:"recovery_complete"`
}

func readyResponse(status drainStatus, storeReady, recovered bool) readyStatus {
	return readyStatus{drainStatus: status, StorageReady: storeReady, RecoveryComplete: recovered}
}
