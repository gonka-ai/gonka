package main

import (
	"bytes"
	"context"
	"decentralized-api/broker"
	"encoding/json"
	"google.golang.org/grpc"
	"inference/x/inference/types"
	"io"
	"log"
	"net/http"
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

func ValidateByInferenceId(id string, node *broker.InferenceNode, transactionRecorder InferenceCosmosClient) error {
	queryClient := types.NewQueryClient(transactionRecorder.client.Context())
	r, err := queryClient.Inference(context.Background(), &types.QueryGetInferenceRequest{Index: id})
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

	var originalResponse broker.Response
	if err := json.Unmarshal([]byte(inference.ResponsePayload), &originalResponse); err != nil {
		log.Printf("Failed to unmarshal ResponsePayload. inferenceId = %v. err = %v", inference.InferenceId, err)
		return err
	}

	//goland:noinspection GoDfaNilDereference
	requestMap["enforced_str"] = originalResponse.Choices[0].Message.Content

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

	originalLogits := extractLogits(originalResponse)
	validationLogits := extractLogits(responseValidation)

	compareLogits(originalLogits, validationLogits)

	return nil
}

func extractLogits(response broker.Response) []broker.Logprob {
	var logits []broker.Logprob
	// Concatenate all logrpobs
	for _, c := range response.Choices {
		logits = append(logits, c.Logprobs.Content...)
	}
	return logits
}

func compareLogits(originalLogits []broker.Logprob, validationLogits []broker.Logprob) bool {
	return true
}
