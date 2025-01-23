package api

import (
	"decentralized-api/apiconfig"
	cosmos_client "decentralized-api/cosmosclient"
	"fmt"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"log/slog"
	"net/http"
)

// v1/admin/unit-of-compute-price-proposal
func WrapUnitOfComputePriceProposal(cosmosClient cosmos_client.CosmosMessageClient, configManager *apiconfig.ConfigManager) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodPost:
			postUnitOfComputePriceProposal(cosmosClient, w, request)
		case http.MethodGet:
			getUnitOfComputePriceProposal(cosmosClient, w, request)
		default:
			slog.Error("Invalid request method", "method", request.Method, "path", request.URL.Path)
			msg := fmt.Sprintf("Invalid request method. method = %s. path = %s", request.Method, request.URL.Path)
			http.Error(w, msg, http.StatusMethodNotAllowed)
		}
	}
}

type UnitOfComputePriceProposalDto struct {
	Price uint64 `json:"price"`
}

func postUnitOfComputePriceProposal(cosmosClient cosmos_client.CosmosMessageClient, w http.ResponseWriter, request *http.Request) {
	body, err := parseJsonBody[UnitOfComputePriceProposalDto](request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	msg := &inference.MsgSubmitUnitOfComputePriceProposal{
		Price: body.Price,
	}

	if err := cosmosClient.SubmitUnitOfComputePriceProposal(msg); err != nil {
		slog.Error("Failed to send a transaction: MsgSubmitUnitOfComputePriceProposal", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func getUnitOfComputePriceProposal(cosmosClient cosmos_client.CosmosMessageClient, w http.ResponseWriter, request *http.Request) {
	queryClient := cosmosClient.NewInferenceQueryClient()

	queryRequest := &types.QueryGetUnitOfComputePriceProposalRequest{}
	queryResponse, err := queryClient.GetUnitOfComputePriceProposal(*cosmosClient.GetContext(), queryRequest)
	if err != nil {
		slog.Error("Failed to query unit of compute price proposal", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	dto := UnitOfComputePriceProposalDto{
		Price: queryResponse.Price,
	}
	RespondWithJson(w, dto)
}
