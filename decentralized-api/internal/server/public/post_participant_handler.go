package public

import (
	"decentralized-api/logging"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
)

func (s *Server) submitNewParticipantHandler(ctx echo.Context) error {
	var body SubmitUnfundedNewParticipantDto

	if err := ctx.Bind(&body); err != nil {
		logging.Error("Failed to decode request body", types.Participants, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	logging.Debug("SubmitNewParticipantDto", types.Participants, "body", body)
	if body.Address != "" && body.PubKey != "" {
		if err := s.submitNewUnfundedParticipant(body); err != nil {
			return err
		}
		return ctx.NoContent(http.StatusOK)
	}

	msg := &inference.MsgSubmitNewParticipant{
		Url:          body.Url,
		Models:       body.Models,
		ValidatorKey: body.ValidatorKey,
		WorkerKey:    body.WorkerKey,
	}

	logging.Info("ValidatorKey in dapi", types.Participants, "key", body.ValidatorKey)
	if err := s.recorder.SubmitNewParticipant(msg); err != nil {
		logging.Error("Failed to submit MsgSubmitNewParticipant", types.Participants, "error", err)
		return err
	}

	return ctx.JSON(http.StatusOK, &ParticipantDto{
		Id:     msg.Creator,
		Url:    msg.Url,
		Models: msg.Models,
	})
}

func (s *Server) submitNewUnfundedParticipant(body SubmitUnfundedNewParticipantDto) error {
	msg := &inference.MsgSubmitNewUnfundedParticipant{
		Address:      body.Address,
		Url:          body.Url,
		Models:       body.Models,
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
