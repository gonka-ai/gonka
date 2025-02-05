package api

import (
	"decentralized-api/api/model"
	"decentralized-api/cosmosclient"
	"github.com/productscience/inference/api/inference/inference"
	"log/slog"
	"net/http"
)

// v1/admin/models
func WrapRegisterModel(cosmosClient cosmosclient.CosmosMessageClient) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		body, err := parseJsonBody[model.RegisterModelDto](request)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		msg := &inference.MsgRegisterModel{
			Id:                     body.Id,
			UnitsOfComputePerToken: body.UnitsOfComputePerToken,
		}

		// TODO: make it a function of cosmosClient interface?
		err = cosmosclient.SubmitProposal(cosmosClient, msg, 1000000)
		if err != nil {
			slog.Error("SubmitProposal failed", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}
