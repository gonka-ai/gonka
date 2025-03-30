package public

import (
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
)

func (s *Server) GetModels(ctx echo.Context) error {
	queryClient := s.recorder.NewInferenceQueryClient()
	context := *s.recorder.GetContext()

	modelsResponse, err := queryClient.ModelsAll(context, &types.QueryModelsAllRequest{})
	if err != nil {
		return err
	}

	return ctx.JSON(http.StatusOK, &ModelsResponse{
		Models: modelsResponse.Model,
	})
}
