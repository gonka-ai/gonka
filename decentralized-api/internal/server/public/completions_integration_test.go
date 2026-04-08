package public

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestPostCompletions_BatchPrompts_ReturnsBadRequest(t *testing.T) {
	e := echo.New()
	s := &Server{e: e}

	req := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(`{"model":"test-model","prompt":["a","b"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)

	err := s.postCompletions(ctx)
	require.Error(t, err)

	var httpErr *echo.HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
	require.Equal(t, "batch prompts are not supported", httpErr.Message)
}

func TestPostCompletions_StreamWithBatchPrompts_ReturnsBadRequest(t *testing.T) {
	e := echo.New()
	s := &Server{e: e}

	req := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(`{"model":"test-model","prompt":["a","b"],"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)

	err := s.postCompletions(ctx)
	require.Error(t, err)

	var httpErr *echo.HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
	require.Equal(t, "batch prompts are not supported", httpErr.Message)
}

func TestPostCompletions_OversizedBody_ReturnsRequestEntityTooLarge(t *testing.T) {
	e := echo.New()
	s := &Server{e: e}

	oversizedBody := bytes.Repeat([]byte("a"), MaxRequestBodySize+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/completions", bytes.NewReader(oversizedBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)

	err := s.postCompletions(ctx)
	require.Error(t, err)

	var httpErr *echo.HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, http.StatusRequestEntityTooLarge, httpErr.Code)
}
