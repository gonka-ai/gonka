package api

import (
	"decentralized-api/cosmosclient"
	"github.com/productscience/inference/api/inference/inference"
	"net/http"
)

type RegisterModelDto struct {
	ModelId               string `json:"model_id"`
	UnitOfComputePerToken uint64 `json:"unit_of_compute_per_token"`
}

// v1/admin/register-model
func WrapRegisterModel(cosmosClient cosmosclient.CosmosMessageClient) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		body, err := parseJsonBody[RegisterModelDto](request)
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
