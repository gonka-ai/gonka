package main

import (
	"bytes"
	"decentralized-api/broker"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"inference/api/inference/inference"
	"io"
	"log"
	"net/http"
)

type InferenceTransaction struct {
	PromptHash           string `json:"promptHash"`
	PromptPayload        string `json:"promptPayload"`
	ResponseHash         string `json:"responseHash"`
	ResponsePayload      string `json:"responsePayload"`
	PromptTokenCount     uint64 `json:"promptTokenCount"`
	CompletionTokenCount uint64 `json:"completionTokenCount"`
	Model                string `json:"model"`
	Id                   string `json:"id"`
}

const testModel = "unsloth/llama-3-8b-Instruct"

var (
	k      = koanf.New(".")
	parser = yaml.Parser()
)

type Config struct {
	Nodes []broker.InferenceNode `koanf:"nodes"`
}

func StartInferenceServerWrapper(transactionRecorder InferenceCosmosClient) {
	// Load the configuration
	if err := k.Load(file.Provider("config.yaml"), parser); err != nil {
		log.Fatalf("error loading config: %v", err)
	}
	var config Config
	err := k.Unmarshal("", &config)
	if err != nil {
		log.Fatalf("error unmarshalling config: %v", err)
	}
	// Initialize logger
	nodeBroker := broker.NewBroker()

	nodes := config.Nodes

	for _, node := range nodes {
		loadNodeToBroker(nodeBroker, &node)
	}

	// Create an HTTP server
	http.HandleFunc("/v1/chat/completions", wrapChat(nodeBroker, transactionRecorder))

	// Start the server
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func loadNodeToBroker(nodeBroker *broker.Broker, node *broker.InferenceNode) {
	err := nodeBroker.QueueMessage(broker.RegisterNode{
		Node:     *node,
		Response: make(chan broker.InferenceNode, 2),
	})
	if err != nil {
		panic(err)
	}
}

func wrapChat(nodeBroker *broker.Broker, recorder InferenceCosmosClient) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		// Get the inference server URL
		nodeChan := make(chan *broker.InferenceNode, 2)
		err := nodeBroker.QueueMessage(broker.LockAvailableNode{
			Model:    testModel,
			Response: nodeChan,
		})
		if err != nil {
			http.Error(w, "Error getting node", http.StatusInternalServerError)
			return
		}
		node := <-nodeChan
		if node == nil {
			http.Error(w, "No nodes available", http.StatusServiceUnavailable)
			return
		}
		resp, bodyBytes, err := getInference(request, node.Url, &recorder)
		queueError := nodeBroker.QueueMessage(broker.ReleaseNode{
			NodeId: node.Id,
			Outcome: broker.InferenceSuccess{
				Response: nil,
			},
			Response: make(chan bool, 2),
		})
		if queueError != nil {
			http.Error(w, queueError.Error(), http.StatusInternalServerError)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Copy the response back to the client
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(bodyBytes)
	}
}

func getInference(request *http.Request, serverUrl string, recorder *InferenceCosmosClient) (*http.Response, []byte, error) {
	promptHash, promptPayload, err := getPromptHash(request)
	transactionUUID := uuid.New().String()
	if err != nil {
		return nil, nil, err
	}
	err = recorder.StartInference(&inference.MsgStartInference{
		Creator:       "alice",
		InferenceId:   transactionUUID,
		PromptHash:    promptHash,
		PromptPayload: promptPayload,
		ReceivedBy:    "alice",
	})
	if err != nil {
		return nil, nil, err
	}

	// Forward the request to the inference server
	resp, err := http.Post(serverUrl+"v1/chat/completions", request.Header.Get("Content-Type"), request.Body)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	// Read the response body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, bodyBytes, nil
	}

	hash, response, err := getResponseHash(bodyBytes)
	if err != nil {
		return nil, nil, err
	}

	transaction := InferenceTransaction{
		PromptHash:           promptHash,
		PromptPayload:        promptPayload,
		ResponseHash:         hash,
		ResponsePayload:      string(bodyBytes),
		PromptTokenCount:     response.Usage.PromptTokens,
		CompletionTokenCount: response.Usage.CompletionTokens,
		Model:                response.Model,
		Id:                   response.ID,
	}

	if recorder != nil {
		createInferenceFinishedTransaction(transactionUUID, *recorder, transaction)
	}

	// print the json of the transaction:
	transactionJson, err := json.Marshal(transaction)
	if err == nil {
		log.Println(string(transactionJson))
	}
	return resp, bodyBytes, nil
}

func createInferenceFinishedTransaction(id string, recorder InferenceCosmosClient, transaction InferenceTransaction) {
	message := &inference.MsgFinishInference{
		Creator:              "????",
		InferenceId:          id,
		ResponseHash:         transaction.ResponseHash,
		ResponsePayload:      transaction.ResponsePayload,
		PromptTokenCount:     transaction.PromptTokenCount,
		CompletionTokenCount: transaction.CompletionTokenCount,
		ExecutedBy:           "???",
	}

	println("--TRANSACTIONID--" + transaction.Id)
	recorder.FinishInference(message)
}

func getResponseHash(bodyBytes []byte) (string, *broker.Response, error) {
	// Unmarshal the JSON response
	var response broker.Response
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return "", nil, err
	}

	// Generate a SHA-256 hash of the content of the aggregated choices
	var content string
	for _, choice := range response.Choices {
		content += choice.Message.Content
	}
	hash := generateSHA256Hash(content)
	return hash, &response, nil
}

func getPromptHash(request *http.Request) (string, string, error) {
	// Read the request body into a buffer
	var buf bytes.Buffer
	tee := io.TeeReader(request.Body, &buf)
	requestBytes, err := io.ReadAll(tee)
	if err != nil {
		return "", "", err
	}

	// Canonicalize the request body
	canonicalJSON, err := CanonicalizeJSON(requestBytes)
	if err != nil {
		return "", "", err
	}

	// Generate the hash of the canonical JSON
	promptHash := generateSHA256Hash(canonicalJSON)
	// Create a new reader from the buffer for forwarding the request
	request.Body = io.NopCloser(&buf)
	return promptHash, canonicalJSON, nil
}
