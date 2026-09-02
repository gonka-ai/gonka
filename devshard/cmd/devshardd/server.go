package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"devshard/heightsync"
	"devshard/observability"
	"devshard/storage"
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
	_ = heightsync.RegisterAnchorMetrics(observability.Registry())
	// devshardd is the verifier, so it also owns the log-plane instruments.
	_ = heightsync.RegisterLogPlaneMetrics(observability.Registry())
	e.GET("/metrics", echo.WrapHandler(observability.MetricsHandler()))
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

	return e
}

type storageProofFunc func(context.Context, storage.ProofOperation, string) (storage.StorageProof, error)

func buildAdminServer(
	lifecycle *lifecycleState,
	storageReady func() bool,
	storageProof storageProofFunc,
) *echo.Echo {
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
	e.GET("/storage/identity", func(c echo.Context) error {
		return runStorageProof(c, storageProof, storage.ProofIdentity, "")
	})
	e.POST("/storage/challenge", func(c echo.Context) error {
		var request struct {
			Operation storage.ProofOperation `json:"operation"`
			Nonce     string                 `json:"nonce"`
		}
		decoder := json.NewDecoder(c.Request().Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid storage challenge")
		}
		if request.Operation != storage.ProofWriteChallenge && request.Operation != storage.ProofReadChallenge {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid storage challenge operation")
		}
		return runStorageProof(c, storageProof, request.Operation, request.Nonce)
	})

	return e
}

func runStorageProof(
	c echo.Context,
	proof storageProofFunc,
	operation storage.ProofOperation,
	nonce string,
) error {
	if proof == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "postgres storage proof unavailable")
	}
	result, err := proof(c.Request().Context(), operation, nonce)
	if err != nil {
		slog.Warn("devshard storage proof failed", "operation", operation, "error", err)
		return echo.NewHTTPError(http.StatusServiceUnavailable, "postgres storage proof unavailable")
	}
	return c.JSON(http.StatusOK, result)
}

// readyStatus augments drainStatus with storage readiness for the /ready probe.
type readyStatus struct {
	drainStatus
	StorageReady bool `json:"storage_ready"`
}

func readyResponse(status drainStatus, storeReady bool) readyStatus {
	return readyStatus{drainStatus: status, StorageReady: storeReady}
}
