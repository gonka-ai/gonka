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

func WrapModels(cosmosClient cosmosclient.CosmosMessageClient) http.HandlerFunc {
	return func(w http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			getModels(w, cosmosClient)
		default:
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		}
	}
}

func getPricing(w http.ResponseWriter, cosmosClient cosmosclient.CosmosMessageClient) {
	queryClient := cosmosClient.NewInferenceQueryClient()
	context := *cosmosClient.GetContext()
	req := &types.QueryCurrentEpochGroupDataRequest{}
	response, err := queryClient.CurrentEpochGroupData(context, req)
	// FIXME: handle epoch 0, there's a default price specifically for that,
	// 	but at the moment you just return 0 (since when epoch == 0 you get empty struct from CurrentEpochGroupData)
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
	for i, m := range modelsResponse.Model {
		pricePerToken := m.UnitsOfComputePerToken * unitOfComputePrice
		models[i] = model.ModelPriceDto{
			Id:                     m.Id,
			UnitsOfComputePerToken: m.UnitsOfComputePerToken,
			PricePerToken:          pricePerToken,
		}
	}

	var responseBody = &model.PricingDto{
		Price:  unitOfComputePrice,
		Models: models,
	}

	RespondWithJson(w, responseBody)
}

type ModelsResponse struct {
	Models []types.Model `json:"models"`
}

func getModels(w http.ResponseWriter, cosmosClient cosmosclient.CosmosMessageClient) {
	queryClient := cosmosClient.NewInferenceQueryClient()
	context := *cosmosClient.GetContext()

	modelsResponse, err := queryClient.ModelsAll(context, &types.QueryModelsAllRequest{})
	if err != nil {
		http.Error(w, "Failed to get models", http.StatusInternalServerError)
		return
	}

	rspBody := &ModelsResponse{
		Models: modelsResponse.Model,
	}

	RespondWithJson(w, rspBody)
}
