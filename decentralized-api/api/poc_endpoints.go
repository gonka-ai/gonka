package api

import (
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/poc"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"log/slog"
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
			slog.Error("Invalid request method", "method", request.Method)
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		}
	}
}

func postPoCBatches(recorder cosmos_client.CosmosMessageClient, w http.ResponseWriter, request *http.Request) {
	suffix := strings.TrimPrefix(request.URL.Path, "/v1/poc-batches/")
	slog.Debug("postPoCBatches", "suffix", suffix)

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
		slog.Error("Failed to decode request body of type ProofBatch", "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slog.Info("ProofBatch received", "body", body)

	msg := &inference.MsgSubmitPocBatch{
		PocStageStartBlockHeight: body.BlockHeight,
		Nonces:                   body.Nonces,
		Dist:                     body.Dist,
		BatchId:                  uuid.New().String(),
	}
	err := recorder.SubmitPocBatch(msg)
	if err != nil {
		slog.Error("Failed to submit MsgSubmitPocBatch", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func submitValidatedPoCBatches(recorder cosmos_client.CosmosMessageClient, w http.ResponseWriter, request *http.Request) {
	var body poc.ValidatedBatch

	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		slog.Error("Failed to decode request body of type ValidatedBatch", "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slog.Info("ValidatedProofBatch received", "body", body)

	address, err := cosmos_client.PubKeyToAddress(body.PublicKey)
	if err != nil {
		slog.Error("Failed to convert public key to address", "error", err)
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
		slog.Error("Failed to submit MsgSubmitValidatedPocBatch", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	return
}

func getPoCBatches(recorder cosmos_client.CosmosMessageClient, w http.ResponseWriter, request *http.Request) {
	// Get what's after /v1/poc/batches/
	epoch := strings.TrimPrefix(request.URL.Path, "/v1/poc-batches/")
	slog.Debug("getPoCBatches", "epoch", epoch)

	// Parse int64 from epoch:
	value, err := strconv.ParseInt(epoch, 10, 64)
	if err != nil {
		slog.Error("Failed to parse epoch", "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slog.Debug("Requesting PoC batches.", "epoch", value)

	queryClient := recorder.NewInferenceQueryClient()
	// ignite scaffold query pocBatchesForStage blockHeight:int
	response, err := queryClient.PocBatchesForStage(*recorder.GetContext(), &types.QueryPocBatchesForStageRequest{BlockHeight: value})
	if err != nil {
		slog.Error("Failed to get PoC batches.", "epoch", value)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if response == nil {
		slog.Error("PoC batches batches not found", "epoch", value)
		msg := fmt.Sprintf("PoC batches batches not found. epoch = %d", value)
		http.Error(w, msg, http.StatusNotFound)
		return
	}

	RespondWithJson(w, response)
}
