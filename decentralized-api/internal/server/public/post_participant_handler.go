package public

import (
	"decentralized-api/internal/server/public_entities"
	"decentralized-api/logging"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
)

func (s *Server) submitNewParticipantHandler(ctx echo.Context) error {
	var body public_entities.SubmitUnfundedNewParticipantDto

	if err := ctx.Bind(&body); err != nil {
		logging.Error("Failed to decode request body", types.Participants, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	logging.Debug("SubmitNewParticipantDto", types.Participants, "body", body)

	if body.Address == "" || body.PubKey == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Address and PubKey are required")
	}
	if err := s.submitNewUnfundedParticipant(body); err != nil {
		return err
	}
	return ctx.NoContent(http.StatusOK)
}

func (s *Server) submitNewUnfundedParticipant(body public_entities.SubmitUnfundedNewParticipantDto) error {
	msg := &inference.MsgSubmitNewUnfundedParticipant{
		Address:      body.Address,
		Url:          body.Url,
		ValidatorKey: body.ValidatorKey,
		PubKey:       body.PubKey,
		WorkerKey:    body.WorkerKey,
	}

	logging.Debug("Submitting NewUnfundedParticipant", types.Participants, "message", msg)

	if err := s.recorder.SubmitNewUnfundedParticipant(msg); err != nil {
		logging.Error("Failed to submit MsgSubmitNewUnfundedParticipant", types.Participants, "error", err)
		return err
	}
	return nil
}
