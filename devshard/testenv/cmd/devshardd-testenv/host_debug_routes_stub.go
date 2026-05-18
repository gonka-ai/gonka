//go:build !dev && !debug && !development

package main

import (
	"github.com/labstack/echo/v4"

	"devshard/transport"
)

func registerHostInferenceHoldDebugRoutes(*echo.Echo, *transport.Server) {}
