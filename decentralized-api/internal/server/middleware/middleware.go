package middleware

import (
	"decentralized-api/logging"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

func LoggingMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		req := c.Request()
		logging.Info("Received request", types.Server, "method", req.Method, "path", req.URL.Path)
		logging.Debug("Request headers", types.Server, "headers", req.Header)
		return next(c)
	}
}
