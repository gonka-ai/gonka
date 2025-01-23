package api

import (
	"decentralized-api/apiconfig"
	cosmos_client "decentralized-api/cosmosclient"
	"fmt"
	"log/slog"
	"net/http"
)

// v1/admin/unit-of-compute-price-proposal
func WrapUnitOfComputePriceProposal(cosmosClient cosmos_client.CosmosMessageClient, configManager *apiconfig.ConfigManager) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodPost:
			postUnitOfComputePriceProposal(cosmosClient, configManager)
		case http.MethodGet:
			getUnitOfComputePriceProposal(cosmosClient, configManager)
		default:
			slog.Error("Invalid request method", "method", request.Method, "path", request.URL.Path)
			msg := fmt.Sprintf("Invalid request method. method = %s. path = %s", request.Method, request.URL.Path)
			http.Error(w, msg, http.StatusMethodNotAllowed)
		}
	}
}

type UnitOfComputePriceProposalDto struct {
	Price string `json:"price"`
}

func postUnitOfComputePriceProposal(cosmosClient cosmos_client.CosmosMessageClient, configManager *apiconfig.ConfigManager) {
	configManager.GetConfig()
}

// ignite scaffold message submit-unit-of-compute-price-proposal price:uint
func getUnitOfComputePriceProposal(cosmosClient cosmos_client.CosmosMessageClient, configManager *apiconfig.ConfigManager) {

}
