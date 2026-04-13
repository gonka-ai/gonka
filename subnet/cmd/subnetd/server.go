package main

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"common/chain"
	"common/queryapi"
	"common/queryapi/gen"
)

// buildServer creates the Echo instance with all routes mounted:
//   - GET /healthz
//   - /v1/... (queryapi: status, participants, models, epochs, BLS, etc.)
//     TODO: - /v1/subnet/sessions/:id/... (per-escrow subnet session routes)
func buildServer(chainClient *chain.Client) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())

	e.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Chain query API: status, participants, models, epochs, BLS, bridge, etc.
	gen.RegisterHandlers(e, queryapi.NewHandlers(chainClient))

	return e
}
