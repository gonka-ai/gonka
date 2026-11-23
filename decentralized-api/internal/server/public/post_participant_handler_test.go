package public

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	servermiddleware "decentralized-api/internal/server/middleware"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestSubmitNewParticipantHandler_MalformedJSON(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = servermiddleware.TransparentErrorHandler

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/participants",
		strings.NewReader(`{"address":`),
	)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)

	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)

	server := &Server{}

	err := server.submitNewParticipantHandler(ctx)
	require.Error(t, err)

	e.HTTPErrorHandler(err, ctx)

	require.Equal(t, http.StatusBadRequest, rec.Code)

	t.Logf("response body: %s", rec.Body.String())
}