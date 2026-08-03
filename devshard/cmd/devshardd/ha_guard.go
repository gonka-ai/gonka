package main

import (
	"net/http"

	"common/storage/mode"

	"github.com/labstack/echo/v4"
)

// haStorageGuard rejects multi-instance HA-marked requests unless this process
// is explicitly configured for fail-closed Postgres (DEVSHARD_STORAGE_MODE=postgres
// and PGHOST set). versiond-router sets Devshard-Ha on HA-pool traffic when the
// deployment declares itself HA or multiple pool hosts are usable, so a sibling
// could be serving the same escrow.
//
// This is the request-time half of the pair; the startup half lives in
// mode.RequireHADeploymentStorage and refuses to boot at all. Both exist because
// a partial rollout can leave a process that started before the deployment
// became HA.
func haStorageGuard() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ha, err := mode.ParseDevshardHAHeader(c.Request().Header)
			if err != nil {
				return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
			}
			if !ha {
				return next(c)
			}
			if err := mode.RequireConfiguredForHA(); err != nil {
				return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
			}
			return next(c)
		}
	}
}
