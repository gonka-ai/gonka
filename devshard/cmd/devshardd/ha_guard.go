package main

import (
	"net/http"

	"common/storage/mode"

	"github.com/labstack/echo/v4"
)

// haStorageGuard rejects multi-instance HA-marked requests unless this process
// is explicitly configured for fail-closed Postgres (DEVSHARD_STORAGE_MODE=postgres
// and PGHOST set). versiond-router sets Devshard-Ha when sticky-hashing across
// multiple VERSIOND_HOSTS for versions not in VERSIOND_NON_HA_VERSIONS.
func haStorageGuard() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !mode.HasDevshardHAHeader(c.Request().Header) {
				return next(c)
			}
			if err := mode.RequireConfiguredForHA(); err != nil {
				return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
			}
			return next(c)
		}
	}
}
