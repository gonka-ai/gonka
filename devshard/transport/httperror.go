package transport

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// X-Devshard-Error classifies non-OK host responses for devshardctl fail-fast handling.
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

// StatusErrorFromResponse builds an HTTPStatusError from a non-OK HTTP response.
func StatusErrorFromResponse(method, path string, resp *http.Response, body string) *HTTPStatusError {
	if resp == nil {
		return &HTTPStatusError{Method: method, Path: path, Body: body}
	}
	return &HTTPStatusError{
		Method:        method,
		Path:          path,
		StatusCode:    resp.StatusCode,
		Body:          body,
		DevshardError: resp.Header.Get(HeaderDevshardError),
	}
}
