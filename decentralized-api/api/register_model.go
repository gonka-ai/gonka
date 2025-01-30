package api

import (
	"decentralized-api/api/model"
	"decentralized-api/cosmosclient"
	"github.com/productscience/inference/api/inference/inference"
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
			Id:                    body.ModelId,
			UnitOfComputePerToken: body.UnitOfComputePerToken,
		}
		err = cosmosClient.RegisterModel(msg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}
