package public

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

func (s *Server) getModels(ctx echo.Context) error {
	queryClient := s.recorder.NewInferenceQueryClient()
	context := s.recorder.GetContext()

	// Get the current epoch group to find out which models are active.
	currentEpoch, err := queryClient.CurrentEpochGroupData(context, &types.QueryCurrentEpochGroupDataRequest{})
	if err != nil {
		return err
	}

	models := make([]ModelDescriptor, 0)
	parentEpochData := currentEpoch.GetEpochGroupData()
	createdAt := time.Now().Unix()

	// Iterate over the subgroup models to get the snapshot for each one.
	for _, modelId := range parentEpochData.SubGroupModels {
		req := &types.QueryGetEpochGroupDataRequest{
			EpochIndex: parentEpochData.EpochIndex,
			ModelId:    modelId,
		}
		modelEpochData, err := queryClient.EpochGroupData(context, req)
		if err != nil {
			// If a model subgroup is listed but not found, we can log it, but we shouldn't fail the entire request.
			continue
		}

		if modelEpochData.EpochGroupData.ModelSnapshot != nil {
			m := modelEpochData.EpochGroupData.ModelSnapshot
			models = append(models, ModelDescriptor{
				Object:           "model",
				ID:               m.Id,
				HuggingFaceID:    m.HfRepo,
				Name:             m.Id,
				Created:          createdAt,
				InputModalities:  []string{"text"},
				OutputModalities: []string{"text"},
				ContextLength:    m.ContextWindow,
				MaxOutputLength:  m.ContextWindow,
			})
		}
	}

	return ctx.JSON(http.StatusOK, ModelsListResponse{
		Object: "list",
		Data:   models,
	})
}

func (s *Server) getGovernanceModels(ctx echo.Context) error {
	queryClient := s.recorder.NewInferenceQueryClient()
	context := s.recorder.GetContext()

	modelsResponse, err := queryClient.ModelsAll(context, &types.QueryModelsAllRequest{})
	if err != nil {
		return err
	}

	return ctx.JSON(http.StatusOK, &ModelsResponse{
		Models: modelsResponse.Model,
	})
}

// TODO: Remove later - response format used by old dashboard
// getGovernanceModelsLegacy is a temporary compatibility endpoint.
// It mirrors governance models but preserves the legacy chain-gateway field name: "model".
func (s *Server) getGovernanceModelsLegacy(ctx echo.Context) error {
	queryClient := s.recorder.NewInferenceQueryClient()
	context := s.recorder.GetContext()

	modelsResponse, err := queryClient.ModelsAll(context, &types.QueryModelsAllRequest{})
	if err != nil {
		return err
	}

	return ctx.JSON(http.StatusOK, map[string]interface{}{
		"model": modelsResponse.Model,
	})
}
