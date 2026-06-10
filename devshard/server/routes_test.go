package server

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/storage"
)

func TestSessionHTTPErrorConflicts(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("wrapped: %w", storage.ErrSessionVersionConflict),
		fmt.Errorf("wrapped: %w", storage.ErrSessionEpochConflict),
	} {
		httpErr, ok := sessionHTTPError(err).(*echo.HTTPError)
		require.True(t, ok)
		require.Equal(t, http.StatusConflict, httpErr.Code)
		require.Contains(t, fmt.Sprint(httpErr.Message), "wrapped")
	}
}

func TestSessionHTTPErrorInitializing(t *testing.T) {
	httpErr, ok := sessionHTTPError(fmt.Errorf("wrapped: %w", ErrInitializing)).(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusServiceUnavailable, httpErr.Code)
	require.Contains(t, fmt.Sprint(httpErr.Message), "wrapped")
}

func TestSessionHTTPErrorDefault(t *testing.T) {
	httpErr, ok := sessionHTTPError(fmt.Errorf("boom")).(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusInternalServerError, httpErr.Code)
}
