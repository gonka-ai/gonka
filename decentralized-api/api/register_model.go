package api

import (
	"decentralized-api/api/model"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"fmt"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
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

		authority := cosmosclient.GetProposalMsgSigner()
		logging.Info("RegisterModel", types.Inferences, "authority", authority)
		msg := &inference.MsgRegisterModel{
			Authority:              authority,
			ProposedBy:             cosmosClient.GetAddress(),
			Id:                     body.Id,
			UnitsOfComputePerToken: body.UnitsOfComputePerToken,
		}

		proposalData := &cosmosclient.ProposalData{
			Metadata:  "Created via decentralized-api",
			Title:     fmt.Sprintf("%s model proposal", body.Id),
			Summary:   fmt.Sprintf("This proposal suggests to serve a model %s and estimates it will take %d units of compute per token", body.Id, body.UnitsOfComputePerToken),
			Expedited: false,
		}

		// TODO: make it a function of cosmosClient interface?
		err = cosmosclient.SubmitProposal(cosmosClient, msg, proposalData)
		if err != nil {
			logging.Error("SubmitProposal failed", types.Inferences, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}
