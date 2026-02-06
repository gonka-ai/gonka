package public

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

var (
	modelSlugPrefix             = "gonka"
	supportedSamplingParameters = []string{
		"temperature", "top_p", "top_k", "frequency_penalty", "presence_penalty", "stop", "seed",
	}
	supportedFeatures = []string{
		"logprobs",
	}
)

func pricingForModel(model *types.Model, unitOfComputePrice uint64) *ModelPricing {
	pricePerToken := model.UnitsOfComputePerToken * unitOfComputePrice
	priceStr := strconv.FormatUint(pricePerToken, 10)
	return &ModelPricing{
		Prompt:         priceStr,
		Completion:     priceStr,
		Request:        "0",
		Image:          "0",
		InputCacheRead: "0",
		Currency:       "ngonka",
	}
}

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
	unitOfComputePrice := uint64(parentEpochData.UnitOfComputePrice)

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
				ID:                          m.Id,
				HuggingFaceID:               m.HfRepo,
				Name:                        m.Id,
				Created:                     0,
				InputModalities:             []string{"text"},
				OutputModalities:            []string{"text"},
				ContextLength:               m.ContextWindow,
				MaxOutputLength:             m.ContextWindow,
				Pricing:                     pricingForModel(m, unitOfComputePrice),
				SupportedSamplingParameters: supportedSamplingParameters,
				SupportedFeatures:           supportedFeatures,
				Provider:                    &ModelMetadata{Slug: modelSlugPrefix},
			})
		}
	}

	// NOTE: Response uses {data:[...]} envelope (OpenRouter-compatible).
	return ctx.JSON(http.StatusOK, ModelsListResponse{
		Data: models,
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
