package public

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
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

	parentEpochData := response.GetEpochGroupData()
	models := make([]ModelPriceDto, 0, len(parentEpochData.SubGroupModels))

	for _, modelId := range parentEpochData.SubGroupModels {
		req := &types.QueryGetEpochGroupDataRequest{
			PocStartBlockHeight: parentEpochData.PocStartBlockHeight,
			ModelId:             modelId,
		}
		modelEpochData, err := queryClient.EpochGroupData(context, req)
		if err != nil {
			continue
		}

		if modelEpochData.EpochGroupData.ModelSnapshot != nil {
			m := modelEpochData.EpochGroupData.ModelSnapshot
			pricePerToken := m.UnitsOfComputePerToken * uint64(unitOfComputePrice)
			models = append(models, ModelPriceDto{
				Id:                     m.Id,
				UnitsOfComputePerToken: m.UnitsOfComputePerToken,
				PricePerToken:          pricePerToken,
			})
		}
	}

	return ctx.JSON(http.StatusOK, &PricingDto{
		Price:  uint64(unitOfComputePrice),
		Models: models,
	})
}

func (s *Server) getGovernancePricing(ctx echo.Context) error {
	queryClient := s.recorder.NewInferenceQueryClient()
	context := *s.recorder.GetContext()

	// Get the unit of compute price from the latest epoch data, as this is always the most current price.
	response, err := queryClient.CurrentEpochGroupData(context, &types.QueryCurrentEpochGroupDataRequest{})
	if err != nil {
		// In case of an error (e.g., first epoch), we might not have a price yet. Default to 0.
		return err
	}
	unitOfComputePrice := response.EpochGroupData.UnitOfComputePrice

	// Get all governance models to calculate their pricing.
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
