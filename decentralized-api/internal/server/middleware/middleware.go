package middleware

import (
	"common/logging"
	"decentralized-api/internal/callbackauth"

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

func RedactingLoggingMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		req := c.Request()
		logging.Info("Received request", types.Server, "method", req.Method, "path", callbackauth.RedactPath(req.URL.Path))
		logging.Debug("Request headers", types.Server, "headers", req.Header)
		return next(c)
	}
}
