package main

import (
	"bytes"
	"context"
	"decentralized-api/broker"
	"encoding/json"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"inference/x/inference/types"
	"io"
	"log"
	"net/http"
	"net/url"
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
		log.Printf("Inference to validate: %v", r.Inference)
		//validate(r.Inference)
	}
}

func ValidateByInferenceId(id string, node *broker.InferenceNode, config Config) error {
	nodeUrl, err := url.Parse(config.ChainNode.Url)
	if err != nil {
		return err
	}

	log.Printf("Trying to open a connection to %s", nodeUrl.Host)
	conn, err := grpc.NewClient(
		nodeUrl.Host,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Printf("Error creating grpc client: %v", err)
		return err
	}

	// Construct the request message
	req := &types.QueryGetInferenceRequest{
		Index: id,
	}

	// Prepare the response message
	var r types.QueryGetInferenceResponse
	err = conn.Invoke(context.Background(), "Inference", req, &r)

	//queryClient := types.NewQueryClient(conn)
	//r, err := queryClient.Inference(context.Background(), &types.QueryGetInferenceRequest{Index: id})
	if err != nil {
		log.Printf("Failed get inference by id query. id = %s. err = %v", id, err)
	}

	return validate(r.Inference, node)
}

func validate(inference types.Inference, inferenceNode *broker.InferenceNode) error {
	var requestMap map[string]interface{}
	if err := json.Unmarshal([]byte(inference.PromptPayload), &requestMap); err != nil {
		log.Printf("Failed to unmarshal PromptPayload. inferenceId = %v. err = %v", inference.InferenceId, err)
		return err
	}

	var response broker.Response
	if err := json.Unmarshal([]byte(inference.ResponsePayload), &response); err != nil {
		log.Printf("Failed to unmarshal ResponsePayload. inferenceId = %v. err = %v", inference.InferenceId, err)
		return err
	}

	//goland:noinspection GoDfaNilDereference
	requestMap["enforced_str"] = response.Choices[0].Message.Content

	// Serialize requestMap to JSON
	requestBody, err := json.Marshal(requestMap)
	if err != nil {
		return err
	}

	resp, err := http.Post(
		inferenceNode.Url+"v1/chat/completions",
		"application/json",
		bytes.NewReader(requestBody),
	)
	if err != nil {
		return err
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	log.Printf("responseValidation = %v", string(bodyBytes))
	var responseValidation broker.Response
	if err := json.Unmarshal(bodyBytes, &responseValidation); err != nil {
		return err
	}

	// TODO: Send a request to inferenceNode to validate the transaction
	return nil
}
