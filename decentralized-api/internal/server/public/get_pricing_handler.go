package public

import (
	"context"
	"decentralized-api/logging"
	"net/http"
	"time"

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
			EpochIndex: parentEpochData.EpochIndex,
			ModelId:    modelId,
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
	context := s.recorder.GetContext()

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

	// Check if dynamic pricing is enabled and get dynamic pricing data
	dynamicPricingEnabled, dynamicPrices, err := s.getDynamicPricingData()
	if err != nil {
		logging.Warn("Failed to get dynamic pricing data, falling back to legacy pricing", types.Pricing, "error", err)
		dynamicPricingEnabled = false
	}

	// Get utilization data if dynamic pricing is enabled
	var modelMetrics map[string]ModelMetrics
	if dynamicPricingEnabled {
		modelMetrics = s.getModelMetrics(queryClient, context)
	}

	models := make([]ModelPriceDto, len(modelsResponse.Model))
	for i, m := range modelsResponse.Model {
		// Legacy price calculation
		legacyPricePerToken := m.UnitsOfComputePerToken * uint64(unitOfComputePrice)

		modelDto := ModelPriceDto{
			Id:                     m.Id,
			UnitsOfComputePerToken: m.UnitsOfComputePerToken,
			PricePerToken:          legacyPricePerToken,
		}

		// Use dynamic pricing if available, otherwise keep legacy price
		if dynamicPricingEnabled {
			if dynamicPrice, exists := dynamicPrices[m.Id]; exists {
				// Override with current dynamic price
				modelDto.PricePerToken = dynamicPrice
			}

			// Add capacity and utilization information from preloaded data
			if metrics, exists := modelMetrics[m.Id]; exists {
				capacity := int64(metrics.Capacity)
				modelDto.Capacity = &capacity
				modelDto.Utilization = &metrics.Utilization
			}
		}

		models[i] = modelDto
	}

	return ctx.JSON(http.StatusOK, &PricingDto{
		Price:                 uint64(unitOfComputePrice),
		Models:                models,
		DynamicPricingEnabled: dynamicPricingEnabled,
	})
}

// ModelMetrics contains utilization and capacity data for a model
type ModelMetrics struct {
	Utilization float64
	Capacity    uint64
}

type modelTokenStats map[string]int64

// getModelMetrics calculates utilization and gets capacity for all models in one go
func (s *Server) getModelMetrics(queryClient types.QueryClient, context context.Context) map[string]ModelMetrics {
	metricsData := make(map[string]ModelMetrics)

	// Get all model capacities in one request
	capacitiesResponse, err := queryClient.GetAllModelCapacities(context, &types.QueryGetAllModelCapacitiesRequest{})
	if err != nil {
		logging.Warn("Failed to get model capacities", types.Pricing, "error", err)
		return metricsData
	}

	// Create capacity lookup map
	capacityMap := make(map[string]uint64)
	for _, modelCapacity := range capacitiesResponse.ModelCapacities {
		capacityMap[modelCapacity.ModelId] = modelCapacity.Capacity
		// Initialize metrics with capacity (utilization will be calculated next)
		metricsData[modelCapacity.ModelId] = ModelMetrics{
			Capacity:    modelCapacity.Capacity,
			Utilization: 0.0, // Default to 0, will be updated if stats available
		}
	}

	// Get dynamic pricing parameters for time window
	params, err := queryClient.Params(context, &types.QueryParamsRequest{})
	if err != nil || params.Params.DynamicPricingParams == nil {
		return metricsData // Return with capacity data only
	}
	windowDurationSeconds := int64(params.Params.DynamicPricingParams.UtilizationWindowDuration)

	localModelStats, hasLocalStats := s.getModelTokenStatsFromLocalStore(context, windowDurationSeconds)
	effectiveStats := localModelStats
	if !hasLocalStats {
		chainModelStats, err := s.getModelTokenStatsFromChain(queryClient, context, windowDurationSeconds)
		if err != nil {
			logging.Warn("Failed to get model stats for utilization", types.Pricing, "error", err)
			return metricsData
		}
		effectiveStats = chainModelStats
	} else if hasMissingModelStats(capacityMap, localModelStats) {
		chainModelStats, err := s.getModelTokenStatsFromChain(queryClient, context, windowDurationSeconds)
		if err != nil {
			logging.Warn("Failed to get missing model stats from chain, using local-only stats", types.Pricing, "error", err)
		} else {
			effectiveStats = mergeModelTokenStatsForMissingModels(capacityMap, localModelStats, chainModelStats)
		}
	}

	for modelID, totalTokens := range effectiveStats {
		if capacity, exists := capacityMap[modelID]; exists && capacity > 0 {
			utilization := float64(totalTokens) / float64(capacity)
			metricsData[modelID] = ModelMetrics{
				Capacity:    capacity,
				Utilization: utilization,
			}
		}
	}

	return metricsData
}

func hasMissingModelStats(capacityMap map[string]uint64, stats modelTokenStats) bool {
	for modelID := range capacityMap {
		if _, exists := stats[modelID]; !exists {
			return true
		}
	}
	return false
}

func mergeModelTokenStatsForMissingModels(
	capacityMap map[string]uint64,
	localStats modelTokenStats,
	chainStats modelTokenStats,
) modelTokenStats {
	merged := make(modelTokenStats, len(localStats))
	for modelID, totalTokens := range localStats {
		merged[modelID] = totalTokens
	}

	for modelID := range capacityMap {
		if _, exists := merged[modelID]; exists {
			continue
		}
		if totalTokens, exists := chainStats[modelID]; exists {
			merged[modelID] = totalTokens
		}
	}

	return merged
}

func (s *Server) getModelTokenStatsFromLocalStore(ctx context.Context, windowDurationSeconds int64) (modelTokenStats, bool) {
	if s.configManager == nil || s.configManager.SqlDb() == nil || s.configManager.SqlDb().GetDb() == nil {
		return nil, false
	}

	nowMillis := time.Now().UnixMilli()
	fromMillis := nowMillis - windowDurationSeconds*1000

	rows, err := s.configManager.SqlDb().GetDb().QueryContext(
		ctx,
		`SELECT model, COALESCE(SUM(total_tokens), 0)
		 FROM inference_finished_stats
		 WHERE end_timestamp_ms >= ? AND end_timestamp_ms <= ?
		 GROUP BY model`,
		fromMillis,
		nowMillis,
	)
	if err != nil {
		logging.Warn("Failed to read model stats from local store", types.Pricing, "error", err)
		return nil, false
	}
	defer rows.Close()

	stats := make(modelTokenStats)
	for rows.Next() {
		var (
			model      string
			totalToken int64
		)
		if err := rows.Scan(&model, &totalToken); err != nil {
			logging.Warn("Failed to scan model stats row from local store", types.Pricing, "error", err)
			return nil, false
		}
		stats[model] = totalToken
	}
	if err := rows.Err(); err != nil {
		logging.Warn("Failed while iterating local model stats", types.Pricing, "error", err)
		return nil, false
	}
	if len(stats) == 0 {
		return nil, false
	}
	return stats, true
}

func (s *Server) getModelTokenStatsFromChain(
	queryClient types.QueryClient,
	ctx context.Context,
	windowDurationSeconds int64,
) (modelTokenStats, error) {
	currentTimeMillis := time.Now().UnixMilli()
	timeWindowStartMillis := currentTimeMillis - windowDurationSeconds*1000

	statsResponse, err := queryClient.InferencesAndTokensStatsByModels(ctx, &types.QueryInferencesAndTokensStatsByModelsRequest{
		TimeFrom: timeWindowStartMillis,
		TimeTo:   currentTimeMillis,
	})
	if err != nil {
		return nil, err
	}

	stats := make(modelTokenStats)
	for _, modelStat := range statsResponse.StatsModels {
		stats[modelStat.Model] = modelStat.AiTokens
	}
	return stats, nil
}

// getDynamicPricingData queries dynamic pricing information from the chain
func (s *Server) getDynamicPricingData() (bool, map[string]uint64, error) {
	queryClient := s.recorder.NewInferenceQueryClient()
	context := s.recorder.GetContext()

	// Get all model prices directly from the chain's KV storage
	pricesResponse, err := queryClient.GetAllModelPerTokenPrices(context, &types.QueryGetAllModelPerTokenPricesRequest{})
	if err != nil {
		return false, nil, err
	}

	// Convert to map format
	modelPrices := make(map[string]uint64)
	for _, modelPrice := range pricesResponse.ModelPrices {
		modelPrices[modelPrice.ModelId] = modelPrice.Price
	}

	// If no prices returned, dynamic pricing is not enabled/working
	if len(modelPrices) == 0 {
		return false, nil, nil
	}

	return true, modelPrices, nil
}
