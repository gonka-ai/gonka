package public

import (
	"bytes"
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/completionapi"
	"decentralized-api/logging"
	"decentralized-api/utils"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/cmd/inferenced/cmd"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// AuthKeyContext represents the context in which an AuthKey was used
type AuthKeyContext int

const (
	// TransferContext indicates the AuthKey was used for a transfer request
	TransferContext AuthKeyContext = 1
	// ExecutorContext indicates the AuthKey was used for an executor request
	ExecutorContext AuthKeyContext = 2
	// BothContexts indicates the AuthKey was used for both transfer and executor requests
	BothContexts = TransferContext | ExecutorContext
)

// Package-level variables for AuthKey reuse prevention
var (
	// Map for O(1) lookup of existing AuthKeys and their contexts
	usedAuthKeys = make(map[string]AuthKeyContext)

	// Map for O(1) lookup of what to remove, organized by block height
	authKeysByBlock = make(map[int64][]string)

	// Track the oldest block height we're storing
	oldestBlockHeight int64

	// Mutex for thread safety
	authKeysMutex sync.RWMutex

	// Reference to the config manager for accessing validation parameters
	configManagerRef *apiconfig.ConfigManager
)

// checkAndRecordAuthKey checks if an AuthKey has been used before and records it if not
// Returns true if the key has been used before in the specified context, false otherwise
func checkAndRecordAuthKey(authKey string, currentBlockHeight int64, context AuthKeyContext) bool {
	authKeysMutex.RLock()
	existingContext, exists := usedAuthKeys[authKey]
	authKeysMutex.RUnlock()

	if exists {
		// If the key exists, check if it's been used in the current context
		if existingContext&context != 0 {
			return true // Key was used before in this context
		}

		// Key exists but hasn't been used in this context, update the context
		authKeysMutex.Lock()
		defer authKeysMutex.Unlock()

		// Update the context to include the new context
		usedAuthKeys[authKey] = existingContext | context
		return false // Key wasn't used before in this context
	}

	// Key doesn't exist, add it with the current context
	authKeysMutex.Lock()
	defer authKeysMutex.Unlock()

	usedAuthKeys[authKey] = context

	authKeysByBlock[currentBlockHeight] = append(authKeysByBlock[currentBlockHeight], authKey)

	if oldestBlockHeight == 0 {
		oldestBlockHeight = currentBlockHeight
	}

	cleanupExpiredAuthKeys(currentBlockHeight)

	return false // Key wasn't used before
}

// cleanupExpiredAuthKeys removes auth keys from block heights based on timestamp_expiration parameter
func cleanupExpiredAuthKeys(currentBlockHeight int64) {
	// Default expiration is 4 blocks if configManager is not set
	expirationBlocks := int64(4)

	// If configManager is available, use twice the timestamp_expiration value
	if configManagerRef != nil {
		validationParams := configManagerRef.GetValidationParams()
		timestampExpiration := validationParams.TimestampExpiration

		// Use default value if parameter is not set
		if timestampExpiration == 0 {
			timestampExpiration = 10 // Default 10 seconds
		}

		// Use twice the timestamp_expiration value (converted to blocks)
		// Assuming average block time of 5 seconds
		expirationBlocks = (timestampExpiration * 2) / 4

		// Ensure we keep at least 4 blocks for safety
		if expirationBlocks < 4 {
			expirationBlocks = 4
		}

		logging.Debug("Auth key expiration", types.Inferences,
			"timestampExpiration", timestampExpiration,
			"expirationBlocks", expirationBlocks)
	}

	expirationHeight := currentBlockHeight - expirationBlocks

	for height := oldestBlockHeight; height < expirationHeight; height++ {
		keys, exists := authKeysByBlock[height]
		if !exists {
			continue
		}

		for _, key := range keys {
			delete(usedAuthKeys, key)
		}

		delete(authKeysByBlock, height)
	}

	if oldestBlockHeight < expirationHeight {
		oldestBlockHeight = expirationHeight
	}
}

func (s *Server) postChat(ctx echo.Context) error {
	logging.Debug("PostChat. Received request", types.Inferences, "path", ctx.Request().URL.Path)

	chatRequest, err := readRequest(ctx.Request(), s.recorder.GetAccountAddress())
	if err != nil {
		return err
	}

	if chatRequest.AuthKey == "" {
		logging.Warn("Request without authorization", types.Server, "path", ctx.Request().URL.Path)
		return ErrRequestAuth
	}

	if chatRequest.InferenceId != "" && chatRequest.Seed != "" {
		logging.Info("Executor request", types.Inferences, "inferenceId", chatRequest.InferenceId, "seed", chatRequest.Seed)
		return s.handleExecutorRequest(ctx, chatRequest, ctx.Response().Writer)
	} else {
		logging.Info("Transfer request", types.Inferences, "requesterAddress", chatRequest.RequesterAddress)
		return s.handleTransferRequest(ctx, chatRequest)
	}
}

func (s *Server) handleTransferRequest(ctx echo.Context, request *ChatRequest) error {
	logging.Debug("GET inference requester for transfer", types.Inferences, "address", request.RequesterAddress)

	queryClient := s.recorder.NewInferenceQueryClient()
	requester, err := queryClient.InferenceParticipant(ctx.Request().Context(), &types.QueryInferenceParticipantRequest{Address: request.RequesterAddress})
	if err != nil {
		logging.Error("Failed to get inference requester", types.Inferences, "address", request.RequesterAddress, "error", err)
		return err
	}

	promptText := ""
	for _, message := range request.OpenAiRequest.Messages {
		promptText += message.Content + "\n"
	}

	promptTokenCount, err := s.getPromptTokenEstimation(promptText, request.OpenAiRequest.Model)

	if err != nil {
		logging.Error("Failed to get prompt token estimation", types.Inferences, "error", err)
		return err
	}

	logging.Info("Prompt token estimation", types.Inferences, "count", promptTokenCount, "model", request.OpenAiRequest.Model)

	if err := validateRequester(request, requester, promptTokenCount); err != nil {
		return err
	}

	status, err := s.recorder.GetCosmosClient().Status(context.Background())
	if err != nil {
		logging.Error("Failed to get status", types.Inferences, "error", err)
		return err
	}

	if err := validateRequest(request, status, s.configManager); err != nil {
		return err
	}

	executor, err := s.getExecutorForRequest(ctx.Request().Context(), request.OpenAiRequest.Model)
	if err != nil {
		logging.Error("Failed to get executor", types.Inferences, "error", err)
		return err
	}

	seed := rand.Int31()
	inferenceUUID := request.AuthKey
	inferenceRequest, err := createInferenceStartRequest(s, request, seed, request.AuthKey, executor, s.configManager.GetCurrentNodeVersion(), promptTokenCount)
	if err != nil {
		logging.Error("Failed to create inference start request", types.Inferences, "error", err)
		return err
	}

	go func() {
		logging.Debug("Starting inference", types.Inferences, "id", inferenceRequest.InferenceId)
		if s.configManager.GetApiConfig().TestMode && request.OpenAiRequest.Seed == 8675309 {
			time.Sleep(10 * time.Second)
		}
		err := s.recorder.StartInference(inferenceRequest)
		if err != nil {
			logging.Error("Failed to submit MsgStartInference", types.Inferences, "id", inferenceRequest.InferenceId, "error", err)
		} else {
			logging.Debug("Submitted MsgStartInference", types.Inferences, "id", inferenceRequest.InferenceId)
		}
	}()

	// It's important here to send the ORIGINAL body, not the finalRequest body. The executor will AGAIN go through
	// the same process to create the same final request body
	logging.Debug("Sending request to executor", types.Inferences, "url", executor.Url, "seed", seed, "inferenceId", inferenceUUID)

	if s.configManager.GetApiConfig().PublicUrl == executor.Url {
		// node found itself as executor

		request.InferenceId = inferenceUUID
		request.Seed = strconv.Itoa(int(seed))
		request.TransferAddress = s.recorder.GetAccountAddress()
		request.TransferSignature = inferenceRequest.TransferSignature

		logging.Info("Execute request on same node, fill request with extra data", types.Inferences, "inferenceId", request.InferenceId, "seed", request.Seed)
		return s.handleExecutorRequest(ctx, request, ctx.Response().Writer)
	}

	req, err := http.NewRequest(http.MethodPost, executor.Url+"/v1/chat/completions", bytes.NewReader(request.Body))
	if err != nil {
		logging.Error("handleTransferRequest. Failed to create request to the executor node", types.Inferences, "error", err)
		return err
	}

	// TODO use echo.Redirect?
	req.Header.Set(utils.XInferenceIdHeader, inferenceUUID)
	req.Header.Set(utils.XSeedHeader, strconv.Itoa(int(seed)))
	req.Header.Set(utils.AuthorizationHeader, request.AuthKey)
	req.Header.Set(utils.XTimestampHeader, strconv.FormatInt(request.Timestamp, 10))
	req.Header.Set(utils.XTransferAddressHeader, request.TransferAddress)
	req.Header.Set(utils.XRequesterAddressHeader, request.RequesterAddress)
	req.Header.Set(utils.XTASignatureHeader, inferenceRequest.TransferSignature)
	req.Header.Set("Content-Type", request.Request.Header.Get("Content-Type"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logging.Error("Failed to make http request to executor", types.Inferences, "error", err, "url", executor.Url)
		return err
	}
	defer resp.Body.Close()

	logging.Info("Proxying response from executor", types.Inferences,
		"inferenceId", inferenceUUID,
		"executor", executor.Address)
	proxyResponse(resp, ctx.Response().Writer, false, nil, inferenceUUID)
	return nil
}

func (s *Server) getPromptTokenEstimation(text string, model string) (int, error) {
	return len(text), nil
}

func validateRequest(request *ChatRequest, status *coretypes.ResultStatus, configManager *apiconfig.ConfigManager) error {
	lastHeightTime := status.SyncInfo.LatestBlockTime.UnixNano()
	currentBlockHeight := status.SyncInfo.LatestBlockHeight

	// Get validation parameters from config
	validationParams := configManager.GetValidationParams()
	timestampExpirationNs := validationParams.TimestampExpiration * int64(time.Second)
	timestampAdvanceNs := validationParams.TimestampAdvance * int64(time.Second)

	// Use default values if parameters are not set
	if timestampExpirationNs == 0 {
		timestampExpirationNs = 10 * int64(time.Second)
	}
	if timestampAdvanceNs == 0 {
		timestampAdvanceNs = 10 * int64(time.Second)
	}

	requestOffset := lastHeightTime - request.Timestamp
	logging.Info("Request offset", types.Inferences,
		"offset", time.Duration(requestOffset).String(),
		"lastHeightTime", lastHeightTime,
		"requestTimestamp", request.Timestamp)

	if requestOffset > timestampExpirationNs {
		return echo.NewHTTPError(http.StatusBadRequest, "Request timestamp is too old")
	}

	if requestOffset < -timestampAdvanceNs {
		return echo.NewHTTPError(http.StatusBadRequest, "Request timestamp is in the future")
	}

	// Check if AuthKey has been used before for a transfer request
	if checkAndRecordAuthKey(request.AuthKey, currentBlockHeight, TransferContext) {
		logging.Warn("AuthKey reuse detected for transfer request", types.Inferences, "authKey", request.AuthKey)
		return echo.NewHTTPError(http.StatusBadRequest, "AuthKey has already been used for a transfer request")
	}

	return nil
}

func (s *Server) getPromptTokenCount(text string, model string) (int, error) {
	type tokenizeRequest struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
	}
	type tokenizeResponse struct {
		TokenCount int `json:"count"`
	}

	response, err := broker.LockNode(s.nodeBroker, model, s.configManager.GetCurrentNodeVersion(), func(node *broker.Node) (*http.Response, error) {
		tokenizeUrl, err := url.JoinPath(node.InferenceUrl(), "/tokenize")
		if err != nil {
			return nil, err
		}

		reqBody := tokenizeRequest{
			Model:  model,
			Prompt: text,
		}
		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			return nil, err
		}

		return http.Post(
			tokenizeUrl,
			"application/json",
			bytes.NewReader(jsonData),
		)
	})

	if err != nil {
		return 0, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("tokenize request failed with status: %d", response.StatusCode)
	}

	var result tokenizeResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return 0, err
	}

	return result.TokenCount, nil
}

func (s *Server) extractPromptTextFromRequest(requestBytes []byte) (string, error) {
	var openAiRequest OpenAiRequest
	err := json.Unmarshal(requestBytes, &openAiRequest)
	if err != nil {
		return "", err
	}

	promptText := ""
	for _, message := range openAiRequest.Messages {
		promptText += message.Content + "\n"
	}
	return promptText, nil
}

func (s *Server) handleExecutorRequest(ctx echo.Context, request *ChatRequest, w http.ResponseWriter) error {
	inferenceId := request.InferenceId
	err := s.validateFullRequest(ctx, request)
	if err != nil {
		return err
	}

	seed, err := strconv.Atoi(request.Seed)
	if err != nil {
		logging.Warn("Unable to parse seed", types.Inferences, "seed", request.Seed)
		return echo.ErrBadRequest
	}

	modifiedRequestBody, err := completionapi.ModifyRequestBody(request.Body, int32(seed))
	if err != nil {
		logging.Warn("Unable to modify request body", types.Inferences, "error", err)
		return err
	}

	logging.Info("Attempting to lock node for inference", types.Inferences,
		"inferenceId", inferenceId, "nodeVersion", s.configManager.GetCurrentNodeVersion())
	resp, err := broker.LockNode(s.nodeBroker, request.OpenAiRequest.Model, s.configManager.GetCurrentNodeVersion(), func(node *broker.Node) (*http.Response, error) {
		logging.Info("Successfully acquired node lock for inference", types.Inferences,
			"inferenceId", inferenceId, "node", node.Id, "url", node.InferenceUrl())

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
		logging.Error("Failed to get response from inference node", types.Inferences,
			"inferenceId", inferenceId, "error", err)
		return err
	}
	defer resp.Body.Close()

	logging.Info("Node lock released for inference", types.Inferences, "inferenceId", inferenceId)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := getInferenceErrorMessage(resp)
		logging.Warn("Inference node response with an error", types.Inferences, "code", resp.StatusCode, "msg", msg)
		return echo.NewHTTPError(http.StatusInternalServerError, msg)
	}

	responseProcessor := completionapi.NewExecutorResponseProcessor(request.InferenceId)
	logging.Debug("Proxying response from inference node", types.Inferences, "inferenceId", request.InferenceId)
	proxyResponse(resp, w, true, responseProcessor, inferenceId)

	logging.Debug("Processing response from inference node", types.Inferences, "inferenceId", request.InferenceId)
	completionResponse, err := responseProcessor.GetResponse()

	if err != nil || completionResponse == nil {
		logging.Error("Failed to parse response data into CompletionResponse", types.Inferences, "error", err)
		return err
	}

	err = s.sendInferenceTransaction(request.InferenceId, completionResponse, request.Body, s.recorder.GetAccountAddress(), request)
	if err != nil {
		// Not http.Error, because we assume we already returned everything to the client during proxyResponse execution
		logging.Error("Failed to send inference transaction", types.Inferences, "error", err)
		return nil
	}
	return nil
}

func (s *Server) validateFullRequest(ctx echo.Context, request *ChatRequest) error {
	queryClient := s.recorder.NewInferenceQueryClient()
	dev, err := queryClient.InferenceParticipant(ctx.Request().Context(), &types.QueryInferenceParticipantRequest{Address: request.RequesterAddress})
	if err != nil {
		logging.Error("Failed to get inference requester", types.Inferences, "address", request.RequesterAddress, "error", err)
		return err
	}

	transfer, err := queryClient.InferenceParticipant(ctx.Request().Context(), &types.QueryInferenceParticipantRequest{Address: request.TransferAddress})
	if err != nil {
		logging.Error("Failed to get transfer participant", types.Inferences, "address", request.TransferAddress, "error", err)
		return err
	}

	if err := validateTransferRequest(request, dev.Pubkey); err != nil {
		logging.Error("Unable to validate request against PubKey", types.Inferences, "error", err)
		return echo.NewHTTPError(http.StatusUnauthorized, "Unable to validate request against PubKey:"+err.Error())
	}

	if err = validateExecuteRequest(request, transfer.Pubkey, s.recorder.GetAccountAddress(), request.TransferSignature); err != nil {
		logging.Error("Unable to validate request against TransferSignature", types.Inferences, "error", err)
		return echo.NewHTTPError(http.StatusUnauthorized, "Unable to validate request against TransferSignature:"+err.Error())
	}

	err = s.validateTimestampNonce(err, request)
	if err != nil {
		return err
	}
	return nil
}

func (s *Server) validateTimestampNonce(err error, request *ChatRequest) error {
	status, err := s.recorder.GetCosmosClient().Status(context.Background())
	if err != nil {
		logging.Error("Failed to get status", types.Inferences, "error", err)
		return err
	}

	currentBlockHeight := status.SyncInfo.LatestBlockHeight
	lastHeightTime := status.SyncInfo.LatestBlockTime.UnixNano()

	// Get validation parameters from config
	validationParams := s.configManager.GetValidationParams()
	timestampExpirationNs := validationParams.TimestampExpiration * int64(time.Second)
	timestampAdvanceNs := validationParams.TimestampAdvance * int64(time.Second)

	// Use default values if parameters are not set
	if timestampExpirationNs == 0 {
		timestampExpirationNs = 10 * int64(time.Second)
	}
	if timestampAdvanceNs == 0 {
		timestampAdvanceNs = 10 * int64(time.Second)
	}

	requestOffset := lastHeightTime - request.Timestamp
	logging.Info("Request offset for executor", types.Inferences,
		"offset", time.Duration(requestOffset).String(),
		"lastHeightTime", lastHeightTime,
		"requestTimestamp", request.Timestamp)

	if requestOffset > timestampExpirationNs {
		logging.Warn("Request timestamp is too old", types.Inferences,
			"inferenceId", request.InferenceId,
			"offset", time.Duration(requestOffset).String())
		return echo.NewHTTPError(http.StatusBadRequest, "Request timestamp is too old")
	}

	if requestOffset < -timestampAdvanceNs {
		logging.Warn("Request timestamp is in the future", types.Inferences,
			"inferenceId", request.InferenceId,
			"offset", time.Duration(requestOffset).String())
		return echo.NewHTTPError(http.StatusBadRequest, "Request timestamp is in the future")
	}

	if checkAndRecordAuthKey(request.AuthKey, currentBlockHeight, ExecutorContext) {
		logging.Warn("AuthKey reuse detected for executor request", types.Inferences, "authKey", request.AuthKey)
		return echo.NewHTTPError(http.StatusBadRequest, "AuthKey has already been used for an executor request")
	}
	return nil
}

func (s *Server) getExecutorForRequest(ctx context.Context, model string) (*ExecutorDestination, error) {
	queryClient := s.recorder.NewInferenceQueryClient()
	response, err := queryClient.GetRandomExecutor(ctx, &types.QueryGetRandomExecutorRequest{
		Model: model,
	})
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

// calculateSignature calculates a signature for the given components and agent type
func (s *Server) calculateSignature(payload string, timestamp int64, transferAddress string, executorAddress string, agentType calculations.SignatureType) (string, error) {
	components := calculations.SignatureComponents{
		Payload:         payload,
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
		ExecutorAddress: executorAddress,
	}

	address, err := sdk.AccAddressFromBech32(s.recorder.GetAccountAddress())
	if err != nil {
		logging.Error("Failed to parse address", types.Inferences, "address", s.recorder.GetAccountAddress(), "error", err)
		return "", err
	}

	accountSigner := &cmd.AccountSigner{
		Addr:    address,
		Context: s.recorder.GetCosmosClient().Context(),
	}

	signature, err := calculations.Sign(accountSigner, components, agentType)
	if err != nil {
		logging.Error("Failed to sign signature", types.Inferences, "error", err, "agentType", agentType)
		return "", err
	}

	return signature, nil
}

func (s *Server) sendInferenceTransaction(inferenceId string, response completionapi.CompletionResponse, requestBody []byte, executorAddress string, request *ChatRequest) error {
	responseHash, err := response.GetHash()
	if err != nil || responseHash == "" {
		logging.Error("Failed to get responseHash from response", types.Inferences, "error", err)
		return err
	}
	model, err := response.GetModel()
	if err != nil || model == "" {
		logging.Error("Failed to get model from response", types.Inferences, "error", err)
		return err
	}
	id, err := response.GetInferenceId()
	if err != nil || id == "" {
		logging.Error("Failed to get id from response", types.Inferences, "error", err)
		return err
	}
	usage, err := response.GetUsage()
	if err != nil {
		logging.Warn("Failed to get usage from response", types.Inferences, "error", err)
		return err
	}

	// If streaming response doesn't have prompt tokens, get accurate count via tokenization
	if usage.PromptTokens == 0 {
		logging.Info("Streaming response missing prompt tokens, using tokenization", types.Inferences, "inferenceId", inferenceId)
		promptText, err := s.extractPromptTextFromRequest(requestBody)
		if err != nil {
			logging.Warn("Failed to extract prompt text for tokenization", types.Inferences, "error", err)
		} else {
			model, _ := response.GetModel()
			actualPromptTokens, err := s.getPromptTokenCount(promptText, model)
			if err != nil {
				logging.Warn("Failed to get actual prompt token count", types.Inferences, "error", err)
			} else {
				logging.Info("Updated prompt tokens via tokenization", types.Inferences, "inferenceId", inferenceId, "tokens", actualPromptTokens)
				usage.PromptTokens = uint64(actualPromptTokens)
			}
		}
	}

	logging.Debug("Usage from response", types.Inferences, "usage", usage)
	bodyBytes, err := response.GetBodyBytes()
	if err != nil || bodyBytes == nil {
		logging.Error("Failed to get body bytes from response", types.Inferences, "error", err)
		return err
	}

	if s.recorder != nil {
		// Calculate executor signature
		executorSignature, err := s.calculateSignature(string(request.Body), request.Timestamp, request.TransferAddress, executorAddress, calculations.ExecutorAgent)
		if err != nil {
			return err
		}

		message := &inference.MsgFinishInference{
			Creator:              executorAddress,
			InferenceId:          inferenceId,
			ResponseHash:         responseHash,
			ResponsePayload:      string(bodyBytes),
			PromptTokenCount:     usage.PromptTokens,
			CompletionTokenCount: usage.CompletionTokens,
			ExecutedBy:           executorAddress,
			TransferredBy:        request.TransferAddress,
			TransferSignature:    request.TransferSignature,
			ExecutorSignature:    executorSignature,
			RequestTimestamp:     request.Timestamp,
			RequestedBy:          request.RequesterAddress,
			OriginalPrompt:       string(request.Body),
		}

		logging.Info("Submitting MsgFinishInference", types.Inferences, "inferenceId", inferenceId)
		err = s.recorder.FinishInference(message)
		if err != nil {
			logging.Error("Failed to submit MsgFinishInference", types.Inferences, "inferenceId", inferenceId, "error", err)
		} else {
			logging.Debug("Submitted MsgFinishInference", types.Inferences, "inferenceId", inferenceId)
		}
	}
	return nil
}

func getPromptHash(requestBytes []byte) (string, string, error) {
	canonicalJSON, err := utils.CanonicalizeJSON(requestBytes)
	if err != nil {
		return "", "", err
	}

	promptHash := utils.GenerateSHA256Hash(canonicalJSON)
	return promptHash, canonicalJSON, nil
}

func createInferenceStartRequest(s *Server, request *ChatRequest, seed int32, inferenceId string, executor *ExecutorDestination, nodeVersion string, promptTokenCount int) (*inference.MsgStartInference, error) {
	finalRequest, err := completionapi.ModifyRequestBody(request.Body, seed)
	if err != nil {
		return nil, err
	}
	promptHash, promptPayload, err := getPromptHash(finalRequest.NewBody)
	if err != nil {
		return nil, err
	}
	maxTokens := 0
	if request.OpenAiRequest.MaxCompletionTokens > 0 {
		maxTokens = int(request.OpenAiRequest.MaxCompletionTokens)
	} else if request.OpenAiRequest.MaxTokens > 0 {
		maxTokens = int(request.OpenAiRequest.MaxTokens)
	}
	transaction := &inference.MsgStartInference{
		InferenceId:      inferenceId,
		PromptHash:       promptHash,
		PromptPayload:    promptPayload,
		RequestedBy:      request.RequesterAddress,
		Model:            request.OpenAiRequest.Model,
		AssignedTo:       executor.Address,
		NodeVersion:      nodeVersion,
		MaxTokens:        uint64(maxTokens),
		PromptTokenCount: uint64(promptTokenCount),
		RequestTimestamp: request.Timestamp,
		OriginalPrompt:   string(request.Body),
	}

	signature, err := s.calculateSignature(string(request.Body), request.Timestamp, request.TransferAddress, executor.Address, calculations.TransferAgent)
	if err != nil {
		return nil, err
	}
	transaction.TransferSignature = signature

	logging.Debug("Prompt token count for inference", types.Inferences, "inferenceId", inferenceId, "count", promptTokenCount)
	return transaction, nil
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

func readRequest(request *http.Request, transferAddress string) (*ChatRequest, error) {
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

	timestamp, err := strconv.ParseInt(request.Header.Get(utils.XTimestampHeader), 10, 64)
	if err != nil {
		timestamp = 0
	}
	if request.Header.Get(utils.XTransferAddressHeader) != "" {
		transferAddress = request.Header.Get(utils.XTransferAddressHeader)
	}

	return &ChatRequest{
		Body:              body,
		Request:           request,
		OpenAiRequest:     openAiRequest,
		AuthKey:           request.Header.Get(utils.AuthorizationHeader),
		Seed:              request.Header.Get(utils.XSeedHeader),
		InferenceId:       request.Header.Get(utils.XInferenceIdHeader),
		RequesterAddress:  request.Header.Get(utils.XRequesterAddressHeader),
		Timestamp:         timestamp,
		TransferAddress:   transferAddress,
		TransferSignature: request.Header.Get(utils.XTASignatureHeader),
	}, nil
}

func readRequestBody(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r.Body); err != nil {
		return nil, err
	}
	defer r.Body.Close()
	return buf.Bytes(), nil
}

// Check signature and available balance.
func validateRequester(request *ChatRequest, requester *types.QueryInferenceParticipantResponse, promptTokenCount int) error {
	if requester == nil {
		logging.Error("Inference participant not found", types.Inferences, "address", request.RequesterAddress)
		return ErrInferenceParticipantNotFound
	}

	err := validateTransferRequest(request, requester.Pubkey)
	if err != nil {
		logging.Error("Unable to validate request against PubKey", types.Inferences, "error", err)
		return echo.NewHTTPError(http.StatusUnauthorized, "Unable to validate request against PubKey:"+err.Error())
	}

	if request.OpenAiRequest.MaxTokens == 0 {
		request.OpenAiRequest.MaxTokens = keeper.DefaultMaxTokens
	}

	// Calculate escrow needed based on both max tokens and prompt token estimation
	maxTokensCost := uint64(request.OpenAiRequest.MaxTokens) * uint64(calculations.PerTokenCost)

	// Use the promptTokenCount parameter that was passed in (estimation for escrow)
	promptTokensCost := uint64(promptTokenCount) * uint64(calculations.PerTokenCost)

	escrowNeeded := maxTokensCost + promptTokensCost
	logging.Debug("Escrow needed (using estimation)", types.Inferences, "escrowNeeded", escrowNeeded, "maxTokensCost", maxTokensCost, "promptTokensCost", promptTokensCost)
	logging.Debug("Client balance", types.Inferences, "balance", requester.Balance)
	if requester.Balance < int64(escrowNeeded) {
		return ErrInsufficientBalance
	}
	return nil
}
