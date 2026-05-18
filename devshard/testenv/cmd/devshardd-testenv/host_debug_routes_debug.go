//go:build dev || debug || development

package main

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"devshard/internal/debugbuild"
	"devshard/transport"
)

func registerHostInferenceHoldDebugRoutes(e *echo.Echo, srv *transport.Server) {
	if !debugbuild.HostDebugRoutesEnabled() {
		return
	}
	e.POST("/v1/debug/arm-hold-inference-response", func(c echo.Context) error {
		srv.ArmHoldInferenceResponse()
		return c.NoContent(http.StatusNoContent)
	})
	e.POST("/v1/debug/release-hold-inference-response", func(c echo.Context) error {
		srv.ReleaseHoldInferenceResponse()
		return c.NoContent(http.StatusNoContent)
	})
}
