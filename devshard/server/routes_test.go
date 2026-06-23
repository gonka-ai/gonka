package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/storage"
	"devshard/transport"
)

func testEchoContext(t *testing.T) echo.Context {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/sessions/x/chat/completions", nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec)
}

func TestSessionHTTPErrorConflicts(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("wrapped: %w", storage.ErrSessionVersionConflict),
		fmt.Errorf("wrapped: %w", storage.ErrSessionEpochConflict),
	} {
		c := testEchoContext(t)
		httpErr, ok := sessionHTTPError(c, err).(*echo.HTTPError)
		require.True(t, ok)
		require.Equal(t, http.StatusConflict, httpErr.Code)
		require.Contains(t, fmt.Sprint(httpErr.Message), "wrapped")
	}
}

func TestSessionHTTPErrorInitializing(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/sessions/x/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := sessionHTTPError(c, fmt.Errorf("wrapped: %w", ErrInitializing))
	require.Error(t, err)
	e.HTTPErrorHandler(err, c)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, transport.DevshardErrorInitializing, rec.Header().Get(transport.HeaderDevshardError))
}

func TestSessionHTTPErrorDefault(t *testing.T) {
	c := testEchoContext(t)
	httpErr, ok := sessionHTTPError(c, fmt.Errorf("boom")).(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusInternalServerError, httpErr.Code)
}
