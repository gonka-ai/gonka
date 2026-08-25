package main

import (
	"fmt"
	"net/http"
	"os"

	"common/storage/mode"
	"devshard/internal/configenv"

	"github.com/labstack/echo/v4"
)

// envHADeployment marks a deployment where several devshard instances may be
// routed the same escrow. The HA Compose overlay sets it explicitly.
const envHADeployment = "GONKA_HA"

// requireHADeploymentStorage fails fast when an HA deployment is not backed by
// fail-closed Postgres. Boolean environment parsing is shared with the other
// devshard components through configenv.ParseBool.
func requireHADeploymentStorage() error {
	ha, err := configenv.ParseBool(os.Getenv(envHADeployment))
	if err != nil {
		return fmt.Errorf("%s: %w", envHADeployment, err)
	}
	if !ha {
		return nil
	}
	if err := mode.RequireConfiguredForHA(); err != nil {
		return fmt.Errorf("%s=true: %w", envHADeployment, err)
	}
	return nil
}

// haStorageGuard rejects multi-instance HA-marked requests unless this process
// is explicitly configured for fail-closed Postgres (DEVSHARD_STORAGE_MODE=postgres
// and PGHOST set). versiond-router sets Devshard-Ha on HA-pool traffic when the
// deployment declares itself HA or multiple pool hosts are usable, so a sibling
// could be serving the same escrow.
//
// This is the request-time half of the pair; requireHADeploymentStorage is the
// startup half and refuses to boot at all. Both exist because a partial rollout
// can leave a process that started before the deployment became HA.
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
