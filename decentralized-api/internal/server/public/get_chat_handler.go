package public

import (
	"decentralized-api/logging"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
)

func (s *Server) getChatById(ctx echo.Context) error {
	logging.Debug("GetCompletion received", types.Inferences)
	id := ctx.Param("id")
	if id == "" {
		return ErrIdRequired
	}

	logging.Debug("GET inference", types.Inferences, "id", id)

	queryClient := s.recorder.NewInferenceQueryClient()
	response, err := queryClient.Inference(ctx.Request().Context(), &types.QueryGetInferenceRequest{Index: id})
	if err != nil {
		logging.Error("Failed to get inference", types.Inferences, "id", id, "error", err)
		return err
	}

	if response == nil {
		logging.Error("Inference not found", types.Inferences, "id", id)
		return ErrInferenceNotFound
	}

	return ctx.JSON(http.StatusOK, response.Inference)
}
