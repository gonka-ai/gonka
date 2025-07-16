package public

import (
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
)

func (s *Server) getPricing(ctx echo.Context) error {
	queryClient := s.recorder.NewInferenceQueryClient()
	context := s.recorder.GetContext()
	req := &types.QueryCurrentEpochGroupDataRequest{}
	response, err := queryClient.CurrentEpochGroupData(context, req)
	// FIXME: handle epoch 0, there's a default price specifically for that,
	// 	but at the moment you just return 0 (since when epoch == 0 you get empty struct from CurrentEpochGroupData)
	if err != nil {
		return err
	}
	unitOfComputePrice := response.EpochGroupData.UnitOfComputePrice

	modelsResponse, err := queryClient.ModelsAll(context, &types.QueryModelsAllRequest{})
	if err != nil {
		return err
	}

	models := make([]ModelPriceDto, len(modelsResponse.Model))
	for i, m := range modelsResponse.Model {
		pricePerToken := m.UnitsOfComputePerToken * uint64(unitOfComputePrice)
		models[i] = ModelPriceDto{
			Id:                     m.Id,
			UnitsOfComputePerToken: m.UnitsOfComputePerToken,
			PricePerToken:          pricePerToken,
		}
	}

	return ctx.JSON(http.StatusOK, &PricingDto{
		Price:  uint64(unitOfComputePrice),
		Models: models,
	})
}
