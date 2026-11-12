//go:build !devshard_testenv

package main

import "github.com/labstack/echo/v4"

func publicFaultMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc { return next }
}
