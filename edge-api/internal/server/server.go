package server

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"common/chain"
	"common/devshardobs"
	"common/queryapi"
	"common/queryapi/gen"
	"edge-api/observability"
)

// New creates the Echo instance with Tier A read-only routes mounted:
//   - GET /healthz
//   - /v1/... (queryapi: status, participants, models, epochs, BLS, etc.)
//   - versionless /v1/devshard/... obs (when obsHandler is non-nil)
func New(chainClient *chain.Client, obsHandler http.Handler) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())
	e.Use(observability.EchoMiddleware())

	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

	gen.RegisterHandlers(e, queryapi.NewHandlers(chainClient))

	if obsHandler != nil {
		devshardobs.Mount(e, obsHandler)
	}

	return e
}
