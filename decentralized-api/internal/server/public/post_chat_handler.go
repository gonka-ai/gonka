package public

import (
	"bytes"
	"context"
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
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func (s *Server) postChat(ctx echo.Context) error {
	logging.Debug("PostChat. Received request", types.Inferences, "path", ctx.Request().URL.Path)

	chatRequest, err := readRequest(ctx.Request())
	if err != nil {
		return err
	}

	if chatRequest.AuthKey == "" {
		logging.Warn("Request without authorization", types.Server, "path", ctx.Request().URL.Path)
		return ErrRequestAuth
	}

	if chatRequest.PubKey != "" && chatRequest.InferenceId != "" && chatRequest.Seed != "" {
		logging.Info("Executor request", types.Inferences, "inferenceId", chatRequest.InferenceId, "seed", chatRequest.Seed, "pubKey", chatRequest.PubKey)
		return s.handleExecutorRequest(chatRequest, ctx.Response().Writer)
	} else if chatRequest.RequesterAddress != "" {
		logging.Info("Transfer request", types.Inferences, "requesterAddress", chatRequest.RequesterAddress)
		return s.handleTransferRequest(ctx, chatRequest)
	} else {
		return echo.ErrBadRequest
	}
}

func (s *Server) handleTransferRequest(ctx echo.Context, request *ChatRequest) error {
	logging.Debug("GET inference participant for transfer", types.Inferences, "address", request.RequesterAddress)

	queryClient := s.recorder.NewInferenceQueryClient()
	participant, err := queryClient.InferenceParticipant(ctx.Request().Context(), &types.QueryInferenceParticipantRequest{Address: request.RequesterAddress})
	if err != nil {
		logging.Error("Failed to get inference participant", types.Inferences, "address", request.RequesterAddress, "error", err)
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

	if err := validateRequester(request, participant, promptTokenCount); err != nil {
		return err
	}

	executor, err := s.getExecutorForRequest(ctx.Request().Context(), request.OpenAiRequest.Model)
	if err != nil {
		logging.Error("Failed to get executor", types.Inferences, "error", err)
		return err
	}

	seed := rand.Int31()
	inferenceUUID := uuid.New().String()
	inferenceRequest, err := createInferenceStartRequest(request, seed, inferenceUUID, executor, s.configManager.GetCurrentNodeVersion(), promptTokenCount)
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
		request.PubKey = participant.GetPubkey()

		logging.Info("Execute request on same node, fill request with extra data", types.Inferences, "inferenceId", request.InferenceId, "seed", request.Seed, "pubKey", request.PubKey)
		return s.handleExecutorRequest(request, ctx.Response().Writer)
	}

	req, err := http.NewRequest(http.MethodPost, executor.Url+"/v1/chat/completions", bytes.NewReader(request.Body))
	if err != nil {
		return err
	}

	// TODO use echo.Redirect?
	req.Header.Set(utils.XInferenceIdHeader, inferenceUUID)
	req.Header.Set(utils.XSeedHeader, strconv.Itoa(int(seed)))
	req.Header.Set(utils.XPublicKeyHeader, participant.GetPubkey())
	req.Header.Set(utils.AuthorizationHeader, request.AuthKey)
	req.Header.Set("Content-Type", request.Request.Header.Get("Content-Type"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logging.Error("Failed to make http request to executor", types.Inferences, "error", err, "url", executor.Url)
		return err
	}
	defer resp.Body.Close()

	logging.Info("Proxying response from executor", types.Inferences, "inferenceId", inferenceUUID)
	proxyResponse(resp, ctx.Response().Writer, false, nil)
	return nil
}

func (s *Server) getPromptTokenEstimation(text string, model string) (int, error) {
	return len(text), nil
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

func (s *Server) handleExecutorRequest(request *ChatRequest, w http.ResponseWriter) error {
	inferenceId := request.InferenceId
	if err := validateRequestAgainstPubKey(request, request.PubKey); err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "Unable to validate request against PubKey:"+err.Error())
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
	proxyResponse(resp, w, true, responseProcessor)

	logging.Debug("Processing response from inference node", types.Inferences, "inferenceId", request.InferenceId)
	completionResponse, err := responseProcessor.GetResponse()

	if err != nil || completionResponse == nil {
		logging.Error("Failed to parse response data into CompletionResponse", types.Inferences, "error", err)
		return err
	}

	err = s.sendInferenceTransaction(request.InferenceId, completionResponse, modifiedRequestBody.NewBody, s.configManager.GetChainNodeConfig().AccountName)
	if err != nil {
		// Not http.Error, because we assume we already returned everything to the client during proxyResponse execution
		logging.Error("Failed to send inference transaction", types.Inferences, "error", err)
		return nil
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

func (s *Server) sendInferenceTransaction(inferenceId string, response completionapi.CompletionResponse, modifiedRequestBodyBytes []byte, accountName string) error {
	promptHash, promptPayload, err := getPromptHash(modifiedRequestBodyBytes)
	if err != nil {
		return err
	}

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
		promptText, err := s.extractPromptTextFromRequest(modifiedRequestBodyBytes)
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

	transaction := InferenceTransaction{
		PromptHash:           promptHash,
		PromptPayload:        promptPayload,
		ResponseHash:         responseHash,
		ResponsePayload:      string(bodyBytes),
		PromptTokenCount:     usage.PromptTokens,
		CompletionTokenCount: usage.CompletionTokens,
		Model:                model,
		Id:                   id,
	}

	if s.recorder != nil {
		s.submitInferenceFinishedTransaction(inferenceId, transaction, accountName)
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

func (s *Server) submitInferenceFinishedTransaction(id string, transaction InferenceTransaction, accountName string) {
	message := &inference.MsgFinishInference{
		Creator:              accountName,
		InferenceId:          id,
		ResponseHash:         transaction.ResponseHash,
		ResponsePayload:      transaction.ResponsePayload,
		PromptTokenCount:     transaction.PromptTokenCount,
		CompletionTokenCount: transaction.CompletionTokenCount,
		ExecutedBy:           accountName,
	}

	logging.Info("Submitting MsgFinishInference", types.Inferences, "inferenceId", id)
	err := s.recorder.FinishInference(message)
	if err != nil {
		logging.Error("Failed to submit MsgFinishInference", types.Inferences, "inferenceId", id, "error", err)
	} else {
		logging.Debug("Submitted MsgFinishInference", types.Inferences, "inferenceId", id)
	}
}

func createInferenceStartRequest(request *ChatRequest, seed int32, inferenceId string, executor *ExecutorDestination, nodeVersion string, promptTokenCount int) (*inference.MsgStartInference, error) {
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
	}

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

	err := validateRequestAgainstPubKey(request, requester.Pubkey)
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
