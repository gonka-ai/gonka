package public

import (
	"decentralized-api/logging"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/http"
	"net/url"
)

func (s *Server) getChatById(ctx echo.Context) error {
	logging.Debug("GetCompletion received", types.Inferences)
	encodedId := ctx.Param("id")
	if encodedId == "" {
		return ErrIdRequired
	}

	// URL decode the inference ID
	id, err := url.QueryUnescape(encodedId)
	if err != nil {
		logging.Error("Failed to decode inference ID", types.Inferences, "encodedId", encodedId, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid inference ID")
	}

	logging.Debug("GET inference", types.Inferences, "id", id)

	queryClient := s.recorder.NewInferenceQueryClient()
	response, err := queryClient.Inference(ctx.Request().Context(), &types.QueryGetInferenceRequest{Index: id})
	if err != nil {
		if grpcStatus, ok := status.FromError(err); ok && grpcStatus.Code() == codes.NotFound {
			logging.Debug("Inference not found", types.Inferences, "id", id, "error", err)
			return ErrInferenceNotFound
		}

		// return 500
		logging.Error("Failed to get inference", types.Inferences, "id", id, "error", err)
		return err
	}

	if response == nil {
		logging.Error("Inference not found", types.Inferences, "id", id)
		return ErrInferenceNotFound
	}

	return ctx.JSON(http.StatusOK, response.Inference)
}
