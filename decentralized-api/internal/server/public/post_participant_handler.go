package public

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

func (s *Server) submitNewParticipantHandler(ctx echo.Context) error {
	ctx.Response().Header().Set("Deprecation", "true")
	return ctx.JSON(http.StatusGone, map[string]string{
		"error":   "deprecated",
		"message": "POST /v1/participants is deprecated; fund your account and register on-chain with MsgSubmitNewParticipant",
	})
}

