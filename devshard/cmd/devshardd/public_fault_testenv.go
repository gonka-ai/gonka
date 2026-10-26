//go:build devshard_testenv

package main

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/labstack/echo/v4"
)

// Public-listener fault for testenv load scenarios, switched by a trip file
// so the harness can flip it with docker exec while the stack runs. While the
// file exists every public route answers 503 and the admin /ready is
// untouched, so versiond sees a child whose public port is down but which
// still reports ready. Production builds compile public_fault_prod.go.
const (
	envTestenvPublicFaultDir = "DEVSHARD_TESTENV_EXECUTOR_FAULT_DIR"
	defaultPublicFaultDir    = "/tmp"

	// PublicFaultDownFile makes the public listener answer 503 on every route.
	PublicFaultDownFile = "devshard-fault-public-down"
)

func publicFaultFile() string {
	dir := os.Getenv(envTestenvPublicFaultDir)
	if dir == "" {
		dir = defaultPublicFaultDir
	}
	return filepath.Join(dir, PublicFaultDownFile)
}

func publicFaultActive() bool {
	_, err := os.Stat(publicFaultFile())
	return err == nil
}

func publicFaultMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if publicFaultActive() {
				c.Response().Header().Set("Connection", "close")
				return c.NoContent(http.StatusServiceUnavailable)
			}
			return next(c)
		}
	}
}
