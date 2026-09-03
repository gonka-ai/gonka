package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"common/storage/mode"
	"github.com/labstack/echo/v4"

	"devshard/internal/boolvalue"
)

// requireHADeploymentStorage fails fast when an HA deployment is not backed by
// fail-closed Postgres. It uses the same strict boolean grammar as other
// devshard environment settings.
func requireHADeploymentStorage() error {
	raw := os.Getenv(mode.EnvHADeployment)
	ha, err := boolvalue.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", mode.EnvHADeployment, err)
	}
	if !ha {
		return nil
	}
	if err := mode.RequireConfiguredForHA(); err != nil {
		return fmt.Errorf("%s=%q: %w", mode.EnvHADeployment, raw, err)
	}
	return nil
}

// parseDevshardHAHeader reports whether the router marked a request as
// multi-instance HA. A present empty value is the legacy shorthand for true;
// every non-empty value uses the same grammar as boolean environment settings.
func parseDevshardHAHeader(h http.Header) (bool, error) {
	if h == nil {
		return false, nil
	}
	values := h.Values(mode.HeaderDevshardHA)
	if len(values) == 0 {
		return false, nil
	}
	if len(values) != 1 {
		return false, fmt.Errorf("%s must have exactly one value", mode.HeaderDevshardHA)
	}

	raw := values[0]
	if strings.TrimSpace(raw) == "" {
		return true, nil
	}
	ha, err := boolvalue.Parse(raw)
	if err != nil {
		return false, fmt.Errorf("%s=%q: %w", mode.HeaderDevshardHA, raw, err)
	}
	return ha, nil
}

// haStorageGuard rejects multi-instance HA-marked requests unless this process
// is explicitly configured for fail-closed Postgres (DEVSHARD_STORAGE_MODE=postgres
// and PGHOST set). versiond-router sets Devshard-Ha on HA-pool traffic when the
// deployment declares itself HA or multiple pool hosts are usable, so a sibling
// could be serving the same escrow.
//
// This is the request-time half of the pair; requireHADeploymentStorage is the
// startup half and refuses to boot at all. Both exist because a partial rollout
// can leave a process that started before the deployment became HA. The storage
// configuration comes from immutable process environment, so resolve it once
// when the middleware is built.
func haStorageGuard() echo.MiddlewareFunc {
	storageErr := mode.RequireConfiguredForHA()
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ha, err := parseDevshardHAHeader(c.Request().Header)
			if err != nil {
				// Treat an unrecognised internal marker conservatively as HA. A
				// Postgres-backed process can serve it safely; an unsafe process
				// still fails closed below.
				ha = true
			}
			if !ha {
				return next(c)
			}
			if storageErr != nil {
				return echo.NewHTTPError(http.StatusServiceUnavailable, storageErr.Error())
			}
			return next(c)
		}
	}
}
