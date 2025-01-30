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
	response, err := queryClient.CurrentEpochGroupData(*cosmosClient.GetContext(), req)
	if err != nil {
		http.Error(w, "Failed to get current epoch group data", http.StatusInternalServerError)
		return
	}

	var responseBody = &model.PricingDto{
		Price:  response.EpochGroupData.UnitOfComputePrice,
		Models: []model.ModelPriceDto{},
	}

	RespondWithJson(w, responseBody)
}
