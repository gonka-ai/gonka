package main

import (
	"bufio"
	"bytes"
	"context"
	"decentralized-api/api"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/completionapi"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/merkleproof"
	"encoding/base64"
	"encoding/json"
	errors2 "errors"
	"fmt"
	"net/url"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"io"
	"log"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
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

func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("Received request", "method", r.Method, "path", r.URL.Path)
		slog.Debug("Request headers", "headers", r.Header)
		next.ServeHTTP(w, r)
	})
}

func StartInferenceServerWrapper(
	nodeBroker *broker.Broker,
	transactionRecorder cosmos_client.CosmosMessageClient,
	configManager *apiconfig.ConfigManager,
) {
	slog.SetLogLoggerLevel(slog.LevelDebug)
	slog.Debug("StartInferenceServerWrapper")

	mux := http.NewServeMux()

	// Create an HTTP server
	mux.HandleFunc("/v1/chat/completions/", wrapGetCompletion(transactionRecorder))
	mux.HandleFunc("/v1/chat/completions", wrapChat(nodeBroker, transactionRecorder, configManager))
	mux.HandleFunc("/v1/validation", wrapValidation(nodeBroker, transactionRecorder))
	mux.HandleFunc("/v1/participants", wrapSubmitNewParticipant(transactionRecorder))
	mux.HandleFunc("/v1/participants/", wrapGetInferenceParticipant(transactionRecorder))
	mux.HandleFunc("/v1/nodes", api.WrapNodes(nodeBroker, configManager))
	mux.HandleFunc("/v1/nodes/", api.WrapNodes(nodeBroker, configManager))
	mux.HandleFunc("/v1/epochs/", api.WrapGetParticipantsByEpoch(transactionRecorder, configManager))
	mux.HandleFunc("/v1/poc-batches/", api.WrapPoCBatches(transactionRecorder))
	mux.HandleFunc("/v1/verify-proof", api.WrapVerifyProof())
	mux.HandleFunc("/v1/verify-block", api.WrapVerifyBlock(configManager))
	mux.HandleFunc("/", logUnknownRequest())
	mux.HandleFunc("/v1/debug/pubkey-to-addr/", func(writer http.ResponseWriter, request *http.Request) {
		pubkey := strings.TrimPrefix(request.URL.Path, "/v1/debug/pubkey-to-addr/")
		addr, err := cosmos_client.PubKeyToAddress(pubkey)
		if err != nil {
			slog.Error("Failed to convert pubkey to address", "error", err)
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}

		writer.WriteHeader(http.StatusOK)
		writer.Write([]byte(addr))
	})
	mux.HandleFunc("/v1/debug/verify/", func(writer http.ResponseWriter, request *http.Request) {
		height, err := strconv.ParseInt(strings.TrimPrefix(request.URL.Path, "/v1/debug/verify/"), 10, 64)
		if err != nil {
			slog.Error("Failed to parse height", "error", err)
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}

		slog.Debug("Verifying block signatures", "height", height)
		if err := merkleproof.VerifyBlockSignatures(configManager.GetConfig().ChainNode.Url, height); err != nil {
			slog.Error("Failed to verify block signatures", "error", err)
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}

		writer.WriteHeader(http.StatusOK)
		writer.Write([]byte("Block signatures verified"))
	})
	mux.HandleFunc("/v1/status", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		writer.Write([]byte("{\"status\": \"ok\"}"))
	})

	addr := fmt.Sprintf(":%d", configManager.GetConfig().Api.Port)

	slog.Info("Starting the server", "address", addr)
	loggedMux := LoggingMiddleware(mux)
	// Start the server
	log.Fatal(http.ListenAndServe(addr, loggedMux))
}

func logUnknownRequest() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		slog.Warn("Unknown request", "path", request.URL.Path)
		http.Error(w, "Unknown request", http.StatusNotFound)
	}
}

func wrapGetInferenceParticipant(recorder cosmos_client.CosmosMessageClient) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			slog.Warn("Invalid method", "method", request.Method)
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
		slog.Error("Failed to load node to broker", "error", err)
		panic(err)
	}
}

type ResponseWithBody struct {
	Response  *http.Response
	BodyBytes []byte
}

func wrapGetCompletion(recorder cosmos_client.CosmosMessageClient) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		slog.Debug("GetCompletion received")

		if request.Method == http.MethodGet {
			processGetCompletionById(w, request, recorder)
			return
		}

		slog.Error("Unrecognixed GetCompletion request")
		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
	}

}

type ChatRequest struct {
	Body                 []byte
	Request              *http.Request
	OpenAiRequest        OpenAiRequest
	AuthKey              string
	PubKey               string
	Seed                 string
	InferenceId          string
	RequesterAddress     string
	FundedByTransferNode bool
}

func readRequest(request *http.Request) (*ChatRequest, error) {
	body, err := ReadRequestBody(request)
	if err != nil {
		slog.Error("Unable to read request body", "error", err)
		return nil, err
	}

	openAiRequest := OpenAiRequest{}
	err = json.Unmarshal(body, &openAiRequest)
	if err != nil {
		return nil, err
	}

	fundedByTransferNode, err := strconv.ParseBool(request.Header.Get("X-Funded-By-Transfer-Node"))
	if err != nil {
		fundedByTransferNode = false
	}

	slog.Debug("fundedByTransferNode", "node", fundedByTransferNode)
	return &ChatRequest{
		Body:                 body,
		Request:              request,
		OpenAiRequest:        openAiRequest,
		AuthKey:              request.Header.Get("Authorization"),
		PubKey:               request.Header.Get("X-Public-Key"),
		Seed:                 request.Header.Get("X-Seed"),
		InferenceId:          request.Header.Get("X-Inference-Id"),
		RequesterAddress:     request.Header.Get("X-Requester-Address"),
		FundedByTransferNode: fundedByTransferNode,
	}, nil
}

func wrapChat(nodeBroker *broker.Broker, recorder cosmos_client.CosmosMessageClient, configManager *apiconfig.ConfigManager) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		slog.Debug("wrapChat. Received request", "method", request.Method, "path", request.URL.Path)
		chatRequest, err := readRequest(request)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if request.Method != http.MethodPost {
			slog.Warn("Invalid method", "method", request.Method)
			http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
			return
		}
		if chatRequest.AuthKey == "" && !chatRequest.FundedByTransferNode {
			slog.Warn("Request without authorization", "path", request.URL.Path)
			http.Error(w, "Authorization is required", http.StatusUnauthorized)
			return
		}
		// Is this a Transfer request or an Executor call?
		if (chatRequest.PubKey != "" && chatRequest.InferenceId != "" && chatRequest.Seed != "") || (chatRequest.FundedByTransferNode && chatRequest.InferenceId != "" && chatRequest.Seed != "") {
			slog.Info("Executor request", "inferenceId", chatRequest.InferenceId, "seed", chatRequest.Seed, "pubKey", chatRequest.PubKey)
			handleExecutorRequest(w, chatRequest, nodeBroker, recorder, configManager.GetConfig())
			return
		} else if request.Header.Get("X-Requester-Address") != "" || chatRequest.FundedByTransferNode {
			slog.Info("Transfer request", "requesterAddress", chatRequest.RequesterAddress)
			handleTransferRequest(request.Context(), w, chatRequest, recorder)
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

func getExecutorForRequest(ctx context.Context, recorder cosmos_client.CosmosMessageClient) (*ExecutorDestination, error) {
	queryClient := recorder.NewInferenceQueryClient()
	response, err := queryClient.GetRandomExecutor(ctx, &types.QueryGetRandomExecutorRequest{})
	if err != nil {
		return nil, err
	}
	executor := response.Executor
	slog.Info("Executor selected", "address", executor.Address, "url", executor.InferenceUrl)
	return &ExecutorDestination{
		Url:     executor.InferenceUrl,
		Address: executor.Address,
	}, nil
}

func handleTransferRequest(ctx context.Context, w http.ResponseWriter, request *ChatRequest, recorder cosmos_client.CosmosMessageClient) bool {
	var pubkey = ""
	if !request.FundedByTransferNode {
		queryClient := recorder.NewInferenceQueryClient()
		slog.Debug("GET inference participant for transfer", "address", request.RequesterAddress)
		client, err := queryClient.InferenceParticipant(ctx, &types.QueryInferenceParticipantRequest{Address: request.RequesterAddress})
		if err != nil {
			slog.Error("Failed to get inference participant", "address", request.RequesterAddress, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		// Response is filled out with validate? Probably want to standardize
		hadError := validateClient(w, request, client)
		if hadError {
			return true
		}
		pubkey = client.Pubkey
	}

	executor, err := getExecutorForRequest(ctx, recorder)
	if err != nil {
		slog.Error("Failed to get executor", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}

	seed := rand.Int31()
	inferenceUUID := uuid.New().String()
	inferenceRequest, err := createInferenceStartRequest(request, seed, inferenceUUID, executor)
	if err != nil {
		slog.Error("Failed to create inference start request", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	go func() {
		slog.Debug("Starting inference", "id", inferenceRequest.InferenceId)
		err := recorder.StartInference(inferenceRequest)
		if err != nil {
			slog.Error("Failed to submit MsgStartInference", "id", inferenceRequest.InferenceId, "error", err)
		} else {
			slog.Debug("Submitted MsgStartInference", "id", inferenceRequest.InferenceId)
		}
	}()
	// It's important here to send the ORIGINAL body, not the finalRequest body. The executor will AGAIN go through
	// the same process to create the same final request body
	slog.Debug("Sending request to executor", "url", executor.Url, "seed", seed, "inferenceId", inferenceUUID)
	req, err := http.NewRequest("POST", executor.Url+"/v1/chat/completions", bytes.NewReader(request.Body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	req.Header.Set("X-Inference-Id", inferenceUUID)
	req.Header.Set("X-Seed", strconv.Itoa(int(seed)))
	req.Header.Set("X-Public-Key", pubkey)
	req.Header.Set("Authorization", request.AuthKey)
	req.Header.Set("Content-Type", request.Request.Header.Get("Content-Type"))
	req.Header.Set("X-Funded-By-Transfer-Node", strconv.FormatBool(request.FundedByTransferNode))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("Failed to make http request to executor", "error", err, "url", executor.Url)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}

	defer resp.Body.Close()

	proxyResponse(resp, w, false, nil)

	return true
}

func proxyResponse(
	resp *http.Response,
	w http.ResponseWriter,
	excludeContentLength bool,
	responseProcessor completionapi.ResponseProcessor,
) {
	// Make sure to copy response headers to the client
	for key, values := range resp.Header {
		// Skip Content-Length, because we're modifying body
		if excludeContentLength && key == "Content-Length" {
			continue
		}

		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	contentType := resp.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "text/event-stream") {
		proxyTextStreamResponse(resp, w, responseProcessor)
	} else {
		proxyJsonResponse(resp, w, responseProcessor)
	}
}

func proxyTextStreamResponse(resp *http.Response, w http.ResponseWriter, responseProcessor completionapi.ResponseProcessor) {
	w.WriteHeader(resp.StatusCode)

	// Stream the response from the completion server to the client
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// DEBUG LOG
		slog.Debug("Chunk", "line", line)

		var lineToProxy = line
		if responseProcessor != nil {
			var err error
			lineToProxy, err = responseProcessor.ProcessStreamedResponse(line)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		slog.Debug("Chunk to proxy", "line", lineToProxy)

		// Forward the line to the client
		_, err := fmt.Fprintln(w, lineToProxy)
		if err != nil {
			slog.Error("Error while streaming response", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Error("Error while streaming response", "error", err)
	}
}

func proxyJsonResponse(resp *http.Response, w http.ResponseWriter, responseProcessor completionapi.ResponseProcessor) {
	var bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "Failed to read inference node response body", http.StatusInternalServerError)
		return
	}

	if responseProcessor != nil {
		bodyBytes, err = responseProcessor.ProcessJsonResponse(bodyBytes)
		if err != nil {
			http.Error(w, "Failed to add ID to response", http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(resp.StatusCode)
	w.Write(bodyBytes)
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
		InferenceId:   inferenceId,
		PromptHash:    promptHash,
		PromptPayload: promptPayload,
		RequestedBy:   request.RequesterAddress,
		Model:         testModel,
	}
	return transaction, nil
}

func validateClient(w http.ResponseWriter, request *ChatRequest, client *types.QueryInferenceParticipantResponse) bool {
	if client == nil {
		slog.Error("Inference participant not found", "address", request.RequesterAddress)
		http.Error(w, "Inference participant not found", http.StatusNotFound)
		return true
	}

	err := validateRequestAgainstPubKey(request, client.Pubkey)
	if err != nil {
		slog.Error("Unable to validate request against PubKey", "error", err)
		http.Error(w, "Unable to validate request against PubKey:"+err.Error(), http.StatusUnauthorized)
		return true
	}
	if request.OpenAiRequest.MaxTokens == 0 {
		request.OpenAiRequest.MaxTokens = keeper.DefaultMaxTokens
	}
	escrowNeeded := request.OpenAiRequest.MaxTokens * keeper.PerTokenCost
	slog.Debug("Escrow needed", "escrowNeeded", escrowNeeded)
	slog.Debug("Client balance", "balance", client.Balance)
	if client.Balance < int64(escrowNeeded) {
		http.Error(w, "Insufficient balance", http.StatusPaymentRequired)
		return true
	}
	return false
}

func handleExecutorRequest(w http.ResponseWriter, request *ChatRequest, nodeBroker *broker.Broker, recorder cosmos_client.CosmosMessageClient, config *apiconfig.Config) bool {
	if !request.FundedByTransferNode {
		err := validateRequestAgainstPubKey(request, request.PubKey)
		if err != nil {
			http.Error(w, "Unable to validate request against PubKey:"+err.Error(), http.StatusUnauthorized)
			return true
		}
	}

	seed, err := strconv.Atoi(request.Seed)
	if err != nil {
		slog.Warn("Unable to parse seed", "seed", request.Seed)
		http.Error(w, "Unable to parse seed", http.StatusBadRequest)
		return true
	}

	modifiedRequestBody, err := completionapi.ModifyRequestBody(request.Body, int32(seed))
	if err != nil {
		slog.Warn("Unable to modify request body", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}

	resp, err := broker.LockNode(nodeBroker, testModel, func(node *broker.InferenceNode) (*http.Response, error) {
		completionsUrl, err := url.JoinPath(node.InferenceUrl(), "/v1/chat/completions")
		if err != nil {
			return nil, err
		}
		return http.Post(
			completionsUrl,
			request.Request.Header.Get("Content-Type"),
			bytes.NewReader(modifiedRequestBody.NewBody),
		)
	})
	if err != nil {
		slog.Error("Failed to get response from inference node", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := getInferenceErrorMessage(resp)
		slog.Warn("Inference node response with an error", "code", resp.StatusCode, "msg", msg)
		http.Error(w, msg, http.StatusInternalServerError)
		return true
	}

	responseProcessor := completionapi.NewExecutorResponseProcessor(request.InferenceId)
	proxyResponse(resp, w, true, responseProcessor)

	responseBodyBytes, err := responseProcessor.GetResponseBytes()
	if err != nil {
		// Not http.Error, because we assume we already returned everything to the client during proxyResponse execution
		return true
	}

	err = sendInferenceTransaction(request.InferenceId, responseBodyBytes, modifiedRequestBody.NewBody, &recorder, config.ChainNode.AccountName)
	if err != nil {
		// Not http.Error, because we assume we already returned everything to the client during proxyResponse execution
		slog.Error("Failed to send inference transaction", "error", err)
		return true
	}

	return false
}

func getInferenceErrorMessage(resp *http.Response) string {
	msg := fmt.Sprintf("Inference node response with an error. code = %d.", resp.StatusCode)
	bodyBytes, err := io.ReadAll(resp.Body)
	if err == nil {
		return msg + fmt.Sprintf(" error = %s.", string(bodyBytes))
	} else {
		return msg
	}
}

func validateRequestAgainstPubKey(request *ChatRequest, pubKey string) error {
	slog.Debug("Checking key for request", "pubkey", pubKey)

	pubKeyBytes, err := base64.StdEncoding.DecodeString(pubKey)
	if err != nil {
		return err
	}
	actualKey := secp256k1.PubKey{Key: pubKeyBytes}
	// Not sure about decoding/encoding the actual key bytes
	keyBytes, err := base64.StdEncoding.DecodeString(request.AuthKey)

	valid := actualKey.VerifySignature(request.Body, keyBytes)
	if !valid {
		slog.Warn("Signature did not match pubkey")
		return errors2.New("invalid signature")
	}
	return nil
}

func processGetInferenceParticipantByAddress(w http.ResponseWriter, request *http.Request, recorder cosmos_client.CosmosMessageClient) {
	// Manually extract the {id} from the URL path
	address := strings.TrimPrefix(request.URL.Path, "/v1/participants/")
	if address == "" {
		http.Error(w, "Address is required", http.StatusBadRequest)
		return
	}

	slog.Debug("GET inference participant", "address", address)
	queryClient := recorder.NewInferenceQueryClient()
	response, err := queryClient.InferenceParticipant(request.Context(), &types.QueryInferenceParticipantRequest{Address: address})
	if err != nil {
		slog.Error("Failed to get inference participant", "address", address, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if response == nil {
		slog.Error("Inference participant not found", "address", address)
		http.Error(w, "Inference participant not found", http.StatusNotFound)
		return
	}

	api.RespondWithJson(w, response)
}

func processGetCompletionById(w http.ResponseWriter, request *http.Request, recorder cosmos_client.CosmosMessageClient) {
	// Manually extract the {id} from the URL path
	id := strings.TrimPrefix(request.URL.Path, "/v1/chat/completions/")
	if id == "" {
		http.Error(w, "ID is required", http.StatusBadRequest)
		return
	}

	slog.Debug("GET inference", "id", id)
	queryClient := recorder.NewInferenceQueryClient()
	response, err := queryClient.Inference(request.Context(), &types.QueryGetInferenceRequest{Index: id})
	if err != nil {
		slog.Error("Failed to get inference", "id", id, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if response == nil {
		slog.Error("Inference not found", "id", id)
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

func getInference(request *ChatRequest, serverUrl string, recorder *cosmos_client.CosmosMessageClient, accountName string, seed int32) (*ResponseWithBody, error) {
	modifiedRequestBody, err := completionapi.ModifyRequestBody(request.Body, seed)
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

	err2 := sendInferenceTransaction(request.InferenceId, bodyBytes, modifiedRequestBody.NewBody, recorder, accountName)
	if err2 != nil {
		return nil, err2
	}

	result := &ResponseWithBody{
		Response:  resp,
		BodyBytes: bodyBytes,
	}
	return result, nil
}

func sendInferenceTransaction(inferenceId string, responseBodyBytes []byte, modifiedRequestBodyBytes []byte, recorder *cosmos_client.CosmosMessageClient, accountName string) error {
	hash, response, err := getResponseHash(responseBodyBytes)
	if err != nil {
		return err
	}

	promptHash, promptPayload, err := getPromptHash(modifiedRequestBodyBytes)
	if err != nil {
		return err
	}

	transaction := InferenceTransaction{
		PromptHash:           promptHash,
		PromptPayload:        promptPayload,
		ResponseHash:         hash,
		ResponsePayload:      string(responseBodyBytes),
		PromptTokenCount:     response.Usage.PromptTokens,
		CompletionTokenCount: response.Usage.CompletionTokens,
		Model:                response.Model,
		Id:                   response.ID,
	}

	if recorder != nil {
		createInferenceFinishedTransaction(inferenceId, *recorder, transaction, accountName)
	}
	return nil
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

func createInferenceFinishedTransaction(id string, recorder cosmos_client.CosmosMessageClient, transaction InferenceTransaction, accountName string) {
	message := &inference.MsgFinishInference{
		Creator:              accountName,
		InferenceId:          id,
		ResponseHash:         transaction.ResponseHash,
		ResponsePayload:      transaction.ResponsePayload,
		PromptTokenCount:     transaction.PromptTokenCount,
		CompletionTokenCount: transaction.CompletionTokenCount,
		ExecutedBy:           accountName,
	}

	// Submit to the block chain effectively AFTER we've served the request. Speed before certainty.
	go func() {
		// PRTODO: delete me and probably introduce retries if FinishInference returns not found
		time.Sleep(10 * time.Second)
		slog.Debug("Submitting MsgFinishInference", "inferenceId", id)
		err := recorder.FinishInference(message)
		if err != nil {
			slog.Error("Failed to submit MsgFinishInference", "inferenceId", id, "error", err)
		} else {
			slog.Debug("Submitted MsgFinishInference", "inferenceId", id)
		}
	}()
}

func getResponseHash(bodyBytes []byte) (string, *completionapi.Response, error) {
	// Unmarshal the JSON response
	var response completionapi.Response
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

func wrapValidation(nodeBroker *broker.Broker, recorder cosmos_client.CosmosMessageClient) func(w http.ResponseWriter, request *http.Request) {
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
			slog.Error("Failed to validate inference", "id", validationRequest.Id, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		msgVal, err := ToMsgValidation(result)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err = recorder.ReportValidation(msgVal); err != nil {
			slog.Error("Failed to submit MsgValidation", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(msgVal.String()))
	}
}

func wrapSubmitNewParticipant(recorder cosmos_client.CosmosMessageClient) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		slog.Debug("SubmitNewParticipant received", "method", request.Method)
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

type ParticipantsDto struct {
	Participants []ParticipantDto `json:"participants"`
	BlockHeight  int64            `json:"block_height"`
}

type ParticipantDto struct {
	Id          string   `json:"id"`
	Url         string   `json:"url"`
	Models      []string `json:"models"`
	CoinsOwed   int64    `json:"coins_owed"`
	RefundsOwed int64    `json:"refunds_owed"`
	Balance     int64    `json:"balance"`
	VotingPower int64    `json:"voting_power"`
	Reputation  float32  `json:"reputation"`
}

func submitNewUnfundedParticipant(recorder cosmos_client.CosmosMessageClient, w http.ResponseWriter, body api.SubmitUnfundedNewParticipantDto) {
	msg := &inference.MsgSubmitNewUnfundedParticipant{
		Address:      body.Address,
		Url:          body.Url,
		Models:       body.Models,
		ValidatorKey: body.ValidatorKey,
		PubKey:       body.PubKey,
	}

	slog.Debug("Submitting NewUnfundedParticipant", "message", msg)

	if err := recorder.SubmitNewUnfundedParticipant(msg); err != nil {
		slog.Error("Failed to submit MsgSubmitNewUnfundedParticipant", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func submitNewParticipant(recorder cosmos_client.CosmosMessageClient, w http.ResponseWriter, request *http.Request) {
	// Parse the request body into a SubmitNewParticipantDto
	var body api.SubmitUnfundedNewParticipantDto

	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		slog.Error("Failed to decode request body", "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	slog.Debug("SubmitNewParticipantDto", "body", body)
	if body.Address != "" && body.PubKey != "" {
		submitNewUnfundedParticipant(recorder, w, body)
		return
	}

	msg := &inference.MsgSubmitNewParticipant{
		Url:          body.Url,
		Models:       body.Models,
		ValidatorKey: body.ValidatorKey,
	}

	slog.Info("ValidatorKey in dapi", "key", body.ValidatorKey)
	if err := recorder.SubmitNewParticipant(msg); err != nil {
		slog.Error("Failed to submit MsgSubmitNewParticipant", "error", err)
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
		slog.Error("Failed to marshal response", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(responseJson)
}

func getParticipants(recorder cosmos_client.CosmosMessageClient, w http.ResponseWriter, request *http.Request) {
	queryClient := recorder.NewInferenceQueryClient()
	r, err := queryClient.ParticipantAll(request.Context(), &types.QueryAllParticipantRequest{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	participants := make([]ParticipantDto, len(r.Participant))
	for i, p := range r.Participant {
		balances, err := recorder.BankBalances(request.Context(), p.Address)
		pBalance := int64(0)
		if err == nil {
			for _, balance := range balances {
				// TODO: surely there is a place to get denom from
				if balance.Denom == "nicoin" {
					pBalance = balance.Amount.Int64()
				}
			}
			if pBalance == 0 {
				slog.Debug("Participant has no balance", "address", p.Address)
			}
		} else {
			slog.Warn("Failed to get balance for participant", "address", p.Address, "error", err)
		}
		participants[i] = ParticipantDto{
			Id:          p.Address,
			Url:         p.InferenceUrl,
			Models:      p.Models,
			CoinsOwed:   p.CoinBalance,
			RefundsOwed: p.RefundBalance,
			Balance:     pBalance,
			VotingPower: int64(p.Weight),
			Reputation:  p.Reputation,
		}
	}

	responseBody := ParticipantsDto{
		Participants: participants,
		BlockHeight:  r.BlockHeight,
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

// Why u no have this in std lib????
func getValueOrDefault[K comparable, V any](m map[K]V, key K, defaultValue V) V {
	if value, exists := m[key]; exists {
		return value
	}
	return defaultValue
}
