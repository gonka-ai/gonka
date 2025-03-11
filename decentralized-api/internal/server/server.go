package server

import (
	"bufio"
	"bytes"
	"context"
	"decentralized-api/api"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/completionapi"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/merkleproof"
	"decentralized-api/utils"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"io"
	"log"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const testModel = "unsloth/llama-3-8b-Instruct"

func StartInferenceServerWrapper(
	nodeBroker *broker.Broker,
	transactionRecorder cosmos_client.CosmosMessageClient,
	configManager *apiconfig.ConfigManager,
) {
	slog.SetLogLoggerLevel(slog.LevelDebug)
	logging.Debug("StartInferenceServerWrapper", types.Server)

	mux := http.NewServeMux()

	// Create an HTTP server
	// TODO: some of handlers defined here and some in api package. Suggest to put it in 1 place
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
	mux.HandleFunc("/v1/pricing", api.WrapPricing(transactionRecorder))
	mux.HandleFunc("/v1/admin/unit-of-compute-price-proposal", api.WrapUnitOfComputePriceProposal(transactionRecorder, configManager))
	mux.HandleFunc("/v1/admin/models", api.WrapRegisterModel(transactionRecorder))
	mux.HandleFunc("/v1/models", api.WrapModels(transactionRecorder))
	mux.HandleFunc("/v1/training-jobs", api.WrapTraining(transactionRecorder))
	mux.HandleFunc("/v1/training-jobs/", api.WrapTraining(transactionRecorder))
	mux.HandleFunc("/", logUnknownRequest())
	mux.HandleFunc("/v1/debug/pubkey-to-addr/", func(writer http.ResponseWriter, request *http.Request) {
		pubkey := strings.TrimPrefix(request.URL.Path, "/v1/debug/pubkey-to-addr/")
		addr, err := cosmos_client.PubKeyToAddress(pubkey)
		if err != nil {
			logging.Error("Failed to convert pubkey to address", types.Participants, "error", err)
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}

		writer.WriteHeader(http.StatusOK)
		writer.Write([]byte(addr)) // TODO handle error??
	})
	mux.HandleFunc("/v1/debug/verify/", func(writer http.ResponseWriter, request *http.Request) {
		height, err := strconv.ParseInt(strings.TrimPrefix(request.URL.Path, "/v1/debug/verify/"), 10, 64)
		if err != nil {
			logging.Error("Failed to parse height", types.System, "error", err)
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}

		logging.Debug("Verifying block signatures", types.System, "height", height)
		if err := merkleproof.VerifyBlockSignatures(configManager.GetConfig().ChainNode.Url, height); err != nil {
			logging.Error("Failed to verify block signatures", types.Participants, "error", err)
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}

		writer.WriteHeader(http.StatusOK)
		writer.Write([]byte("Block signatures verified")) // TODO handle error??
	})
	mux.HandleFunc("/v1/status", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		writer.Write([]byte("{\"status\": \"ok\"}")) // TODO handle error??
	})

	addr := fmt.Sprintf(":%d", configManager.GetConfig().Api.Port)

	logging.Info("Starting the server", types.Server, "address", addr)
	loggedMux := loggingMiddleware(mux)
	// Start the server
	log.Fatal(http.ListenAndServe(addr, loggedMux))
}

func logUnknownRequest() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		logging.Warn("Unknown request", types.Server, "path", request.URL.Path)
		http.Error(w, "Unknown request", http.StatusNotFound)
	}
}

func wrapGetInferenceParticipant(recorder cosmos_client.CosmosMessageClient) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			logging.Warn("Invalid method", types.Server, "method", request.Method)
			http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
			return
		}
		processGetInferenceParticipantByAddress(w, request, recorder)
	}
}

func LoadNodeToBroker(nodeBroker *broker.Broker, node *apiconfig.InferenceNodeConfig) {
	err := nodeBroker.QueueMessage(broker.RegisterNode{
		Node:     *node,
		Response: make(chan apiconfig.InferenceNodeConfig, 2),
	})
	if err != nil {
		logging.Error("Failed to load node to broker", types.Nodes, "error", err)
		panic(err)
	}
}

func wrapGetCompletion(recorder cosmos_client.CosmosMessageClient) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		logging.Debug("GetCompletion received", types.Inferences)

		if request.Method == http.MethodGet {
			processGetCompletionById(w, request, recorder)
			return
		}

		logging.Error("Unrecognized GetCompletion request", types.Inferences, "method", request.Method)
		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
	}

}

func readRequest(request *http.Request) (*ChatRequest, error) {
	body, err := readRequestBody(request)
	if err != nil {
		logging.Error("Unable to read request body", types.Server, "error", err)
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

	logging.Debug("fundedByTransferNode", types.Inferences, "node", fundedByTransferNode)
	return &ChatRequest{
		Body:             body,
		Request:          request,
		OpenAiRequest:    openAiRequest,
		AuthKey:          request.Header.Get(utils.AuthorizationHeader),
		PubKey:           request.Header.Get(utils.XPublicKeyHeader),
		Seed:             request.Header.Get(utils.XSeedHeader),
		InferenceId:      request.Header.Get(utils.XInferenceIdHeader),
		RequesterAddress: request.Header.Get(utils.XRequesterAddressHeader),
	}, nil
}

func wrapChat(nodeBroker *broker.Broker, recorder cosmos_client.CosmosMessageClient, configManager *apiconfig.ConfigManager) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		logging.Debug("wrapChat. Received request", types.Inferences, "method", request.Method, "path", request.URL.Path)
		if request.Method != http.MethodPost {
			logging.Warn("Invalid method", types.Server, "method", request.Method)
			http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
			return
		}

		chatRequest, err := readRequest(request)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if chatRequest.AuthKey == "" {
			logging.Warn("Request without authorization", types.Server, "path", request.URL.Path)
			http.Error(w, "Authorization is required", http.StatusUnauthorized)
			return
		}
		// Is this a Transfer request or an Executor call?
		if (chatRequest.PubKey != "" && chatRequest.InferenceId != "" && chatRequest.Seed != "") || chatRequest.InferenceId != "" && chatRequest.Seed != "" {
			logging.Info("Executor request", types.Inferences, "inferenceId", chatRequest.InferenceId, "seed", chatRequest.Seed, "pubKey", chatRequest.PubKey)
			handleExecutorRequest(w, chatRequest, nodeBroker, recorder, configManager.GetConfig())
			return
		} else if chatRequest.RequesterAddress != "" {
			logging.Info("Transfer request", types.Inferences, "requesterAddress", chatRequest.RequesterAddress)
			handleTransferRequest(request.Context(), w, chatRequest, recorder)
			return
		} else {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
	}
}

func getExecutorForRequest(ctx context.Context, recorder cosmos_client.CosmosMessageClient) (*ExecutorDestination, error) {
	queryClient := recorder.NewInferenceQueryClient()
	response, err := queryClient.GetRandomExecutor(ctx, &types.QueryGetRandomExecutorRequest{})
	if err != nil {
		return nil, err
	}
	executor := response.Executor
	logging.Info("Executor selected", types.Inferences, "address", executor.Address, "url", executor.InferenceUrl)
	return &ExecutorDestination{
		Url:     executor.InferenceUrl,
		Address: executor.Address,
	}, nil
}

func handleTransferRequest(ctx context.Context, w http.ResponseWriter, request *ChatRequest, recorder cosmos_client.CosmosMessageClient) bool {
	logging.Debug("GET inference participant for transfer", types.Inferences, "address", request.RequesterAddress)

	queryClient := recorder.NewInferenceQueryClient()
	participant, err := queryClient.InferenceParticipant(ctx, &types.QueryInferenceParticipantRequest{Address: request.RequesterAddress})
	if err != nil {
		logging.Error("Failed to get inference participant", types.Inferences, "address", request.RequesterAddress, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}

	// Response is filled out with validate? Probably want to standardize
	hadError := validateClient(w, request, participant)
	if hadError {
		return true
	}

	executor, err := getExecutorForRequest(ctx, recorder)
	if err != nil {
		logging.Error("Failed to get executor", types.Inferences, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}

	seed := rand.Int31()
	inferenceUUID := uuid.New().String()
	inferenceRequest, err := createInferenceStartRequest(request, seed, inferenceUUID, executor)
	if err != nil {
		logging.Error("Failed to create inference start request", types.Inferences, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}

	go func() {
		logging.Debug("Starting inference", types.Inferences, "id", inferenceRequest.InferenceId)
		err := recorder.StartInference(inferenceRequest)
		if err != nil {
			logging.Error("Failed to submit MsgStartInference", types.Inferences, "id", inferenceRequest.InferenceId, "error", err)
		} else {
			logging.Debug("Submitted MsgStartInference", types.Inferences, "id", inferenceRequest.InferenceId)
		}
	}()

	// It's important here to send the ORIGINAL body, not the finalRequest body. The executor will AGAIN go through
	// the same process to create the same final request body
	logging.Debug("Sending request to executor", types.Inferences, "url", executor.Url, "seed", seed, "inferenceId", inferenceUUID)

	req, err := http.NewRequest("POST", executor.Url+"/v1/chat/completions", bytes.NewReader(request.Body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	req.Header.Set(utils.XInferenceIdHeader, inferenceUUID)
	req.Header.Set(utils.XSeedHeader, strconv.Itoa(int(seed)))
	req.Header.Set(utils.XPublicKeyHeader, participant.GetPubkey())
	req.Header.Set(utils.AuthorizationHeader, request.AuthKey)
	req.Header.Set("Content-Type", request.Request.Header.Get("Content-Type"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logging.Error("Failed to make http request to executor", types.Inferences, "error", err, "url", executor.Url)
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
		logging.Debug("Chunk", types.Inferences, "line", line)

		var lineToProxy = line
		if responseProcessor != nil {
			var err error
			lineToProxy, err = responseProcessor.ProcessStreamedResponse(line)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		logging.Debug("Chunk to proxy", types.Inferences, "line", lineToProxy)

		// Forward the line to the client
		_, err := fmt.Fprintln(w, lineToProxy)
		if err != nil {
			logging.Error("Error while streaming response", types.Inferences, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := scanner.Err(); err != nil {
		logging.Error("Error while streaming response", types.Inferences, "error", err)
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
			logging.Error("Failed to process inference node response", types.Inferences, "error", err)
			http.Error(w, "Failed to add ID to response", http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(resp.StatusCode)
	w.Write(bodyBytes) // TODO handle error??
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
		AssignedTo:    executor.Address,
	}
	return transaction, nil
}

func validateClient(w http.ResponseWriter, request *ChatRequest, client *types.QueryInferenceParticipantResponse) bool {
	if client == nil {
		logging.Error("Inference participant not found", types.Inferences, "address", request.RequesterAddress)
		http.Error(w, "Inference participant not found", http.StatusNotFound)
		return true
	}

	err := validateRequestAgainstPubKey(request, client.Pubkey)
	if err != nil {
		logging.Error("Unable to validate request against PubKey", types.Inferences, "error", err)
		http.Error(w, "Unable to validate request against PubKey:"+err.Error(), http.StatusUnauthorized)
		return true
	}
	if request.OpenAiRequest.MaxTokens == 0 {
		request.OpenAiRequest.MaxTokens = keeper.DefaultMaxTokens
	}
	escrowNeeded := request.OpenAiRequest.MaxTokens * keeper.PerTokenCost
	logging.Debug("Escrow needed", types.Inferences, "escrowNeeded", escrowNeeded)
	logging.Debug("Client balance", types.Inferences, "balance", client.Balance)
	if client.Balance < int64(escrowNeeded) {
		http.Error(w, "Insufficient balance", http.StatusPaymentRequired)
		return true
	}
	return false
}

func handleExecutorRequest(w http.ResponseWriter, request *ChatRequest, nodeBroker *broker.Broker, recorder cosmos_client.CosmosMessageClient, config *apiconfig.Config) bool {
	err := validateRequestAgainstPubKey(request, request.PubKey)
	if err != nil {
		http.Error(w, "Unable to validate request against PubKey:"+err.Error(), http.StatusUnauthorized)
		return true
	}

	seed, err := strconv.Atoi(request.Seed)
	if err != nil {
		logging.Warn("Unable to parse seed", types.Inferences, "seed", request.Seed)
		http.Error(w, "Unable to parse seed", http.StatusBadRequest)
		return true
	}

	modifiedRequestBody, err := completionapi.ModifyRequestBody(request.Body, int32(seed))
	if err != nil {
		logging.Warn("Unable to modify request body", types.Inferences, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}

	resp, err := broker.LockNode(nodeBroker, testModel, func(node *broker.Node) (*http.Response, error) {
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
		logging.Error("Failed to get response from inference node", types.Inferences, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := getInferenceErrorMessage(resp)
		logging.Warn("Inference node response with an error", types.Inferences, "code", resp.StatusCode, "msg", msg)
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
		logging.Error("Failed to send inference transaction", types.Inferences, "error", err)
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

func processGetInferenceParticipantByAddress(w http.ResponseWriter, request *http.Request, recorder cosmos_client.CosmosMessageClient) {
	// Manually extract the {id} from the URL path
	address := strings.TrimPrefix(request.URL.Path, "/v1/participants/")
	if address == "" {
		http.Error(w, "Address is required", http.StatusBadRequest)
		return
	}

	logging.Debug("GET inference participant", types.Inferences, "address", address)
	queryClient := recorder.NewInferenceQueryClient()
	response, err := queryClient.InferenceParticipant(request.Context(), &types.QueryInferenceParticipantRequest{Address: address})
	if err != nil {
		logging.Error("Failed to get inference participant", types.Inferences, "address", address, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if response == nil {
		logging.Error("Inference participant not found", types.Inferences, "address", address)
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

	logging.Debug("GET inference", types.Inferences, "id", id)
	queryClient := recorder.NewInferenceQueryClient()
	response, err := queryClient.Inference(request.Context(), &types.QueryGetInferenceRequest{Index: id})
	if err != nil {
		logging.Error("Failed to get inference", types.Inferences, "id", id, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if response == nil {
		logging.Error("Inference not found", types.Inferences, "id", id)
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

	// Submit to the blockchain effectively AFTER we've served the request. Speed before certainty.
	go func() {
		// PRTODO: delete me and probably introduce retries if FinishInference returns not found
		time.Sleep(10 * time.Second)
		logging.Debug("Submitting MsgFinishInference", types.Inferences, "inferenceId", id)
		err := recorder.FinishInference(message)
		if err != nil {
			logging.Error("Failed to submit MsgFinishInference", types.Inferences, "inferenceId", id, "error", err)
		} else {
			logging.Debug("Submitted MsgFinishInference", types.Inferences, "inferenceId", id)
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
	hash := utils.GenerateSHA256Hash(content)
	return hash, &response, nil
}

func getPromptHash(requestBytes []byte) (string, string, error) {
	canonicalJSON, err := utils.CanonicalizeJSON(requestBytes)
	if err != nil {
		return "", "", err
	}

	promptHash := utils.GenerateSHA256Hash(canonicalJSON)
	return promptHash, canonicalJSON, nil
}

func wrapValidation(nodeBroker *broker.Broker, recorder cosmos_client.CosmosMessageClient) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		var validationRequest ValidationRequest
		if err := json.NewDecoder(request.Body).Decode(&validationRequest); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		result, err := broker.LockNode(nodeBroker, testModel, func(node *broker.Node) (ValidationResult, error) {
			return validateByInferenceId(validationRequest.Id, node, recorder)
		})

		if err != nil {
			logging.Error("Failed to validate inference", types.Validation, "id", validationRequest.Id, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		msgVal, err := toMsgValidation(result)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err = recorder.ReportValidation(msgVal); err != nil {
			logging.Error("Failed to submit MsgValidation", types.Validation, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(msgVal.String())) // TODO: handle error??
	}
}

func wrapSubmitNewParticipant(recorder cosmos_client.CosmosMessageClient) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		logging.Debug("SubmitNewParticipant received", types.Participants, "method", request.Method)
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

func submitNewUnfundedParticipant(recorder cosmos_client.CosmosMessageClient, w http.ResponseWriter, body api.SubmitUnfundedNewParticipantDto) {
	msg := &inference.MsgSubmitNewUnfundedParticipant{
		Address:      body.Address,
		Url:          body.Url,
		Models:       body.Models,
		ValidatorKey: body.ValidatorKey,
		PubKey:       body.PubKey,
		WorkerKey:    body.WorkerKey,
	}

	logging.Debug("Submitting NewUnfundedParticipant", types.Participants, "message", msg)

	if err := recorder.SubmitNewUnfundedParticipant(msg); err != nil {
		logging.Error("Failed to submit MsgSubmitNewUnfundedParticipant", types.Participants, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func submitNewParticipant(recorder cosmos_client.CosmosMessageClient, w http.ResponseWriter, request *http.Request) {
	// Parse the request body into a SubmitNewParticipantDto
	var body api.SubmitUnfundedNewParticipantDto

	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		logging.Error("Failed to decode request body", types.Participants, "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logging.Debug("SubmitNewParticipantDto", types.Participants, "body", body)
	if body.Address != "" && body.PubKey != "" {
		submitNewUnfundedParticipant(recorder, w, body)
		return
	}

	msg := &inference.MsgSubmitNewParticipant{
		Url:          body.Url,
		Models:       body.Models,
		ValidatorKey: body.ValidatorKey,
		WorkerKey:    body.WorkerKey,
	}

	logging.Info("ValidatorKey in dapi", types.Participants, "key", body.ValidatorKey)
	if err := recorder.SubmitNewParticipant(msg); err != nil {
		logging.Error("Failed to submit MsgSubmitNewParticipant", types.Participants, "error", err)
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
		logging.Error("Failed to marshal response", types.Participants, "error", err)
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
				logging.Debug("Participant has no balance", types.Participants, "address", p.Address)
			}
		} else {
			logging.Warn("Failed to get balance for participant", types.Participants, "address", p.Address, "error", err)
		}
		participants[i] = ParticipantDto{
			Id:          p.Address,
			Url:         p.InferenceUrl,
			Models:      p.Models,
			CoinsOwed:   p.CoinBalance,
			Balance:     pBalance,
			VotingPower: int64(p.Weight),
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
