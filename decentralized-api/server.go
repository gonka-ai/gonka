package main

import (
	"bytes"
	"crypto/sha256"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/completionapi"
	cosmos_client "decentralized-api/cosmosclient"
	"encoding/base64"
	"encoding/json"
	errors2 "errors"
	"fmt"
	types2 "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/google/uuid"
	"github.com/productscience/inference/x/inference/keeper"
	"math/rand"
	"strconv"

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

func StartInferenceServerWrapper(nodeBroker *broker.Broker, transactionRecorder cosmos_client.InferenceCosmosClient, config apiconfig.Config) {
	nodes := config.Nodes
	for _, node := range nodes {
		loadNodeToBroker(nodeBroker, &node)
	}

	// Create an HTTP server
	http.HandleFunc("/v1/chat/completions/", wrapGetCompletion(transactionRecorder))
	http.HandleFunc("/v1/chat/completions", wrapChat(nodeBroker, transactionRecorder, config))
	http.HandleFunc("/v1/validation", wrapValidation(nodeBroker, transactionRecorder))
	http.HandleFunc("/v1/participants", wrapSubmitNewParticipant(transactionRecorder))
	http.HandleFunc("/v1/participant/", wrapGetInferenceParticipant(transactionRecorder))

	addr := fmt.Sprintf(":%d", config.Api.Port)
	log.Printf("Starting the server on %s", addr)
	// Start the server
	log.Fatal(http.ListenAndServe(addr, nil))
}

func wrapGetInferenceParticipant(recorder cosmos_client.InferenceCosmosClient) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
			return
		}
		processGetInferenceParticipantByAddress(w, request, recorder)
	}
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

func wrapGetCompletion(recorder cosmos_client.InferenceCosmosClient) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		log.Printf("Received request. method = %s. path = %s", request.Method, request.URL.Path)

		if request.Method == http.MethodGet {
			processGetCompletionById(w, request, recorder)
			return
		}

		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
	}

}

type ChatRequest struct {
	Body             []byte
	Request          *http.Request
	OpenAiRequest    OpenAiRequest
	AuthKey          string
	PubKey           string
	Seed             string
	InferenceId      string
	RequesterAddress string
}

func readRequest(request *http.Request) (*ChatRequest, error) {
	body, err := ReadRequestBody(request)
	if err != nil {
		return nil, err
	}

	openAiRequest := OpenAiRequest{}
	err = json.Unmarshal(body, &openAiRequest)
	if err != nil {
		return nil, err
	}

	return &ChatRequest{
		Body:             body,
		Request:          request,
		OpenAiRequest:    openAiRequest,
		AuthKey:          request.Header.Get("Authorization"),
		PubKey:           request.Header.Get("X-Public-Key"),
		Seed:             request.Header.Get("X-Seed"),
		InferenceId:      request.Header.Get("X-Inference-Id"),
		RequesterAddress: request.Header.Get("X-Requester-Address"),
	}, nil
}

func wrapChat(nodeBroker *broker.Broker, recorder cosmos_client.InferenceCosmosClient, config apiconfig.Config) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		log.Printf("wrapChat. Received request. method = %s. path = %s", request.Method, request.URL.Path)
		chatRequest, err := readRequest(request)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if request.Method != http.MethodPost {
			http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
			return
		}
		if chatRequest.AuthKey == "" {
			http.Error(w, "Authorization is required", http.StatusUnauthorized)
			return
		}
		// Is this a Transfer request or an Executor call?
		if chatRequest.PubKey != "" && chatRequest.InferenceId != "" && chatRequest.Seed != "" {
			handleExecutorRequest(w, chatRequest, nodeBroker, recorder, config)
			return
		} else if request.Header.Get("X-Requester-Address") != "" {
			handleTransferRequest(w, chatRequest, recorder)
			return
		} else {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

	}
}

// Only extract info we need
type OpenAiRequest struct {
	Model     string `json:"model"`
	Seed      int32  `json:"seed"`
	MaxTokens int32  `json:"max_tokens"`
}

type ExecutorDestination struct {
	Url     string `json:"url"`
	Address string `json:"address"`
}

func getExecutorForRequest(recorder cosmos_client.InferenceCosmosClient) (*ExecutorDestination, error) {
	executor, err := recorder.QueryRandomExecutor()
	if err != nil {
		return nil, err
	}
	log.Printf("LB Executor:" + executor.InferenceUrl)
	return &ExecutorDestination{
		Url:     executor.InferenceUrl,
		Address: executor.Address,
	}, nil
}

func handleTransferRequest(w http.ResponseWriter, request *ChatRequest, recorder cosmos_client.InferenceCosmosClient) bool {
	queryClient := recorder.NewInferenceQueryClient()
	log.Printf("GET inference participant for transfer. address = %s", request.RequesterAddress)
	client, err := queryClient.InferenceParticipant(recorder.Context, &types.QueryInferenceParticipantRequest{Address: request.RequesterAddress})
	if err != nil {
		log.Printf("Failed to get inference participant. address = %s. err = %v", request.RequesterAddress, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	executor, err := getExecutorForRequest(recorder)
	if err != nil {
		log.Printf("Failed to get executor. %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	// Response is filled out with validate? Probably want to standardize
	hadError := validateClient(w, request, client)
	if hadError {
		return true
	}

	seed := rand.Int31()
	inferenceUUID := uuid.New().String()
	inferenceRequest, err := createInferenceStartRequest(request, seed, inferenceUUID, executor)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	go func() {
		err := recorder.StartInference(inferenceRequest)
		if err != nil {
			log.Printf("Failed to submit MsgStartInference. %v", err)
		}
	}()
	// It's important here to send the ORIGINAL body, not the finalRequest body. The executor will AGAIN go through
	// the same process to create the same final request body
	req, err := http.NewRequest("POST", executor.Url+"/v1/chat/completions", bytes.NewReader(request.Body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	req.Header.Set("X-Inference-Id", inferenceUUID)
	req.Header.Set("X-Seed", strconv.Itoa(int(seed)))
	req.Header.Set("X-Public-Key", client.Pubkey)
	req.Header.Set("Authorization", request.AuthKey)
	req.Header.Set("Content-Type", request.Request.Header.Get("Content-Type"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}

	defer resp.Body.Close()
	// Copy the headers from the final server response
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Write the status code from the final server response
	w.WriteHeader(resp.StatusCode)

	// Copy the body from the final server response
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("Error copying response body: %v", err)
	}
	return true
}

func createInferenceStartRequest(request *ChatRequest, seed int32, inferenceId string, executor *ExecutorDestination) (*inference.MsgStartInference, error) {
	finalRequest, err := completionapi.ModifyRequestBody(request.Body, seed)
	if err != nil {
		return nil, err
	}
	promptHash, promptPayload, err := getPromptHash(finalRequest.NewBody)
	if err != nil {
		return nil, err
	}
	transaction := &inference.MsgStartInference{
		Creator:       request.RequesterAddress,
		InferenceId:   inferenceId,
		PromptHash:    promptHash,
		PromptPayload: promptPayload,
		// TODO: This should actually be the Executor selected by the address
		ReceivedBy: executor.Address,
		Model:      testModel,
	}
	return transaction, nil
}

func validateClient(w http.ResponseWriter, request *ChatRequest, client *types.QueryInferenceParticipantResponse) bool {
	if client == nil {
		log.Printf("Inference participant not found. address = %s", request.RequesterAddress)
		http.Error(w, "Inference participant not found", http.StatusNotFound)
		return true
	}

	err := validateRequestAgainstPubKey(request, client.Pubkey)
	if err != nil {
		http.Error(w, "Unable to validate request against PubKey:"+err.Error(), http.StatusUnauthorized)
		return true
	}
	if request.OpenAiRequest.MaxTokens == 0 {
		request.OpenAiRequest.MaxTokens = keeper.DefaultMaxTokens
	}
	escrowNeeded := request.OpenAiRequest.MaxTokens * keeper.PerTokenCost
	log.Printf("Escrow needed: %d", escrowNeeded)
	log.Printf("Client balance: %d", client.Balance)
	if client.Balance < int64(escrowNeeded) {
		http.Error(w, "Insufficient balance", http.StatusPaymentRequired)
		return true
	}
	return false
}

func handleExecutorRequest(w http.ResponseWriter, request *ChatRequest, nodeBroker *broker.Broker, recorder cosmos_client.InferenceCosmosClient, config apiconfig.Config) bool {
	err := validateRequestAgainstPubKey(request, request.PubKey)
	if err != nil {
		http.Error(w, "Unable to validate request against PubKey:"+err.Error(), http.StatusUnauthorized)
		return true
	}
	seed, err := strconv.Atoi(request.Seed)
	if err != nil {
		http.Error(w, "Unable to parse seed", http.StatusBadRequest)
		return true
	}
	respWithBody, err := broker.LockNode(nodeBroker, testModel, func(node *broker.InferenceNode) (*ResponseWithBody, error) {
		return getInference(request, node.Url, &recorder, config.ChainNode.AccountName, int32(seed))
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
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
	return false
}

func validateRequestAgainstPubKey(request *ChatRequest, pubKey string) error {
	log.Printf("PubKey: %s", pubKey)

	pubKeyBytes, err := base64.StdEncoding.DecodeString(pubKey)
	if err != nil {
		return err
	}
	actualKey := secp256k1.PubKey{Key: pubKeyBytes}
	// Not sure about decoding/encoding the actual key bytes
	keyBytes, err := base64.StdEncoding.DecodeString(request.AuthKey)
	shaOfBytes := sha256.Sum256(request.Body)
	encoding := base64.StdEncoding.EncodeToString(shaOfBytes[:])
	log.Print("SHA of bytes (verification):" + encoding)

	valid := actualKey.VerifySignature(request.Body, keyBytes)
	if !valid {
		return errors2.New("invalid signature")
	}
	return nil
}

func processGetInferenceParticipantByAddress(w http.ResponseWriter, request *http.Request, recorder cosmos_client.InferenceCosmosClient) {
	// Manually extract the {id} from the URL path
	address := strings.TrimPrefix(request.URL.Path, "/v1/participant/")
	if address == "" {
		http.Error(w, "Address is required", http.StatusBadRequest)
		return
	}

	log.Printf("GET inference participant. address = %s", address)
	queryClient := recorder.NewInferenceQueryClient()
	response, err := queryClient.InferenceParticipant(recorder.Context, &types.QueryInferenceParticipantRequest{Address: address})
	if err != nil {
		log.Printf("Failed to get inference participant. address = %s. err = %v", address, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if response == nil {
		log.Printf("Inference participant not found. address = %s", address)
		http.Error(w, "Inference participant not found", http.StatusNotFound)
		return
	}

	respBytes, err := json.Marshal(response)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(respBytes)

	return
}

func processGetCompletionById(w http.ResponseWriter, request *http.Request, recorder cosmos_client.InferenceCosmosClient) {
	// Manually extract the {id} from the URL path
	id := strings.TrimPrefix(request.URL.Path, "/v1/chat/completions/")
	if id == "" {
		http.Error(w, "ID is required", http.StatusBadRequest)
		return
	}

	log.Printf("GET inference. id = %s", id)
	queryClient := recorder.NewInferenceQueryClient()
	response, err := queryClient.Inference(recorder.Context, &types.QueryGetInferenceRequest{Index: id})
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(respBytes)

	return
}

func getInference(request *ChatRequest, serverUrl string, recorder *cosmos_client.InferenceCosmosClient, accountName string, seed int32) (*ResponseWithBody, error) {
	modifiedRequestBody, err := completionapi.ModifyRequestBody(request.Body, seed)
	if err != nil {
		return nil, err
	}

	promptHash, promptPayload, err := getPromptHash(modifiedRequestBody.NewBody)
	if err != nil {
		return nil, err
	}

	// Forward the request to the inference server
	resp, err := http.Post(
		serverUrl+"v1/chat/completions",
		request.Request.Header.Get("Content-Type"),
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

	bodyBytes, err = addIdToBodyBytes(bodyBytes, request.InferenceId)
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
		createInferenceFinishedTransaction(request.InferenceId, *recorder, transaction, accountName)
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

func createInferenceFinishedTransaction(id string, recorder cosmos_client.InferenceCosmosClient, transaction InferenceTransaction, accountName string) {
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
	// Submit to the block chain effectively AFTER we've served the request. Speed before certainty.
	go func() {
		err := recorder.FinishInference(message)
		if err != nil {
			log.Printf("Failed to submit MsgFinishInference. %v", err)
		}
	}()
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

func wrapValidation(nodeBroker *broker.Broker, recorder cosmos_client.InferenceCosmosClient) func(w http.ResponseWriter, request *http.Request) {
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

func wrapSubmitNewParticipant(recorder cosmos_client.InferenceCosmosClient) func(w http.ResponseWriter, request *http.Request) {
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
	RefundsOwed uint64   `json:"refunds_owed"`
	Balance     int64    `json:"balance"`
	VotingPower int64    `json:"voting_power"`
}

func submitNewParticipant(recorder cosmos_client.InferenceCosmosClient, w http.ResponseWriter, request *http.Request) {
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

func getParticipants(recorder cosmos_client.InferenceCosmosClient, w http.ResponseWriter, request *http.Request) {
	queryClient := recorder.NewInferenceQueryClient()
	r, err := queryClient.ParticipantAll(recorder.Context, &types.QueryAllParticipantRequest{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	validators, err := recorder.Client.Context().Client.Validators(recorder.Context, nil, nil, nil)

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	list, err := recorder.Client.Context().Keyring.List()
	for _, key := range list {
		log.Printf("KeyRecord: %s", key.String())
		log.Printf("Name: %s", key.Name)
		pubKey, err := key.GetPubKey()
		if err != nil {
			log.Printf("Failed to get pubkey for key %s: %v", key, err)
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
		balances, err := recorder.Client.BankBalances(recorder.Context, p.Address, nil)
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
			RefundsOwed: p.RefundBalance,
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

func getVotingPower(recorder cosmos_client.InferenceCosmosClient, address string, validatorMap map[string]types2.Validator) int64 {
	addr, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		log.Printf("Failed to get account for participant %s: %v", address, err)
	}
	log.Printf("AccAddressFromBech32: %s", addr.String())
	account, err := recorder.Client.Account(address)
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
