package server

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"common/chain"
	"edge-api/observability"
	"edge-api/queryapi"
	"edge-api/queryapi/gen"
)

// New creates the Echo instance with Tier A read-only routes mounted:
//   - GET /healthz
//   - /v1/... (queryapi: status, participants, models, epochs, BLS, etc.)
func New(chainClient *chain.Client) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())
	e.Use(observability.EchoMiddleware())

	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

	gen.RegisterHandlers(e, queryapi.NewHandlers(chainClient))

	return e
}
