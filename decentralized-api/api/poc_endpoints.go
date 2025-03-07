package api

import (
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/internal/poc"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
	"strconv"
	"strings"
)

// /v1/poc-batches/generated
// /v1/poc-batches/validated
func WrapPoCBatches(recorder cosmos_client.CosmosMessageClient) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodPost:
			postPoCBatches(recorder, w, request)
		case http.MethodGet:
			getPoCBatches(recorder, w, request)
		default:
			logging.Error("Invalid request method", types.Server, "method", request.Method)
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		}
	}
}

func postPoCBatches(recorder cosmos_client.CosmosMessageClient, w http.ResponseWriter, request *http.Request) {
	suffix := strings.TrimPrefix(request.URL.Path, "/v1/poc-batches/")
	logging.Debug("postPoCBatches", types.PoC, "suffix", suffix)

	switch suffix {
	case "generated":
		submitPoCBatches(recorder, w, request)
	case "validated":
		submitValidatedPoCBatches(recorder, w, request)
	}
}

func submitPoCBatches(recorder cosmos_client.CosmosMessageClient, w http.ResponseWriter, request *http.Request) {
	var body poc.ProofBatch

	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		logging.Error("Failed to decode request body of type ProofBatch", types.PoC, "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	logging.Info("ProofBatch received", types.PoC, "body", body)

	msg := &inference.MsgSubmitPocBatch{
		PocStageStartBlockHeight: body.BlockHeight,
		Nonces:                   body.Nonces,
		Dist:                     body.Dist,
		BatchId:                  uuid.New().String(),
	}
	err := recorder.SubmitPocBatch(msg)
	if err != nil {
		logging.Error("Failed to submit MsgSubmitPocBatch", types.PoC, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func submitValidatedPoCBatches(recorder cosmos_client.CosmosMessageClient, w http.ResponseWriter, request *http.Request) {
	var body poc.ValidatedBatch

	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		logging.Error("Failed to decode request body of type ValidatedBatch", types.PoC, "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	logging.Info("ValidatedProofBatch received", types.PoC, "body", body)

	address, err := cosmos_client.PubKeyToAddress(body.PublicKey)
	if err != nil {
		logging.Error("Failed to convert public key to address", types.PoC, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	msg := &inference.MsgSubmitPocValidation{
		ParticipantAddress:       address,
		PocStageStartBlockHeight: body.BlockHeight,
		Nonces:                   body.Nonces,
		Dist:                     body.Dist,
		ReceivedDist:             body.ReceivedDist,
		RTarget:                  body.RTarget,
		FraudThreshold:           body.FraudThreshold,
		NInvalid:                 body.NInvalid,
		ProbabilityHonest:        body.ProbabilityHonest,
		FraudDetected:            body.FraudDetected,
	}

	err = recorder.SubmitPoCValidation(msg)
	if err != nil {
		logging.Error("Failed to submit MsgSubmitValidatedPocBatch", types.PoC, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	return
}

func getPoCBatches(recorder cosmos_client.CosmosMessageClient, w http.ResponseWriter, request *http.Request) {
	// Get what's after /v1/poc/batches/
	epoch := strings.TrimPrefix(request.URL.Path, "/v1/poc-batches/")
	logging.Debug("getPoCBatches", types.PoC, "epoch", epoch)

	// Parse int64 from epoch:
	value, err := strconv.ParseInt(epoch, 10, 64)
	if err != nil {
		logging.Error("Failed to parse epoch", types.PoC, "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	logging.Debug("Requesting PoC batches.", types.PoC, "epoch", value)

	queryClient := recorder.NewInferenceQueryClient()
	// ignite scaffold query pocBatchesForStage blockHeight:int
	response, err := queryClient.PocBatchesForStage(*recorder.GetContext(), &types.QueryPocBatchesForStageRequest{BlockHeight: value})
	if err != nil {
		logging.Error("Failed to get PoC batches.", types.PoC, "epoch", value)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if response == nil {
		logging.Error("PoC batches batches not found", types.PoC, "epoch", value)
		msg := fmt.Sprintf("PoC batches batches not found. epoch = %d", value)
		http.Error(w, msg, http.StatusNotFound)
		return
	}

	RespondWithJson(w, response)
}
