package api

import (
	"decentralized-api/api/model"
	"decentralized-api/broker"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/training"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
	"strconv"
	"strings"
)

/*
	curl -X POST http://localhost:8080/v1/training-jobs \
		  -H "Content-Type: application/json" \
		  -d '{"hardware_resources": [{"type": "A100", "count": 1}, {"type": "T4", "count": 2}],"config": {"datasets": {"train": "train-dataset","test": "test-dataset"},"num_uoc_estimation_steps": 100}}'

curl -X GET http://localhost:8080/v1/training-jobs/1
*/
func WrapTraining(cosmosClient cosmosclient.CosmosMessageClient, broker *broker.Broker, executor *training.Executor) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodPost:
			if request.URL.Path == "/v1/training/tasks" {
				handleCreateTrainingJob(cosmosClient, w, request)
			} else if request.URL.Path == "/v1/training/lock-nodes" {
				handleLockTrainingNodes(cosmosClient, executor, w, request)
			} else {
				http.NotFound(w, request)
			}
		case http.MethodGet:
			// e.g. /v1/training/tasks/123
			pathParts := strings.Split(request.URL.Path, "/")
			// pathParts[0] = "", pathParts[1] = "v1", pathParts[2] = "training", pathParts[3] = "tasks", pathParts[4] = "{id}"
			if len(pathParts) == 5 && pathParts[1] == "v1" && pathParts[2] == "training" && pathParts[3] == "tasks" {
				handleGetTrainingJob(cosmosClient, pathParts[4], w, request)
			} else if len(pathParts) == 4 && pathParts[1] == "v1" && pathParts[2] == "training" && pathParts[3] == "tasks" {
				handleGetTrainingJobs(cosmosClient, w, request)
			} else {
				http.NotFound(w, request)
			}
		default:
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}
}

func handleGetTrainingJobs(client cosmosclient.CosmosMessageClient, w http.ResponseWriter, request *http.Request) {
	queryClient := client.NewInferenceQueryClient()
	tasks, err := queryClient.TrainingTaskAll(*client.GetContext(), &types.QueryTrainingTaskAllRequest{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	RespondWithJson(w, tasks)
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

	msgResponse, err := cosmosClient.CreateTrainingTask(msg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	RespondWithJson(w, msgResponse)
}

func handleGetTrainingJob(cosmosClient cosmosclient.CosmosMessageClient, id string, w http.ResponseWriter, r *http.Request) {
	logging.Info("GetTrainingJob", types.Training, "id", id)
	uintId, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		http.Error(w, "Invalid training job ID", http.StatusBadRequest)
		return
	}

	queryClient := cosmosClient.NewInferenceQueryClient()
	task, err := queryClient.TrainingTask(*cosmosClient.GetContext(), &types.QueryTrainingTaskRequest{Id: uintId})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	RespondWithJson(w, task)
}

// FIXME: Needs some kind of a proof that the requester is the assigner
func handleLockTrainingNodes(cosmosClient cosmosclient.CosmosMessageClient, executor *training.Executor, w http.ResponseWriter, r *http.Request) {
	body, err := parseJsonBody[model.LockTrainingNodesDto](r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err = executor.PreassignTask(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
