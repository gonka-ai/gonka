package transport

import (
	"github.com/labstack/echo/v4"
)

// X-Devshard-Error classifies non-OK host responses for observability and
// direct host API clients. The gateway treats all upstream 503s uniformly
// (hide-redirect via redundancy and throttling).
const HeaderDevshardError = "X-Devshard-Error"

const (
	DevshardErrorRequestsDisabled = "requests_disabled"
	DevshardErrorInitializing     = "initializing"
	DevshardErrorNotImplemented   = "not_implemented"
)

// HTTPError returns an echo HTTP error and sets X-Devshard-Error when devshardCode is non-empty.
func HTTPError(c echo.Context, code int, devshardCode, message string) error {
	if devshardCode != "" {
		c.Response().Header().Set(HeaderDevshardError, devshardCode)
	}
	return echo.NewHTTPError(code, message)
}
