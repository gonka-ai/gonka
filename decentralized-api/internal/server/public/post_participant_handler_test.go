package public

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"decentralized-api/internal/server/middleware"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestSubmitNewParticipantHandler_MalformedJSON(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = middleware.TransparentErrorHandler
	s := &Server{e: e}

	req := httptest.NewRequest(http.MethodPost, "/v1/participants", bytes.NewBufferString(`{"invalid_json":`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := s.submitNewParticipantHandler(c)
	require.Error(t, err)

	e.HTTPErrorHandler(err, c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, `{"error":"Invalid request body"}`, rec.Body.String())
}

func TestSubmitNewParticipantHandler_MissingFields(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = middleware.TransparentErrorHandler
	s := &Server{e: e}

	req := httptest.NewRequest(http.MethodPost, "/v1/participants", bytes.NewBufferString(`{"url":"http://localhost"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := s.submitNewParticipantHandler(c)
	require.Error(t, err)

	e.HTTPErrorHandler(err, c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, `{"error":"Address and PubKey are required"}`, rec.Body.String())
}
