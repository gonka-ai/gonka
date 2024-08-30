package main

import (
	"bytes"
	"decentralized-api/broker"
	"decentralized-api/completionapi"
	"encoding/json"
	"fmt"
	types2 "github.com/cometbft/cometbft/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/google/uuid"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/v2"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"io"
	"log"
	"net/http"
	"strings"
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
	http.HandleFunc("/v1/chat/completions/", wrapGetCompletion(transactionRecorder))
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

type ResponseWithBody struct {
	Response  *http.Response
	BodyBytes []byte
}

func wrapGetCompletion(recorder InferenceCosmosClient) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		log.Printf("Received request. method = %s. path = %s", request.Method, request.URL.Path)

		if request.Method == http.MethodGet {
			processGetCompletionById(w, request, recorder)
			return
		}

		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
	}

}
func wrapChat(nodeBroker *broker.Broker, recorder InferenceCosmosClient, config Config) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		log.Printf("wrapChat. Received request. method = %s. path = %s", request.Method, request.URL.Path)

		if request.Method != http.MethodPost {
			http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
			return
		}

		respWithBody, err := broker.LockNode(nodeBroker, testModel, func(node *broker.InferenceNode) (*ResponseWithBody, error) {
			return getInference(request, node.Url, &recorder, config.ChainNode.AccountName)
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Copy the response back to the client
		for key, values := range respWithBody.Response.Header {
			// Skip Content-Length, because we're modifying body
			if key == "Content-Length" {
				continue
			}
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(respWithBody.Response.StatusCode)
		w.Write(respWithBody.BodyBytes)
	}
}

func processGetCompletionById(w http.ResponseWriter, request *http.Request, recorder InferenceCosmosClient) {
	// Manually extract the {id} from the URL path
	id := strings.TrimPrefix(request.URL.Path, "/v1/chat/completions/")
	if id == "" {
		http.Error(w, "ID is required", http.StatusBadRequest)
		return
	}

	log.Printf("GET inference. id = %s", id)
	queryClient := recorder.NewInferenceQueryClient()
	response, err := queryClient.Inference(recorder.context, &types.QueryGetInferenceRequest{Index: id})
	if err != nil {
		log.Printf("Failed to get inference. id = %s. err = %v", id, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if response == nil {
		log.Printf("Inference not found. id = %s", id)
		http.Error(w, "Inference not found", http.StatusNotFound)
		return
	}

	respBytes, err := json.Marshal(response.Inference)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(respBytes)

	return
}

func getInference(request *http.Request, serverUrl string, recorder *InferenceCosmosClient, accountName string) (*ResponseWithBody, error) {
	requestBytes, err := ReadRequestBody(request)
	if err != nil {
		return nil, err
	}

	modifiedRequestBody, err := completionapi.ModifyRequestBody(requestBytes)
	if err != nil {
		return nil, err
	}

	promptHash, promptPayload, err := getPromptHash(modifiedRequestBody.NewBody)
	transactionUUID := uuid.New().String()
	if err != nil {
		return nil, err
	}
	err = recorder.StartInference(&inference.MsgStartInference{
		Creator:       accountName,
		InferenceId:   transactionUUID,
		PromptHash:    promptHash,
		PromptPayload: promptPayload,
		ReceivedBy:    accountName,
		Model:         testModel,
	})
	if err != nil {
		log.Printf("Failed to submit MsgStartInference. %v", err)
		return nil, err
	}

	// Forward the request to the inference server
	resp, err := http.Post(
		serverUrl+"v1/chat/completions",
		request.Header.Get("Content-Type"),
		bytes.NewReader(modifiedRequestBody.NewBody),
	)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Read the response body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	bodyBytes, err = addIdToBodyBytes(bodyBytes, transactionUUID)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result := &ResponseWithBody{
			Response:  resp,
			BodyBytes: bodyBytes,
		}
		return result, nil
	}

	hash, response, err := getResponseHash(bodyBytes)
	if err != nil {
		return nil, err
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

	result := &ResponseWithBody{
		Response:  resp,
		BodyBytes: bodyBytes,
	}
	return result, nil
}

func addIdToBodyBytes(bodyBytes []byte, id string) ([]byte, error) {
	var bodyMap map[string]interface{}
	err := json.Unmarshal(bodyBytes, &bodyMap)
	if err != nil {
		return nil, err
	}

	bodyMap["id"] = id

	updatedBodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, err
	}

	return updatedBodyBytes, nil
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
		log.Printf("Failed to submit MsgFinishInference. %v", err)
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
			log.Printf("Failed to submit MsgValidation. %v", err)
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
	Url          string   `json:"url"`
	Models       []string `json:"models"`
	ValidatorKey string   `json:"validator_key"`
}

type ParticipantsDto struct {
	Participants []ParticipantDto `json:"participants"`
}

type ParticipantDto struct {
	Id          string   `json:"id"`
	Url         string   `json:"url"`
	Models      []string `json:"models"`
	CoinsOwed   uint64   `json:"coins_owed"`
	Balance     int64    `json:"balance"`
	VotingPower int64    `json:"voting_power"`
}

func submitNewParticipant(recorder InferenceCosmosClient, w http.ResponseWriter, request *http.Request) {
	// Parse the request body into a SubmitNewParticipantDto
	var body SubmitNewParticipantDto
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	msg := &inference.MsgSubmitNewParticipant{
		Url:          body.Url,
		Models:       body.Models,
		ValidatorKey: body.ValidatorKey,
	}

	log.Printf("ValidatorKey in dapi: %s", body.ValidatorKey)
	if err := recorder.SubmitNewParticipant(msg); err != nil {
		log.Printf("Failed to submit MsgSubmitNewParticipant. %v", err)
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
	validators, err := recorder.client.Context().Client.Validators(recorder.context, nil, nil, nil)

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	list, err := recorder.client.Context().Keyring.List()
	for _, key := range list {
		log.Printf("KeyRecord: %s", key.String())
		log.Printf("Name: %s", key.Name)
		pubKey, err := key.GetPubKey()
		if err != nil {
			log.Printf("Failed to get pubkey for key %s: %v", key)
		} else {
			log.Printf("PubKey: %s", pubKey.Address().String())
		}
		log.Printf("Key: %s", key.PubKey.String())
		log.Printf("Item: %s", key.Item)
	}

	// Index validators by address
	validatorMap := make(map[string]types2.Validator)
	for _, v := range validators.Validators {
		log.Printf("-Validator info:")
		log.Printf("Validator: %s", v.Address)
		// Use public key... account is based on this anyhow
		s := v.PubKey.Address().String()
		log.Printf("PubKey: %s", s)
		log.Printf("VotingPower: %d", v.VotingPower)
		accAddress := sdk.AccAddress(v.PubKey.Address()).String()
		valAddress := sdk.ValAddress(v.PubKey.Address()).String()
		consAdress := sdk.ConsAddress(v.PubKey.Address()).String()
		log.Printf("AccAddress: %s", accAddress)
		log.Printf("ValAddress: %s", valAddress)
		log.Printf("ConsAddress: %s", consAdress)
		log.Printf("-----")
		validatorMap[s] = *v
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
		power := getVotingPower(recorder, p.Address, validatorMap)
		participants[i] = ParticipantDto{
			Id:          p.Address,
			Url:         p.InferenceUrl,
			Models:      p.Models,
			CoinsOwed:   p.CoinBalance,
			Balance:     pBalance,
			VotingPower: power,
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

func getVotingPower(recorder InferenceCosmosClient, address string, validatorMap map[string]types2.Validator) int64 {
	addr, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		log.Printf("Failed to get account for participant %s: %v", address, err)
	}
	log.Printf("AccAddressFromBech32: %s", addr.String())
	account, err := recorder.client.Account(address)
	if err != nil {
		log.Printf("Failed to get account for participant %s: %v", address, err)
		return 0
	}
	s, err := account.Address("")
	log.Printf("Address: %s", s)
	pubKey, err := account.PubKey()
	log.Printf("PubKey: %s", pubKey)
	log.Printf("Name: %s", account.Name)
	log.Printf("Record: %s", account.Record.String())
	if err != nil {
		log.Printf("Failed to get pubkey for participant %s: %v", address, err)
		return 0
	}
	power := getValueOrDefault(validatorMap, pubKey, types2.Validator{}).VotingPower
	return power
}

// Why u no have this in std lib????
func getValueOrDefault[K comparable, V any](m map[K]V, key K, defaultValue V) V {
	if value, exists := m[key]; exists {
		return value
	}
	return defaultValue
}
