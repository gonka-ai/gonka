package main

import (
	"context"
	"decentralized-api/completionapi"
	"encoding/json"
	"google.golang.org/grpc"
	"inference/x/inference/types"
	"log"
	"time"
)

func StartValidationScheduledTask(transactionRecorder InferenceCosmosClient, config Config) {
	// Sleep but every X seconds wake up and do the task
	for {
		time.Sleep(5 * time.Second)
		// TODO: query transaction
		conn, err := grpc.NewClient(config.ChainNode.Url)
		if err != nil {
			log.Printf("Error creating grpc client: %v", err)
			continue
		}

		queryClient := types.NewQueryClient(conn)
		r, err := queryClient.Inference(context.Background(), &types.QueryGetInferenceRequest{Index: "1"})
		if err != nil {
			log.Printf("Failed to query a transaction for validation	: %v", err)
		}
		validate(r.Inference)
	}
}

func ValidateByInferenceId(id string, config Config) error {
	conn, err := grpc.NewClient(config.ChainNode.Url)
	if err != nil {
		log.Printf("Error creating grpc client: %v", err)
		return err
	}

	queryClient := types.NewQueryClient(conn)
	r, err := queryClient.Inference(context.Background(), &types.QueryGetInferenceRequest{Index: id})
	if err != nil {
		log.Printf("Failed get inference by id query. id = %s. err = %v", id, err)
	}

	return validate(r.Inference)
}

func validate(inference types.Inference) error {
	var requestMap map[string]interface{}
	if err := json.Unmarshal([]byte(inference.PromptPayload), &requestMap); err != nil {
		log.Printf("Failed to unmarshal PromptPayload. inferenceId = %v. err = %v", inference.InferenceId, err)
		return err
	}

	var response *completionapi.Response
	if err := json.Unmarshal([]byte(inference.ResponsePayload), response); err != nil {
		log.Printf("Failed to unmarshal ResponsePayload. inferenceId = %v. err = %v", inference.InferenceId, err)
		return err
	}

	//goland:noinspection GoDfaNilDereference
	requestMap["enforced_str"] = response.Choices[0].Message.Content

	// TODO: Send a request to node to validate the transaction
	return nil
}
