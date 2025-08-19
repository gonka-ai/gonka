package public

import (
	"net/http"

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

	var activeModels []types.Model
	parentEpochData := currentEpoch.GetEpochGroupData()

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
			activeModels = append(activeModels, *modelEpochData.EpochGroupData.ModelSnapshot)
		}
	}

	return ctx.JSON(http.StatusOK, &ModelsResponse{
		Models: activeModels,
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
