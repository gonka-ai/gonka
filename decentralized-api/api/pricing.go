package api

import (
	"decentralized-api/api/model"
	"decentralized-api/cosmosclient"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
)

func WrapPricing(cosmosClient cosmosclient.CosmosMessageClient) http.HandlerFunc {
	return func(w http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			getPricing(w, cosmosClient)
		default:
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		}
	}
}

func getPricing(w http.ResponseWriter, cosmosClient cosmosclient.CosmosMessageClient) {
	queryClient := cosmosClient.NewInferenceQueryClient()
	req := &types.QueryCurrentEpochGroupDataRequest{}
	context := *cosmosClient.GetContext()
	response, err := queryClient.CurrentEpochGroupData(context, req)
	if err != nil {
		http.Error(w, "Failed to get current epoch group data", http.StatusInternalServerError)
		return
	}
	unitOfComputePrice := response.EpochGroupData.UnitOfComputePrice

	modelsResponse, err := queryClient.ModelsAll(context, &types.QueryModelsAllRequest{})
	if err != nil {
		http.Error(w, "Failed to get models", http.StatusInternalServerError)
		return
	}

	models := make([]model.ModelPriceDto, len(modelsResponse.Model))
	for _, m := range modelsResponse.Model {
		pricePerToken := m.UnitsOfComputePerToken * unitOfComputePrice
		models = append(models, model.ModelPriceDto{
			Id:                     m.Id,
			UnitsOfComputePerToken: m.UnitsOfComputePerToken,
			PricePerToken:          pricePerToken,
		})
	}

	var responseBody = &model.PricingDto{
		Price:  unitOfComputePrice,
		Models: models,
	}

	RespondWithJson(w, responseBody)
}
