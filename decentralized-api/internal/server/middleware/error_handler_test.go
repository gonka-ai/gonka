package middleware_test

import (
	"errors"
	"net/http"
	"testing"

	"decentralized-api/internal/server/middleware"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestExtractError(t *testing.T) {
	baseErr := errors.New("inference server is not running")

	// 1. Generic error
	status, msg := middleware.ExtractError(baseErr)
	require.Equal(t, http.StatusInternalServerError, status)
	require.Equal(t, baseErr.Error(), msg)

	// 2. echo.HTTPError preserving original payload and status code
	httpErr := echo.NewHTTPError(http.StatusBadRequest, baseErr)

	status, msg = middleware.ExtractError(httpErr)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, baseErr, msg)
}
