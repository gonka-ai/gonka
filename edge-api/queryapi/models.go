package queryapi

import (
	"context"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	inferencetypes "github.com/productscience/inference/x/inference/types"

	"common/chain"
	"edge-api/queryapi/gen"
)

// Ported from decentralized-api/internal/server/public/get_models_handler.go:10
func (h *Handlers) GetModels(ctx echo.Context) error {
	qc := h.chain.InferenceQueryClient()
	reqCtx := ctx.Request().Context()

	// Get the current epoch group to find out which models are active.
	currentEpoch, err := qc.CurrentEpochGroupData(reqCtx, &inferencetypes.QueryCurrentEpochGroupDataRequest{})
	if err != nil {
		return grpcErrorToHTTP(err)
	}

	parentEpochData := currentEpoch.GetEpochGroupData()
	models := make([]gen.ModelDescriptor, 0, len(parentEpochData.SubGroupModels))
	createdAt := time.Now().Unix()

	// Iterate over the subgroup models to get the snapshot for each one.
	for _, modelId := range parentEpochData.SubGroupModels {
		req := &inferencetypes.QueryGetEpochGroupDataRequest{
			EpochIndex: parentEpochData.EpochIndex,
			ModelId:    modelId,
		}
		modelEpochData, err := qc.EpochGroupData(reqCtx, req)
		if err != nil {
			// If a model subgroup is listed but not found, skip it without failing the whole request.
			continue
		}

		if modelEpochData.EpochGroupData.ModelSnapshot != nil {
			m := modelEpochData.EpochGroupData.ModelSnapshot
			object := "model"
			desc := gen.ModelDescriptor{
				Object:           &object,
				Id:               m.Id,
				Name:             m.Id,
				Created:          createdAt,
				InputModalities:  []string{"text"},
				OutputModalities: []string{"text"},
				ContextLength:    m.ContextWindow,
				MaxOutputLength:  m.ContextWindow,
			}
			if m.HfRepo != "" {
				hfRepo := m.HfRepo
				desc.HuggingFaceId = &hfRepo
			}
			models = append(models, desc)
		}
	}

	return ctx.JSON(http.StatusOK, gen.ModelsListResponse{
		Object: "list",
		Data:   models,
	})
}

// Ported from decentralized-api/internal/server/public/get_models_handler.go:45
func (h *Handlers) GetGovernanceModels(ctx echo.Context) error {
	qc := h.chain.InferenceQueryClient()
	reqCtx := ctx.Request().Context()

	modelsResponse, err := qc.ModelsAll(reqCtx, &inferencetypes.QueryModelsAllRequest{})
	if err != nil {
		return grpcErrorToHTTP(err)
	}

	return ctx.JSON(http.StatusOK, governanceModelsResponse{
		Models: modelsResponse.Model,
	})
}

// Ported from decentralized-api/internal/server/public/get_models_handler.go:62
// TODO: Remove later - response format used by old dashboard.
// GetGovernanceModelsLegacy is a temporary compatibility endpoint.
// It mirrors governance models but preserves the legacy chain-gateway field name: "model".
func (h *Handlers) GetGovernanceModelsLegacy(ctx echo.Context) error {
	qc := h.chain.InferenceQueryClient()
	reqCtx := ctx.Request().Context()

	modelsResponse, err := qc.ModelsAll(reqCtx, &inferencetypes.QueryModelsAllRequest{})
	if err != nil {
		return grpcErrorToHTTP(err)
	}

	return ctx.JSON(http.StatusOK, map[string]any{
		"model": modelsResponse.Model,
	})
}

// Ported from decentralized-api/internal/server/public/get_pricing_handler.go:14
func (h *Handlers) GetPricing(ctx echo.Context) error {
	qc := h.chain.InferenceQueryClient()
	reqCtx := ctx.Request().Context()

	// FIXME: handle epoch 0, there's a default price specifically for that,
	// but at the moment you just return 0 (since when epoch == 0 you get empty struct from CurrentEpochGroupData)
	response, err := qc.CurrentEpochGroupData(reqCtx, &inferencetypes.QueryCurrentEpochGroupDataRequest{})
	if err != nil {
		return grpcErrorToHTTP(err)
	}

	unitOfComputePrice := response.EpochGroupData.UnitOfComputePrice
	parentEpochData := response.GetEpochGroupData()
	models := make([]gen.ModelPriceDto, 0, len(parentEpochData.SubGroupModels))

	for _, modelId := range parentEpochData.SubGroupModels {
		req := &inferencetypes.QueryGetEpochGroupDataRequest{
			EpochIndex: parentEpochData.EpochIndex,
			ModelId:    modelId,
		}
		modelEpochData, err := qc.EpochGroupData(reqCtx, req)
		if err != nil {
			continue
		}

		if modelEpochData.EpochGroupData.ModelSnapshot != nil {
			m := modelEpochData.EpochGroupData.ModelSnapshot
			pricePerToken := m.UnitsOfComputePerToken * uint64(unitOfComputePrice)
			models = append(models, gen.ModelPriceDto{
				Id:                     m.Id,
				UnitsOfComputePerToken: m.UnitsOfComputePerToken,
				PricePerToken:          pricePerToken,
			})
		}
	}

	return ctx.JSON(http.StatusOK, gen.PricingDto{
		UnitOfComputePrice: uint64(unitOfComputePrice),
		Models:             models,
	})
}

// Ported from decentralized-api/internal/server/public/get_pricing_handler.go:56
// Change: Utilization per model is not populated — original derived it from a stats-storage
// service (statsStorage.GetModelStatsByTime) that is not available in this package.
// Capacity is populated via GetAllModelCapacities when dynamic pricing is enabled.
func (h *Handlers) GetGovernancePricing(ctx echo.Context) error {
	qc := h.chain.InferenceQueryClient()
	reqCtx := ctx.Request().Context()

	// Get the unit of compute price from the latest epoch data, as this is always the most current price.
	response, err := qc.CurrentEpochGroupData(reqCtx, &inferencetypes.QueryCurrentEpochGroupDataRequest{})
	if err != nil {
		// In case of an error (e.g., first epoch), we might not have a price yet.
		return grpcErrorToHTTP(err)
	}
	unitOfComputePrice := response.EpochGroupData.UnitOfComputePrice

	// Get all governance models to calculate their pricing.
	modelsResponse, err := qc.ModelsAll(reqCtx, &inferencetypes.QueryModelsAllRequest{})
	if err != nil {
		return grpcErrorToHTTP(err)
	}

	// Check if dynamic pricing is enabled by querying per-token prices from the chain.
	dynamicPricingEnabled, dynamicPrices := getChainDynamicPrices(reqCtx, qc)

	// Load per-model capacities when dynamic pricing is enabled.
	var capacities map[string]int
	if dynamicPricingEnabled {
		capacities = getChainModelCapacities(reqCtx, qc)
	}

	models := make([]gen.ModelPriceDto, len(modelsResponse.Model))
	for i, m := range modelsResponse.Model {
		// Legacy price calculation.
		legacyPricePerToken := m.UnitsOfComputePerToken * uint64(unitOfComputePrice)

		modelDto := gen.ModelPriceDto{
			Id:                     m.Id,
			UnitsOfComputePerToken: m.UnitsOfComputePerToken,
			PricePerToken:          legacyPricePerToken,
		}

		// Override with dynamic price and attach capacity if available.
		// TODO: Note: Utilization is not populated — it required a stats-storage dependency
		// (statsStorage.GetModelStatsByTime) that is not available in this package.
		if dynamicPricingEnabled {
			if dynamicPrice, exists := dynamicPrices[m.Id]; exists {
				modelDto.PricePerToken = dynamicPrice
			}
			if capacity, exists := capacities[m.Id]; exists {
				modelDto.Capacity = &capacity
			}
		}

		models[i] = modelDto
	}

	return ctx.JSON(http.StatusOK, gen.PricingDto{
		UnitOfComputePrice:    uint64(unitOfComputePrice),
		Models:                models,
		DynamicPricingEnabled: dynamicPricingEnabled,
	})
}

// getChainDynamicPrices queries per-token model prices from the chain KV store.
// Returns (enabled=true, prices) when the chain has dynamic prices configured,
// or (enabled=false, nil) if no dynamic prices exist or the query fails.
func getChainDynamicPrices(ctx context.Context, qc chain.InferenceClient) (bool, map[string]uint64) {
	pricesResponse, err := qc.GetAllModelPerTokenPrices(ctx, &inferencetypes.QueryGetAllModelPerTokenPricesRequest{})
	if err != nil || len(pricesResponse.ModelPrices) == 0 {
		return false, nil
	}

	modelPrices := make(map[string]uint64, len(pricesResponse.ModelPrices))
	for _, mp := range pricesResponse.ModelPrices {
		modelPrices[mp.ModelId] = mp.Price
	}
	return true, modelPrices
}

// getChainModelCapacities queries per-model capacity values from the chain.
// Returns an empty map if the query fails or returns no data.
func getChainModelCapacities(ctx context.Context, qc chain.InferenceClient) map[string]int {
	resp, err := qc.GetAllModelCapacities(ctx, &inferencetypes.QueryGetAllModelCapacitiesRequest{})
	if err != nil || len(resp.ModelCapacities) == 0 {
		return map[string]int{}
	}
	result := make(map[string]int, len(resp.ModelCapacities))
	for _, mc := range resp.ModelCapacities {
		result[mc.ModelId] = int(mc.Capacity)
	}
	return result
}
