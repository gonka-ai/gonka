package api

import (
	"decentralized-api/api/model"
	"decentralized-api/cosmosclient"
	"github.com/productscience/inference/api/inference/inference"
	"log/slog"
	"net/http"
	"strings"
)

func WrapTraining(cosmosClient cosmosclient.CosmosMessageClient) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodPost:
			if request.URL.Path == "/v1/training-jobs" {
				handleCreateTrainingJob(cosmosClient, w, request)
			} else {
				http.NotFound(w, request)
			}
		case http.MethodGet:
			// e.g. /v1/training-jobs/123
			pathParts := strings.Split(request.URL.Path, "/")
			// pathParts[0] = "", pathParts[1] = "v1", pathParts[2] = "training-jobs", pathParts[3] = "{id}"
			if len(pathParts) == 4 && pathParts[1] == "v1" && pathParts[2] == "training-jobs" {
				handleGetTrainingJob(cosmosClient, pathParts[3], w, request)
			} else {
				http.NotFound(w, request)
			}
		default:
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}
}

func handleCreateTrainingJob(cosmosClient cosmosclient.CosmosMessageClient, w http.ResponseWriter, r *http.Request) {
	body, err := parseJsonBody[model.StartTrainingDto](r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var hardwareResources = make([]*inference.TrainingHardwareResources, len(body.HardwareResources))
	for i, hr := range body.HardwareResources {
		hardwareResources[i] = &inference.TrainingHardwareResources{
			Type_: hr.Type,
			Count: hr.Count,
		}
	}

	msg := &inference.MsgCreateTrainingTask{
		HardwareResources: hardwareResources,
		Config: &inference.TrainingConfig{
			Datasets: &inference.TrainingDatasets{
				Train: body.Config.Datasets.Train,
				Test:  body.Config.Datasets.Test,
			},
			NumUocEstimationSteps: body.Config.NumUocEstimationSteps,
		},
	}

	err = cosmosClient.CreateTrainingTask(msg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func handleGetTrainingJob(cosmosClient cosmosclient.CosmosMessageClient, id string, w http.ResponseWriter, r *http.Request) {
	slog.Info("GetTrainingJob", "id", id)
	queryClient := cosmosClient.NewInferenceQueryClient()
	_ = queryClient
}
