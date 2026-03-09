package public

import (
	"bytes"
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/completionapi"
	"decentralized-api/logging"
	"decentralized-api/semanticcache"
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

	// MaxRequestBodySize is the maximum allowed size for request bodies (10 MB)
	// This prevents memory exhaustion attacks from oversized requests
	MaxRequestBodySize = 10 * 1024 * 1024
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

func NewNoRedirectClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// emptyButParseableResponsePayload returns a deterministic "empty" response payload that:
// - is valid JSON parseable by older validators
// - yields no logits (so validator re-execution cannot meaningfully compare)
// - produces a stable response hash (hash is over these exact bytes)
//
// IMPORTANT: This payload is committed via `ResponseHash` on-chain and served to validators.
func emptyButParseableResponsePayload(inferenceId, model string, promptTokens uint64) *completionapi.JsonCompletionResponse {
	choice := completionapi.Choice{
		Index:        0,
		Message:      &completionapi.Message{Role: "assistant", Content: ""},
		FinishReason: "error",
		StopReason:   "",
	}
	// Provide a minimal synthetic logprob entry so older validators won't end up with:
	// - EnforcedTokens.Tokens == nil (marshals to {"tokens":null})
	// - or an error due to missing enforced tokens
	//
	// This must have TopLogprobs != nil AND len(TopLogprobs) > 0 to pass GetEnforcedTokens().
	choice.Logprobs.Content = []completionapi.Logprob{
		{
			Token:   "<EMPTY>",
			Logprob: 0,
			Bytes:   []int{},
			TopLogprobs: []completionapi.TopLogprobs{
				{Token: "<EMPTY>", Logprob: 0, Bytes: []int{}},
			},
		},
	}

	resp := completionapi.Response{
		ID:      inferenceId,
		Object:  "chat.completion",
		Created: 0,
		Model:   model,
		Choices: []completionapi.Choice{choice},
		Usage: completionapi.Usage{
			// Must be non-zero so `completionapi.JsonCompletionResponse.GetUsage()` won't error.
			// We set it to the best-effort prompt token count so MsgFinishInference can still charge.
			PromptTokens:     promptTokens,
			CompletionTokens: 0,
		},
	}

	b, err := json.Marshal(resp)
	if err != nil {
		// If marshaling fails, return error instead of generating a fallback response
		return nil
	}
	return &completionapi.JsonCompletionResponse{Bytes: b, Resp: resp}
}

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

	chatRequest, err := readRequest(ctx.Request(), ctx.Response().Writer, s.recorder.GetAccountAddress())
	if err != nil {
		return err
	}

	// Early TA whitelist check - covers both transfer and executor paths:
	// - Transfer requests: TransferAddress = this node's address (set by readRequest)
	// - Executor requests: TransferAddress = forwarding TA's address (from X-Transfer-Address header)
	if err := s.enforceTransferAgentAccess(chatRequest.TransferAddress); err != nil {
		return err
	}

	if chatRequest.AuthKey == "" {
		logging.Warn("Request without authorization", types.Server, "path", ctx.Request().URL.Path)
		return ErrRequestAuth
	}

	if chatRequest.OpenAiRequest.Model == "" {
		logging.Warn("Request without model", types.Server, "path", ctx.Request().URL.Path)
		return ErrNoModelSpecified
	}

	// Developer access gating: before a configured cutoff height, only allowlisted developers may use the public API
	// for both transfer-agent and executor request paths.
	if err := s.enforceDeveloperAccessGate(ctx.Request().Context(), chatRequest.RequesterAddress); err != nil {
		return err
	}

	if chatRequest.InferenceId != "" && chatRequest.Seed != "" {
		logging.Info("Executor request", types.Inferences, "inferenceId", chatRequest.InferenceId, "seed", chatRequest.Seed)
		return s.handleExecutorRequest(ctx, chatRequest, ctx.Response().Writer)
	} else {
		logging.Info("Transfer request", types.Inferences, "requesterAddress", chatRequest.RequesterAddress)
		return s.handleTransferRequest(ctx, chatRequest)
	}
}

func (s *Server) enforceDeveloperAccessGate(ctx context.Context, requesterAddress string) error {
	queryClient := s.recorder.NewInferenceQueryClient()
	paramsResp, err := queryClient.Params(ctx, &types.QueryParamsRequest{})
	if err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "unable to fetch chain params")
	}
	p := paramsResp.Params.DeveloperAccessParams
	if p == nil || p.UntilBlockHeight == 0 {
		return nil
	}

	status, err := s.recorder.Status(ctx)
	if err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "unable to fetch chain status")
	}
	currentHeight := status.SyncInfo.LatestBlockHeight
	if currentHeight >= p.UntilBlockHeight {
		return nil
	}

	for _, a := range p.AllowedDeveloperAddresses {
		if a == requesterAddress {
			return nil
		}
	}

	return echo.NewHTTPError(http.StatusForbidden, fmt.Sprintf("inference requests are restricted until block height %d", p.UntilBlockHeight))
}

// enforceTransferAgentAccess checks if the given TA address is in the whitelist.
// Returns nil if allowed, or a Forbidden error if not allowed.
func (s *Server) enforceTransferAgentAccess(taAddress string) error {
	cache := s.configManager.GetTransferAgentAccessCache()
	if !cache.IsEnabled {
		return nil // no restriction
	}
	if _, ok := cache.AllowedAddresses[taAddress]; ok {
		return nil
	}
	logging.Warn("Transfer Agent not in whitelist", types.Inferences, "address", taAddress)
	return echo.NewHTTPError(http.StatusForbidden, "Transfer Agent not allowed")
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

	if err := s.validateRequester(ctx.Request().Context(), request, requester, promptTokenCount); err != nil {
		return err
	}

	status, err := s.recorder.Status(context.Background())
	if err != nil {
		logging.Error("Failed to get status", types.Inferences, "error", err)
		return err
	}

	if err := validateRequest(request, status, s.configManager); err != nil {
		return err
	}

	requestBlockHeight := status.SyncInfo.LatestBlockHeight
	can, estimatedKB := s.bandwidthLimiter.CanAcceptRequest(requestBlockHeight, int(promptTokenCount), int(request.OpenAiRequest.MaxTokens))
	if !can {
		logging.Warn("Capacity limit exceeded", types.Inferences, "address", request.RequesterAddress)
		url := s.configManager.GetApiConfig().PublicUrl
		return echo.NewHTTPError(http.StatusTooManyRequests, "Transfer Agent capacity reached. Try another TA from "+url+"/v1/epochs/current/participants")
	}

	s.bandwidthLimiter.RecordRequest(requestBlockHeight, estimatedKB)
	defer s.bandwidthLimiter.ReleaseRequest(requestBlockHeight, estimatedKB)

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
		request.PromptHash = inferenceRequest.PromptHash

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
	req.Header.Set(utils.XPromptHashHeader, inferenceRequest.PromptHash)
	req.Header.Set("Content-Type", request.Request.Header.Get("Content-Type"))

	resp, err := s.httpClient.Do(req)
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
	logging.Info("Validating timestamp", types.Inferences,
		"timestampExpiration", validationParams.TimestampExpiration,
		"timestampAdvance", validationParams.TimestampAdvance,
		"lastHeightTime", lastHeightTime,
		"requestTimestamp", request.Timestamp)
	err := calculations.ValidateTimestamp(request.Timestamp, lastHeightTime, validationParams.TimestampExpiration, validationParams.TimestampAdvance, 0)

	if err != nil {
		logging.Warn("Invalid timestamp", types.Inferences,
			"inferenceId", request.InferenceId,
			"status", status,
			"error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
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

	response, err := broker.DoWithLockedNodeHTTPRetry(s.nodeBroker, model, nil, 1, func(node *broker.Node) (*http.Response, *broker.ActionError) {
		tokenizeUrl, err := url.JoinPath(node.InferenceUrlWithVersion(s.configManager.GetCurrentNodeVersion()), "/tokenize")
		if err != nil {
			return nil, broker.NewApplicationActionError(err)
		}

		reqBody := tokenizeRequest{
			Model:  model,
			Prompt: text,
		}
		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			return nil, broker.NewApplicationActionError(err)
		}

		resp, postErr := s.httpClient.Post(
			tokenizeUrl,
			"application/json",
			bytes.NewReader(jsonData),
		)
		if postErr != nil {
			return nil, broker.NewTransportActionError(postErr)
		}
		return resp, nil
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

	computedPromptHash, promptPayload, err := getModifiedPromptHash(modifiedRequestBody.NewBody)
	if err != nil {
		logging.Error("Failed to compute prompt hash", types.Inferences, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "Failed to compute prompt hash")
	}
	if request.PromptHash != "" && computedPromptHash != request.PromptHash {
		logging.Error("Prompt hash mismatch", types.Inferences,
			"expected", request.PromptHash, "computed", computedPromptHash)
		return echo.NewHTTPError(http.StatusBadRequest, "Prompt hash mismatch")
	}

	// ── Semantic cache lookup ────────────────────────────────────────────────
	// MsgStartInference is already sent (handleTransferRequest). The on-chain
	// inference cycle is open; MsgFinishInference MUST be sent regardless of
	// HIT or MISS so the node earns its CacheQualityWeight reward.
	//
	// Two levels checked in order:
	//   L1 — PromptHash exact-match: sha256(canonical JSON), O(1), 100% same result.
	//   L2 — cosine similarity: embedding via ML-node, probabilistic (≥ threshold).
	//
	// On HIT (either level): serve cached payload + call sendInferenceTransaction
	// to close the on-chain cycle. Node earns CacheQualityWeight for the reuse.
	// Streaming cannot be served from a cached JSON entry — skip for stream.
	var promptEmbedText string
	if s.semanticCache != nil && !request.OpenAiRequest.Stream {
		if t, err := s.extractPromptTextFromRequest(request.Body); err == nil {
			promptEmbedText = t
		}
	}
	var cachedEmbedding []float32 // reused at store time to avoid double embed
	var isL2ContextHit bool       // true when L2 context injection ran → coherence check needed
	var l2SimBps uint32           // similarity score of the matched L2 entry; used for adaptive coherence floor
	if s.semanticCache != nil && !request.OpenAiRequest.Stream {
		var currentEpoch uint64
		if epochState := s.phaseTracker.GetCurrentEpochState(); epochState != nil {
			currentEpoch = epochState.LatestEpoch.EpochIndex
		}

		// L1: PromptHash exact-match — checked first, no embedding needed.
		if l1cached, l1hit := s.semanticCache.LookupByPromptHash(computedPromptHash, currentEpoch); l1hit {
			if ok, reason := s.verifyCachedEntry(l1cached); !ok {
				logging.Warn("Semantic cache L1 HIT: verification failed — falling through",
					types.Inferences, "inferenceId", inferenceId, "reason", reason)
			} else {
				var cachedResp completionapi.Response
				if parseErr := json.Unmarshal(l1cached.ResponsePayload, &cachedResp); parseErr == nil {
					logging.Info("Semantic cache L1 HIT — serving cached result, closing on-chain cycle",
						types.Inferences, "inferenceId", inferenceId,
						"originalEpoch", l1cached.OriginalEpoch, "similarityBps", l1cached.SimilarityBps)
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-Cache", "HIT")
					w.Header().Set("X-Cache-Level", "1")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(l1cached.ResponsePayload)
					go func() {
						_ = s.semanticCache.RecordReuse(context.Background(),
							l1cached.OriginalParticipantAddress, currentEpoch, l1cached.SimilarityBps)
						if s.qualityReporter != nil {
							s.qualityReporter.RecordReuse(currentEpoch, l1cached.SimilarityBps)
						}
					}()
					cachedResponse := &completionapi.JsonCompletionResponse{
						Bytes: l1cached.ResponsePayload, Resp: cachedResp,
					}
					if txErr := s.sendInferenceTransaction(request.InferenceId, cachedResponse, request.Body,
						s.recorder.GetAccountAddress(), request, promptPayload); txErr != nil {
						logging.Error("Failed to send FinishInference for L1 cache hit",
							types.Inferences, "inferenceId", inferenceId, "error", txErr)
					}
					return nil
				}
			}
		}

		// L2: cosine similarity — embedding computed here, reused at store time.
		// Capture embedding from Lookup regardless of HIT/MISS so StoreResult
		// can reuse it on the MISS path — one ML-node embed call per request.
		cached, embedding, hit := s.semanticCache.Lookup(ctx.Request().Context(), []byte(promptEmbedText), currentEpoch)
		cachedEmbedding = embedding
		if hit {
			logging.Info("Semantic cache L2 HIT — context-augmented inference",
				types.Inferences,
				"inferenceId", inferenceId,
				"similarityBps", cached.SimilarityBps,
				"originalEpoch", cached.OriginalEpoch,
				"validUntilEpoch", cached.ValidUntilEpoch)

			// L2 context injection: do NOT return the cached payload directly.
			//
			// Returning a cached answer verbatim for a semantically similar but
			// distinct prompt would give a wrong answer (e.g. Counter mutex fix
			// served for a RateLimiter problem). Instead, inject the cached
			// answer as a structured reference context and run a fresh GPU
			// inference so the LLM can adapt the pattern to the current problem.
			//
			// This guarantees:
			//   1. Answer correctness — the model produces code for THIS prompt.
			//   2. Real-time learning — each epoch's solutions become reference
			//      context for the next, compounding quality over time.
			//   3. Honesty — CacheQualityWeight is earned by improving answers,
			//      not by returning potentially wrong cached bytes faster.
			//
			// L2 value is not "skipped GPU call" but "better GPU answer via
			// accumulated network knowledge".
			if cachedContent, ok := completionapi.ExtractCachedContent(cached.ResponsePayload); ok {
				injected, injectErr := completionapi.InjectCachedContext(request.Body, cachedContent)
				if injectErr != nil {
					logging.Warn("L2 context injection failed — falling through to plain GPU",
						types.Inferences, "inferenceId", inferenceId, "error", injectErr)
				} else {
					// Replace request body with context-augmented version.
					// ModifyRequestBody (seed, logprobs) is re-run below on this body.
					request.Body = injected
					// Re-derive the modified body with the injected context using the same seed.
					reinjected, reinjectErr := completionapi.ModifyRequestBody(request.Body, int32(seed))
					if reinjectErr == nil {
						modifiedRequestBody = reinjected
					}
					w.Header().Set("X-Cache", "CONTEXT-HIT")
					w.Header().Set("X-Cache-Level", "2")
					w.Header().Set("X-Cache-Similarity", strconv.Itoa(int(cached.SimilarityBps)))
					// X-Cache-Coherence will be set after GPU inference completes
					// (coherence is computed from the response, not available yet).
					go func() {
						_ = s.semanticCache.RecordReuse(
							context.Background(),
							cached.OriginalParticipantAddress,
							currentEpoch,
							cached.SimilarityBps,
						)
						if s.qualityReporter != nil {
							s.qualityReporter.RecordReuse(currentEpoch, cached.SimilarityBps)
						}
					}()
					isL2ContextHit = true
				l2SimBps = cached.SimilarityBps
				logging.Info("L2 context injected — proceeding to GPU inference",
					types.Inferences, "inferenceId", inferenceId,
					"similarityBps", cached.SimilarityBps)
					// fall through to GPU inference with augmented context
				}
			}
		}
	}
	// ── end semantic cache lookup ─────────────────────────────────────────────

	logging.Info("Attempting to lock node for inference", types.Inferences,
		"inferenceId", inferenceId, "nodeVersion", s.configManager.GetCurrentNodeVersion())
	resp, err := broker.DoWithLockedNodeHTTPRetry(s.nodeBroker, request.OpenAiRequest.Model, nil, 3, func(node *broker.Node) (*http.Response, *broker.ActionError) {
		logging.Info("Successfully acquired node lock for inference", types.Inferences,
			"inferenceId", inferenceId, "node", node.Id, "url", node.InferenceUrlWithVersion(s.configManager.GetCurrentNodeVersion()))

		completionsUrl, err := url.JoinPath(node.InferenceUrlWithVersion(s.configManager.GetCurrentNodeVersion()), "/v1/chat/completions")
		if err != nil {
			return nil, broker.NewApplicationActionError(err)
		}
		resp, postErr := s.httpClient.Post(
			completionsUrl,
			request.Request.Header.Get("Content-Type"),
			bytes.NewReader(modifiedRequestBody.NewBody),
		)
		if postErr != nil {
			return nil, broker.NewTransportActionError(postErr)
		}
		return resp, nil
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
		// If vLLM rejects the payload (400/422), still record a FinishInference with an empty response
		// so the inference lifecycle is closed on-chain.
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
			logging.Warn("Recording FinishInference with empty response due to inference node payload error", types.Inferences,
				"inferenceId", inferenceId, "code", resp.StatusCode)
			// Provide a parseable synthetic response payload so older validators can still unmarshal it.
			promptTokens := uint64(1)
			synthetic := emptyButParseableResponsePayload(inferenceId, request.OpenAiRequest.Model, promptTokens)
			if synthetic == nil {
				logging.Error("Failed to create synthetic response payload", types.Inferences, "inferenceId", inferenceId)
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create synthetic response payload")
			}
			if txErr := s.sendInferenceTransaction(request.InferenceId, synthetic, request.Body, s.recorder.GetAccountAddress(), request, promptPayload); txErr != nil {
				logging.Error("Failed to record FinishInference after inference node payload error", types.Inferences,
					"inferenceId", inferenceId, "error", txErr)
			}
			return echo.NewHTTPError(resp.StatusCode, msg)
		}
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

	err = s.sendInferenceTransaction(request.InferenceId, completionResponse, request.Body, s.recorder.GetAccountAddress(), request, promptPayload)
	if err != nil {
		// Not http.Error, because we assume we already returned everything to the client during proxyResponse execution
		logging.Error("Failed to send inference transaction", types.Inferences, "error", err)
		return nil
	}

	// ── Semantic cache store ──────────────────────────────────────────────────
	// After a successful GPU inference, seed the cache for future non-streaming requests.
	// Streaming responses are not cached: their format (SSE) cannot be replayed as JSON.
	// Best-effort and non-blocking; failures must not affect the user.
	if s.semanticCache != nil && !request.OpenAiRequest.Stream {
		bodyBytes, _ := completionResponse.GetBodyBytes()
		if bodyBytes != nil {
			var currentEpoch uint64
			if epochState := s.phaseTracker.GetCurrentEpochState(); epochState != nil {
				currentEpoch = epochState.LatestEpoch.EpochIndex
			}
			cacheEntry := semanticcache.CachedResult{
				PromptHash:                 computedPromptHash,
				ResponsePayload:            bodyBytes,
				ResponseHash:               utils.GenerateSHA256HashBytes(bodyBytes),
				InferenceId:                request.InferenceId,
				OriginalParticipantAddress: s.recorder.GetAccountAddress(),
			}
			go func() {
				storeCtx := context.Background()

			// ── Hub-level coherence + loop closure (L2 context hits only) ──
			//
			// Two-gate logical verification — no additional GPU inference call.
			// Both gates use the idle mlnode CPU embed already computed above.
			//
		// Gate 1 — Adaptive coherence floor (absolute):
		//   Verifies the GPU answer semantically addresses THIS prompt.
		//   Floor scales with sim tier: high-similarity pairs (structural
		//   twins, sim>8000) require a stricter floor because the risk of
		//   the model copying a wrong pattern verbatim is highest there.
		//   NOTE: floor for sim>8000 is 4500 (not 5000): code-embedding to
		//   NL-prompt cosine is structurally ~4800–5500 bps; 5000 causes
		//   false rejections for correct code responses.
		//     sim > 8000 bps → floor 4500  (structural twin zone)
		//     sim > 6250 bps → floor 4000  (clear zone)
		//     sim ≤ 6250 bps → floor 3000  (grey zone)
			//
			// Gate 2 — Loop closure (relative, hub frontier check):
			//   Verifies the context-injected answer is at/above the hub's
			//   current semantic frontier — the running avg coherence of all
			//   accepted entries. This IS the hub verify state: CoherenceStats()
			//   accumulates the frontier over time without any extra inference.
			//   If coherence(ctx) < frontier - 800 bps → the hub already knows
			//   better answers exist → don't store the degraded entry → the user
			//   still receives the answer (always deliver), but the hub pool is
			//   not polluted with below-frontier results.
			//
			// In both cases the user always gets the answer.
			// Only hub pool storage is gated — this is the PoC honest loop.
			if isL2ContextHit {
				if responseContent, ok := completionapi.ExtractCachedContent(bodyBytes); ok {
					responseEmbed, embedErr := s.semanticCache.EmbedText(storeCtx, []byte(responseContent))
					if embedErr != nil {
						logging.Warn("Coherence embed failed — storing without coherence score",
							types.Inferences, "inferenceId", inferenceId, "error", embedErr)
					} else {
						cacheEntry.CoherenceScoreBps = semanticcache.CosineBps(cachedEmbedding, responseEmbed)

							// Gate 1: adaptive absolute floor by sim tier.
						// See semanticcache.AdaptiveCoherenceFloor for calibration rationale.
						coherenceFloorBps := semanticcache.AdaptiveCoherenceFloor(l2SimBps)

						logging.Info("L2 coherence validated",
							types.Inferences,
							"inferenceId", inferenceId,
							"coherenceBps", cacheEntry.CoherenceScoreBps,
							"coherenceFloor", coherenceFloorBps,
							"l2SimBps", l2SimBps)

						if cacheEntry.CoherenceScoreBps < coherenceFloorBps {
							logging.Warn("L2 coherence below adaptive floor — skipping cache store",
								types.Inferences,
								"inferenceId", inferenceId,
								"coherenceBps", cacheEntry.CoherenceScoreBps,
								"floor", coherenceFloorBps,
								"l2SimBps", l2SimBps)
							s.semanticCache.RecordCoherenceResult(cacheEntry.CoherenceScoreBps, false)
							if s.qualityReporter != nil {
								s.qualityReporter.RecordCompute(currentEpoch)
							}
							return
						}

						// Gate 2: loop closure — hub frontier check.
						// fresh_baseline = hub's running avg coherence (accumulated semantic frontier).
						// Requires ≥10 accepted samples for reliable estimate; default 6000 bps
						// (conservative prior) until enough data is accumulated.
						const loopClosureMarginBps = 800
						const loopClosureMinSamples = 10
						// Default baseline 5500 (not 6000): code-embedding tasks produce
						// coherence in the 5000–6500 range; 6000 is too conservative during
						// cold start and causes false loop-closure breaks for correct Go code.
						// After loopClosureMinSamples entries the running avg takes over.
						const loopClosureDefaultBaselineBps = 5500
						ctxHits, rejections, coherenceSumBps := s.semanticCache.CoherenceStats()
						accepted := ctxHits - rejections
						freshBaseline := int64(loopClosureDefaultBaselineBps)
						if accepted >= loopClosureMinSamples {
							freshBaseline = coherenceSumBps / accepted
						}
							delta := int64(cacheEntry.CoherenceScoreBps) - freshBaseline
						if !semanticcache.LoopClosureOK(cacheEntry.CoherenceScoreBps, freshBaseline, loopClosureMarginBps) {
							logging.Warn("L2 loop closure BREAK — context degrades quality vs hub frontier",
								types.Inferences,
								"inferenceId", inferenceId,
								"coherenceBps", cacheEntry.CoherenceScoreBps,
								"hubFrontier", freshBaseline,
								"deltaBps", delta,
								"marginBps", -loopClosureMarginBps)
							s.semanticCache.RecordCoherenceResult(cacheEntry.CoherenceScoreBps, false)
							s.semanticCache.RecordLoopClosureBreak()
							if s.qualityReporter != nil {
								s.qualityReporter.RecordCompute(currentEpoch)
							}
							return
						}

						s.semanticCache.RecordCoherenceResult(cacheEntry.CoherenceScoreBps, true)
					}
				}
			}
			// ── end hub coherence + loop closure ────────────────────────

				if storeErr := s.semanticCache.StoreResult(storeCtx, []byte(promptEmbedText), cachedEmbedding, cacheEntry, currentEpoch); storeErr != nil {
					logging.Warn("Semantic cache store failed", types.Inferences,
						"inferenceId", inferenceId, "error", storeErr)
				} else if s.qualityReporter != nil {
					s.qualityReporter.RecordCompute(currentEpoch)
				}
			}()
		}
	}
	// ── end semantic cache store ──────────────────────────────────────────────

	return nil
}

func (s *Server) getAllowedPubKeys(ctx echo.Context, granterAddress string) ([]string, error) {
	return s.authzCache.GetPubKeys(ctx.Request().Context(), granterAddress, "/inference.inference.MsgStartInference")
}

func (s *Server) validateFullRequest(ctx echo.Context, request *ChatRequest) error {
	queryClient := s.recorder.NewInferenceQueryClient()
	dev, err := queryClient.InferenceParticipant(ctx.Request().Context(), &types.QueryInferenceParticipantRequest{Address: request.RequesterAddress})
	if err != nil {
		logging.Error("Failed to get inference requester", types.Inferences, "address", request.RequesterAddress, "error", err)
		return err
	}

	transferPubkeys, err := s.getAllowedPubKeys(ctx, request.TransferAddress)
	if err != nil {
		logging.Error("Failed to get grantees to sign inference", types.Inferences, "error", err)
		return err
	}
	logging.Info("Transfer pubkeys", types.Inferences, "pubkeys", transferPubkeys)

	if err := validateTransferRequest(request, dev.Pubkey); err != nil {
		logging.Error("Unable to validate request against PubKey", types.Inferences, "error", err)
		return echo.NewHTTPError(http.StatusUnauthorized, "Unable to validate request against PubKey:"+err.Error())
	}

	if err = validateExecuteRequestWithGrantees(request, transferPubkeys, s.recorder.GetAccountAddress(), request.TransferSignature); err != nil {
		logging.Error("Unable to validate request against TransferSignature", types.Inferences, "error", err)
		return echo.NewHTTPError(http.StatusUnauthorized, "Unable to validate request against TransferSignature:"+err.Error())
	}

	err = s.validateTimestampNonce(request)
	if err != nil {
		return err
	}
	return nil
}

func (s *Server) validateTimestampNonce(request *ChatRequest) error {
	status, err := s.recorder.Status(context.Background())
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
		// For now, we do NOT return an error here. This is solely harmful to EA with the current
		// scheme, and is happening during chain-slow periods regularly
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

	signerAddressStr := s.recorder.GetSignerAddress()
	signerAddress, err := sdk.AccAddressFromBech32(signerAddressStr)
	if err != nil {
		logging.Error("Failed to parse address", types.Inferences, "address", signerAddressStr, "error", err)
		return "", err
	}
	accountSigner := &cmd.AccountSigner{
		Addr:    signerAddress,
		Keyring: s.recorder.GetKeyring(),
	}

	signature, err := calculations.Sign(accountSigner, components, agentType)
	if err != nil {
		logging.Error("Failed to sign signature", types.Inferences, "error", err, "agentType", agentType)
		return "", err
	}

	return signature, nil
}

func (s *Server) sendInferenceTransaction(inferenceId string, response completionapi.CompletionResponse, requestBody []byte, executorAddress string, request *ChatRequest, promptPayload []byte) error {
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
		promptHash := utils.GenerateSHA256HashBytes(promptPayload)
		originalPromptHash := utils.GenerateSHA256HashBytes(request.Body)

		executorSignature, err := s.calculateSignature(promptHash, request.Timestamp, request.TransferAddress, executorAddress, calculations.ExecutorAgent)
		if err != nil {
			return err
		}

		message := &inference.MsgFinishInference{
			Creator:              executorAddress,
			InferenceId:          inferenceId,
			ResponseHash:         responseHash,
			PromptTokenCount:     usage.PromptTokens,
			CompletionTokenCount: usage.CompletionTokens,
			ExecutedBy:           executorAddress,
			TransferredBy:        request.TransferAddress,
			TransferSignature:    request.TransferSignature,
			ExecutorSignature:    executorSignature,
			RequestTimestamp:     request.Timestamp,
			RequestedBy:          request.RequesterAddress,
			Model:                model,
			PromptHash:           promptHash,
			OriginalPromptHash:   originalPromptHash,
		}

		// Store payloads before broadcasting transaction
		// If storage fails, we still proceed with broadcast (but log error)
		s.storePayloadsToStorage(request.Request.Context(), inferenceId, promptPayload, bodyBytes)

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

// verifyCachedEntry verifies payload integrity on a cache HIT.
//
// ResponseHash = sha256(ResponsePayload), identical to the value committed
// on-chain via MsgFinishInference.ResponseHash.  This is the same integrity
// check validators perform for fresh inferences — a cache entry is only served
// if its payload matches the on-chain hash exactly.
//
// BLSSignature (future): when the chain wires RequestThresholdSignature to
// MsgFinishInference, the stored BLSSignature will carry the quorum proof.
// Verification is done via bls.VerifyFinalSignature (already implemented in
// internal/bls/utils.go) against the epoch GroupPublicKey at store time —
// no gRPC calls needed on the HIT path.
func (s *Server) verifyCachedEntry(cached semanticcache.CachedResult) (bool, string) {
	computed := utils.GenerateSHA256HashBytes(cached.ResponsePayload)
	if cached.ResponseHash == "" || computed != cached.ResponseHash {
		return false, "response hash mismatch"
	}
	return true, ""
}

func (s *Server) storePayloadsToStorage(ctx context.Context, inferenceId string, promptPayload, responsePayload []byte) {
	if s.payloadStorage == nil {
		logging.Warn("Cannot store payload: payloadStorage is nil", types.Inferences, "inferenceId", inferenceId)
		return
	}
	if s.phaseTracker == nil {
		logging.Warn("Cannot store payload: phaseTracker is nil", types.Inferences, "inferenceId", inferenceId)
		return
	}

	epochState := s.phaseTracker.GetCurrentEpochState()
	if epochState == nil {
		logging.Warn("Cannot store payload: epoch state is nil", types.Inferences, "inferenceId", inferenceId)
		return
	}
	epochId := epochState.LatestEpoch.EpochIndex

	err := s.payloadStorage.Store(ctx, inferenceId, epochId, promptPayload, responsePayload)
	if err != nil {
		logging.Error("Failed to store payloads locally", types.Inferences, "inferenceId", inferenceId, "epochId", epochId, "error", err)
		return
	}
	logging.Debug("Stored payloads locally", types.Inferences, "inferenceId", inferenceId, "epochId", epochId)
}

func getModifiedPromptHash(requestBytes []byte) (string, []byte, error) {
	canonicalJSON, err := utils.CanonicalizeJSON(requestBytes)
	if err != nil {
		return "", nil, err
	}

	promptHash := utils.GenerateSHA256Hash(canonicalJSON)
	// By definition, canonicalize will only accept UTF-8, so straight conversion is safe
	return promptHash, []byte(canonicalJSON), nil
}

func createInferenceStartRequest(s *Server, request *ChatRequest, seed int32, inferenceId string, executor *ExecutorDestination, nodeVersion string, promptTokenCount int) (*inference.MsgStartInference, error) {
	modifiedRequest, err := completionapi.ModifyRequestBody(request.Body, seed)
	if err != nil {
		return nil, err
	}
	modifiedPromptHash, _, err := getModifiedPromptHash(modifiedRequest.NewBody)
	if err != nil {
		return nil, err
	}
	maxTokens := 0
	if request.OpenAiRequest.MaxCompletionTokens > 0 {
		maxTokens = int(request.OpenAiRequest.MaxCompletionTokens)
	} else if request.OpenAiRequest.MaxTokens > 0 {
		maxTokens = int(request.OpenAiRequest.MaxTokens)
	}

	originalPromptHash := utils.GenerateSHA256HashBytes(request.Body)

	transaction := &inference.MsgStartInference{
		InferenceId:        inferenceId,
		PromptHash:         modifiedPromptHash,
		RequestedBy:        request.RequesterAddress,
		Model:              request.OpenAiRequest.Model,
		AssignedTo:         executor.Address,
		NodeVersion:        nodeVersion,
		MaxTokens:          uint64(maxTokens),
		PromptTokenCount:   uint64(promptTokenCount),
		RequestTimestamp:   request.Timestamp,
		OriginalPromptHash: originalPromptHash,
	}

	signature, err := s.calculateSignature(modifiedPromptHash, request.Timestamp, request.TransferAddress, executor.Address, calculations.TransferAgent)
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

func readRequest(request *http.Request, writer http.ResponseWriter, transferAddress string) (*ChatRequest, error) {
	body, err := readRequestBody(request, writer)
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
		PromptHash:        request.Header.Get(utils.XPromptHashHeader),
	}, nil
}

func readRequestBody(r *http.Request, writer http.ResponseWriter) ([]byte, error) {
	// Limit request body size to prevent memory exhaustion attacks
	r.Body = http.MaxBytesReader(writer, r.Body, MaxRequestBodySize)
	defer r.Body.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// validateRequester validates requester with dynamic pricing fallback to legacy
func (s *Server) validateRequester(ctx context.Context, request *ChatRequest, requester *types.QueryInferenceParticipantResponse, promptTokenCount int) error {
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
		request.OpenAiRequest.MaxTokens = calculations.DefaultMaxTokens
	}

	var escrowNeeded uint64
	var perTokenPrice uint64

	// Try to get dynamic pricing first
	queryClient := s.recorder.NewInferenceQueryClient()
	priceResponse, err := queryClient.GetModelPerTokenPrice(ctx, &types.QueryGetModelPerTokenPriceRequest{
		ModelId: request.OpenAiRequest.Model,
	})

	if err == nil && priceResponse.Found {
		// Use dynamic pricing
		perTokenPrice = priceResponse.Price

		logging.Debug("Using dynamic pricing", types.Inferences,
			"perTokenPrice", perTokenPrice,
			"model", request.OpenAiRequest.Model)
	} else {
		// Fall back to legacy pricing
		logging.Warn("Failed to get dynamic pricing, falling back to legacy calculation", types.Inferences, "error", err)
		perTokenPrice = uint64(calculations.PerTokenCost)

		logging.Debug("Using legacy pricing", types.Inferences,
			"perTokenPrice", perTokenPrice)
	}

	// Calculate escrow using consistent formula: (PromptTokens + MaxTokens) × PerTokenPrice
	totalTokens := uint64(promptTokenCount) + uint64(request.OpenAiRequest.MaxTokens)
	escrowNeeded = totalTokens * perTokenPrice

	logging.Debug("Escrow calculation", types.Inferences,
		"escrowNeeded", escrowNeeded,
		"perTokenPrice", perTokenPrice,
		"promptTokens", promptTokenCount,
		"maxTokens", request.OpenAiRequest.MaxTokens,
		"totalTokens", totalTokens)

	logging.Debug("Client balance", types.Inferences, "balance", requester.Balance)
	if requester.Balance < int64(escrowNeeded) {
		return ErrInsufficientBalance
	}
	return nil
}
