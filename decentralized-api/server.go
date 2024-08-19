package main

import (
	"bytes"
	"decentralized-api/broker"
	"decentralized-api/completionapi"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/v2"
	"inference/api/inference/inference"
	"inference/x/inference/types"
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
	Api       ApiConfig              `koanf:"api"`
	Nodes     []broker.InferenceNode `koanf:"nodes"`
	ChainNode ChainNodeConfig        `koanf:"chain_node"`
}

type ApiConfig struct {
	Port int `koanf:"port"`
}

type ChainNodeConfig struct {
	Url            string `koanf:"url"`
	AccountName    string `koanf:"account_name"`
	KeyringBackend string `koanf:"keyring_backend"`
	KeyringDir     string `koanf:"keyring_dir"`
}

func StartInferenceServerWrapper(nodeBroker *broker.Broker, transactionRecorder InferenceCosmosClient, config Config) {
	nodes := config.Nodes

	for _, node := range nodes {
		loadNodeToBroker(nodeBroker, &node)
	}

	// Create an HTTP server
	http.HandleFunc("/v1/chat/completions", wrapChat(nodeBroker, transactionRecorder, config))
	http.HandleFunc("/v1/validation", wrapValidation(nodeBroker, transactionRecorder))
	http.HandleFunc("/v1/participants", wrapSubmitNewParticipant(transactionRecorder))

	addr := fmt.Sprintf(":%d", config.Api.Port)
	log.Printf("Starting the server on %s", addr)

	// Start the server
	log.Fatal(http.ListenAndServe(addr, nil))
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

func wrapChat(nodeBroker *broker.Broker, recorder InferenceCosmosClient, config Config) func(w http.ResponseWriter, request *http.Request) {
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
		resp, bodyBytes, err := getInference(request, node.Url, &recorder, config.ChainNode.AccountName)
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

func getInference(request *http.Request, serverUrl string, recorder *InferenceCosmosClient, accountName string) (*http.Response, []byte, error) {
	requestBytes, err := ReadRequestBody(request)
	if err != nil {
		return nil, nil, err
	}

	modifiedRequestBody, err := completionapi.ModifyRequestBody(requestBytes)
	if err != nil {
		return nil, nil, err
	}

	promptHash, promptPayload, err := getPromptHash(modifiedRequestBody.NewBody)
	transactionUUID := uuid.New().String()
	if err != nil {
		return nil, nil, err
	}
	err = recorder.StartInference(&inference.MsgStartInference{
		Creator:       accountName,
		InferenceId:   transactionUUID,
		PromptHash:    promptHash,
		PromptPayload: promptPayload,
		ReceivedBy:    accountName,
	})
	if err != nil {
		return nil, nil, err
	}

	// Forward the request to the inference server
	resp, err := http.Post(
		serverUrl+"v1/chat/completions",
		request.Header.Get("Content-Type"),
		bytes.NewReader(modifiedRequestBody.NewBody),
	)
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
		createInferenceFinishedTransaction(transactionUUID, *recorder, transaction, accountName)
	}

	// print the json of the transaction:
	transactionJson, err := json.Marshal(transaction)
	if err == nil {
		log.Println(string(transactionJson))
	}
	return resp, bodyBytes, nil
}

func ReadRequestBody(r *http.Request) ([]byte, error) {
	// Read the request body into a buffer
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r.Body); err != nil {
		return nil, err
	}
	defer r.Body.Close()

	return buf.Bytes(), nil
}

func createInferenceFinishedTransaction(id string, recorder InferenceCosmosClient, transaction InferenceTransaction, accountName string) {
	message := &inference.MsgFinishInference{
		Creator:              accountName,
		InferenceId:          id,
		ResponseHash:         transaction.ResponseHash,
		ResponsePayload:      transaction.ResponsePayload,
		PromptTokenCount:     transaction.PromptTokenCount,
		CompletionTokenCount: transaction.CompletionTokenCount,
		ExecutedBy:           accountName,
	}

	println("--TRANSACTIONID--" + transaction.Id)
	err := recorder.FinishInference(message)
	if err != nil {
		log.Printf("Failed to finish inference. id = %s. err = %v", id, err)
	}
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

func getPromptHash(requestBytes []byte) (string, string, error) {
	// Canonicalize the request body
	canonicalJSON, err := CanonicalizeJSON(requestBytes)
	if err != nil {
		return "", "", err
	}

	// Generate the hash of the canonical JSON
	promptHash := generateSHA256Hash(canonicalJSON)

	return promptHash, canonicalJSON, nil
}

// Debug-only request
type ValidationRequest struct {
	Id string `json:"id"`
}

func wrapValidation(nodeBroker *broker.Broker, recorder InferenceCosmosClient) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		var validationRequest ValidationRequest
		if err := json.NewDecoder(request.Body).Decode(&validationRequest); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		result, err := broker.LockNode(nodeBroker, testModel, func(node *broker.InferenceNode) (ValidationResult, error) {
			return ValidateByInferenceId(validationRequest.Id, node, recorder)
		})

		if err != nil {
			log.Printf("Failed to validate inference. id = %s. err = %v", validationRequest.Id, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		msgVal, err := ToMsgValidation(result)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err = recorder.ReportValidation(msgVal); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(msgVal.String()))
	}
}

func wrapSubmitNewParticipant(recorder InferenceCosmosClient) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		if request.Method == "POST" {
			submitNewParticipant(recorder, w, request)
		} else if request.Method == "GET" {
			getParticipants(recorder, w, request)
		} else {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}
	}
}

type SubmitNewParticipantDto struct {
	Url    string   `json:"url"`
	Models []string `json:"models"`
}

type ParticipantsDto struct {
	Participants []ParticipantDto `json:"participants"`
}

type ParticipantDto struct {
	Id        string   `json:"id"`
	Url       string   `json:"url"`
	Models    []string `json:"models"`
	CoinsOwed uint64   `json:"coins_owed"`
	Balance   int64    `json:"balance"`
}

func submitNewParticipant(recorder InferenceCosmosClient, w http.ResponseWriter, request *http.Request) {
	// Parse the request body into a SubmitNewParticipantDto
	var body SubmitNewParticipantDto
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	msg := &inference.MsgSubmitNewParticipant{
		Url:    body.Url,
		Models: body.Models,
	}

	if err := recorder.SubmitNewParticipant(msg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	responseBody := ParticipantDto{
		Id:     msg.Creator,
		Url:    msg.Url,
		Models: msg.Models,
	}

	responseJson, err := json.Marshal(responseBody)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(responseJson)
}

func getParticipants(recorder InferenceCosmosClient, w http.ResponseWriter, request *http.Request) {
	queryClient := recorder.NewInferenceQueryClient()
	r, err := queryClient.ParticipantAll(recorder.context, &types.QueryAllParticipantRequest{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	participants := make([]ParticipantDto, len(r.Participant))
	for i, p := range r.Participant {
		balances, err := recorder.client.BankBalances(recorder.context, p.Address, nil)
		pBalance := int64(0)
		if err == nil {
			for _, balance := range balances {
				// TODO: surely there is a place to get denom from
				if balance.Denom == "icoin" {
					pBalance = balance.Amount.Int64()
				}
			}
			if pBalance == 0 {
				log.Printf("Participant %s has no balance", p.Address)
			}
		} else {
			log.Printf("Failed to get balance for participant %s: %v", p.Address, err)
		}
		participants[i] = ParticipantDto{
			Id:        p.Address,
			Url:       p.InferenceUrl,
			Models:    p.Models,
			CoinsOwed: p.CoinBalance,
			Balance:   pBalance,
		}
	}

	responseBody := ParticipantsDto{
		Participants: participants,
	}

	responseJson, err := json.Marshal(responseBody)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(responseJson)
}
