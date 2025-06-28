package middleware

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
)

// TransparentErrorHandler ensures that errors returned by handlers are propagated
// to the client with as much context as possible.
//
// Behaviour:
//   - If the error is *echo.HTTPError – use the embedded status code & message.
//   - Otherwise – treat it as an internal error but still return the original
//     error string so that the client can see what actually happened.
//
// The response body is always JSON in the following form:
//
//	{ "error": "<message>" }
//
// NOTE: Make sure NOT to expose sensitive information in production.
func TransparentErrorHandler(err error, c echo.Context) {
	status, message := ExtractError(err)

	// Avoid double responses
	if c.Response().Committed {
		return
	}

	// Always return JSON so that the client can reliably parse it.
	// We ignore any error from JSON serialization because we are already in the error path.
	_ = c.JSON(status, map[string]interface{}{"error": message})
}

func ExtractError(err error) (int, interface{}) {
	var (
		status              = http.StatusInternalServerError
		message interface{} = err.Error()
	)

	var he *echo.HTTPError
	if errors.As(err, &he) {
		status = he.Code
		if he.Message != nil {
			message = he.Message
		}
	}

	return status, message
}
