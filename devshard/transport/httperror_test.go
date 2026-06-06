package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestHTTPError_SetsDevshardHeader(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/sessions/x/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := HTTPError(c, http.StatusServiceUnavailable, DevshardErrorRequestsDisabled, "disabled")
	require.Error(t, err)
	e.HTTPErrorHandler(err, c)
	require.Equal(t, DevshardErrorRequestsDisabled, rec.Header().Get(HeaderDevshardError))
}

func TestStatusErrorFromResponse_ReadsHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set(HeaderDevshardError, DevshardErrorInitializing)
	rec.WriteHeader(http.StatusServiceUnavailable)

	err := StatusErrorFromResponse(http.MethodPost, "/p", rec.Result(), "init")
	require.Equal(t, DevshardErrorInitializing, err.DevshardError)
	require.True(t, err.IsFailFast())
}
