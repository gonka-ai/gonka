package devshard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"decentralized-api/completionapi"
	"decentralized-api/internal/server/public"
	validationpkg "decentralized-api/internal/validation"
	"decentralized-api/logging"
	"decentralized-api/payloadstorage"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/cmd/inferenced/cmd"
	"github.com/productscience/inference/x/inference/calculations"
	chaintypes "github.com/productscience/inference/x/inference/types"

	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/observability"
	devshardserver "devshard/server"
)

type MLRequestExecutor func(ctx context.Context, model string, body []byte) (*http.Response, error)

const (
	MLNodeHTTPTimeout   = 10 * time.Minute
	PayloadFetchTimeout = 30 * time.Second
)

func ExecuteInferenceWithExecutor(
	ctx context.Context,
	req devshardpkg.ExecuteRequest,
	payloadStore payloadstorage.PayloadStorage,
	payloadEpoch uint64,
	execute MLRequestExecutor,
	chainParams ChainParamsProvider,
) (*devshardpkg.ExecuteResult, error) {
	seed := int32(req.InferenceID)
	inferenceID := fmt.Sprintf("devshard-%s-%d", req.EscrowID, req.InferenceID)

	modified, err := completionapi.ModifyRequestBodyWithLogprobsMode(req.Prompt, seed, chainParams.LogprobsMode())
	if err != nil {
		return nil, observability.Classify(observability.ReasonModifyRequestErr, observability.WhereRuntimeModifyRequest, fmt.Errorf("modify request body: %w", err))
	}

	resp, err := execute(ctx, req.Model, modified.NewBody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return nil, observability.Classify(observability.ReasonHTTP5xx, observability.WhereEngineMLNodeCall, fmt.Errorf("mlnode status %d", resp.StatusCode))
	}
	if resp.StatusCode >= 400 {
		return nil, observability.Classify(observability.ReasonHTTP4xx, observability.WhereEngineMLNodeCall, fmt.Errorf("mlnode status %d", resp.StatusCode))
	}

	processed, err := ProcessExecutionHTTPResponse(req, resp, inferenceID)
	if err != nil {
		return nil, err
	}
	observability.ObserveTokens(observability.PathExecute, "", observability.TokenKindPrompt, processed.InputTokens)
	observability.ObserveTokens(observability.PathExecute, "", observability.TokenKindCompletion, processed.OutputTokens)

	// Store the canonicalized ORIGINAL prompt (not the modified one with seed).
	promptPayload, err := devshardpkg.CanonicalizeJSON(req.Prompt)
	if err != nil {
		return nil, observability.Classify(observability.ReasonCanonicalizePromptErr, observability.WhereRuntimeStorePayloads, fmt.Errorf("canonicalize prompt: %w", err))
	}

	if err := payloadStore.Store(
		ctx,
		devshardserver.PayloadKey(req.EscrowID, req.InferenceID),
		payloadEpoch,
		promptPayload,
		processed.ResponseBody,
	); err != nil {
		return nil, observability.Classify(observability.ReasonPayloadStoreErr, observability.WhereRuntimeStorePayloads, fmt.Errorf("store payloads: %w", err))
	}

	return &devshardpkg.ExecuteResult{
		ResponseHash:          processed.ResponseHash,
		InputTokens:           processed.InputTokens,
		OutputTokens:          processed.OutputTokens,
		ResponseBody:          processed.ResponseBody,
		PartialResponse:       processed.PartialResponse,
		PartialResponseReason: processed.PartialResponseReason,
		PartialResponseWhere:  processed.PartialResponseWhere,
	}, nil
}

func ValidateInferenceWithExecutor(
	ctx context.Context,
	req devshardpkg.ValidateRequest,
	httpClient *http.Client,
	br bridge.MainnetBridge,
	recorder PayloadAuthClient,
	payloadEpoch uint64,
	requestPath string,
	execute MLRequestExecutor,
	chainParams ChainParamsProvider,
	thresholds *ValidationThresholdResolver,
) (*devshardpkg.ValidateResult, error) {
	inferenceID := strconv.FormatUint(req.InferenceID, 10)

	promptPayload, responsePayload, err := FetchPayloadsFromExecutor(
		ctx,
		httpClient,
		br,
		recorder,
		req,
		inferenceID,
		payloadEpoch,
		requestPath,
	)
	if err != nil {
		return nil, observability.Classify(observability.ReasonPayloadFetchErr, observability.WhereRuntimeFetchPayloads, fmt.Errorf("fetch payloads from executor: %w", err))
	}

	validationBody, err := BuildValidationBody(promptPayload, responsePayload, req.InferenceID, chainParams)
	if err != nil {
		return nil, err
	}

	resp, err := execute(ctx, req.Model, validationBody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return EvaluateValidationResponse(ctx, resp, req, inferenceID, responsePayload, thresholds)
}

type ProcessedExecutionResponse struct {
	ResponseHash          []byte
	InputTokens           uint64
	OutputTokens          uint64
	ResponseBody          []byte
	PartialResponse       bool
	PartialResponseReason string
	PartialResponseWhere  string
}

func ProcessExecutionHTTPResponse(
	req devshardpkg.ExecuteRequest,
	resp *http.Response,
	inferenceID string,
) (*ProcessedExecutionResponse, error) {
	processor := completionapi.NewExecutorResponseProcessor(inferenceID)

	contentType := resp.Header.Get("Content-Type")
	isSSE := strings.HasPrefix(contentType, "text/event-stream")

	var processErr error
	if req.ResponseWriter != nil && isSSE {
		if err := public.ProxyResponse(resp, req.ResponseWriter, true, processor, inferenceID); err != nil {
			processErr = observability.Classify(observability.ReasonResponseWriteErr, observability.WhereRuntimeWriteClientResponse, err)
		}
	} else {
		if err := completionapi.ProcessHTTPResponse(resp, processor); err != nil {
			processErr = observability.Classify(observability.ReasonResponseProcessErr, observability.WhereRuntimeProcessExecution, fmt.Errorf("process response: %w", err))
		}
	}

	processed, err := buildProcessedExecutionResponse(req, processor, isSSE)
	if err != nil {
		if processErr != nil {
			return nil, fmt.Errorf("process response: %w", processErr)
		}
		return nil, err
	}
	if processErr != nil {
		_, where := observability.ErrorReason(processErr, observability.ReasonResponseProcessErr, observability.WhereRuntimeProcessExecution)
		processed.PartialResponse = true
		processed.PartialResponseReason = string(observability.ReasonPartialResponseInterrupted)
		processed.PartialResponseWhere = string(where)
		logging.Warn("Using partial devshard inference response after interrupted stream",
			chaintypes.Inferences, "inferenceId", inferenceID, "error", processErr)
	}
	return processed, nil
}

func buildProcessedExecutionResponse(
	req devshardpkg.ExecuteRequest,
	processor *completionapi.ExecutorResponseProcessor,
	isSSE bool,
) (*ProcessedExecutionResponse, error) {
	completionResp, err := processor.GetResponse()
	if err != nil {
		return nil, observability.Classify(observability.ReasonResponseProcessErr, observability.WhereRuntimeProcessExecution, fmt.Errorf("get completion response: %w", err))
	}

	bodyBytes, err := completionResp.GetBodyBytes()
	if err != nil {
		return nil, observability.Classify(observability.ReasonResponseProcessErr, observability.WhereRuntimeProcessExecution, fmt.Errorf("get body bytes: %w", err))
	}

	if req.ResponseWriter != nil && !isSSE {
		if _, err := fmt.Fprintf(req.ResponseWriter, "data: %s\n\ndata: [DONE]\n\n", bodyBytes); err != nil {
			return nil, observability.Classify(observability.ReasonResponseWriteErr, observability.WhereRuntimeWriteClientResponse, err)
		}
		if f, ok := req.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
	}

	hash := sha256.Sum256(bodyBytes)
	usage, err := completionResp.GetUsage()
	if err != nil {
		return nil, observability.Classify(observability.ReasonUsageParseErr, observability.WhereRuntimeProcessExecution, fmt.Errorf("get usage: %w", err))
	}

	return &ProcessedExecutionResponse{
		ResponseHash: hash[:],
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		ResponseBody: bodyBytes,
	}, nil
}

func BuildValidationBody(
	promptPayload []byte,
	responsePayload []byte,
	inferenceID uint64,
	chainParams ChainParamsProvider,
) ([]byte, error) {
	seed := int32(inferenceID)
	modified, err := completionapi.ModifyRequestBodyWithLogprobsMode(promptPayload, seed, chainParams.LogprobsMode())
	if err != nil {
		return nil, observability.Classify(observability.ReasonValidationBodyErr, observability.WhereRuntimeModifyRequest, fmt.Errorf("modify request body for validation: %w", err))
	}

	var requestMap map[string]interface{}
	if err := json.Unmarshal(modified.NewBody, &requestMap); err != nil {
		return nil, observability.Classify(observability.ReasonValidationBodyErr, observability.WhereRuntimeModifyRequest, fmt.Errorf("unmarshal modified prompt: %w", err))
	}

	originalResponse, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(responsePayload)
	if err != nil {
		return nil, observability.Classify(observability.ReasonOriginalResponseParseErr, observability.WhereRuntimeProcessExecution, fmt.Errorf("parse original response: %w", err))
	}

	enforcedTokens, err := originalResponse.GetEnforcedTokens()
	if err != nil {
		return nil, observability.Classify(observability.ReasonEnforcedTokensErr, observability.WhereRuntimeProcessExecution, fmt.Errorf("get enforced tokens: %w", err))
	}

	requestMap["enforced_tokens"] = enforcedTokens
	requestMap["stream"] = false
	delete(requestMap, "stream_options")

	validationBody, err := json.Marshal(requestMap)
	if err != nil {
		return nil, observability.Classify(observability.ReasonValidationBodyErr, observability.WhereRuntimeModifyRequest, fmt.Errorf("marshal validation body: %w", err))
	}
	return validationBody, nil
}

func EvaluateValidationResponse(
	ctx context.Context,
	resp *http.Response,
	req devshardpkg.ValidateRequest,
	inferenceID string,
	originalResponsePayload []byte,
	thresholds *ValidationThresholdResolver,
) (*devshardpkg.ValidateResult, error) {
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
		observability.IncValidation(observability.StageValidationFinished, observability.MetricStatusOK)
		return &devshardpkg.ValidateResult{
			Valid:   true,
			Reason:  "rejected_payload",
			Details: []any{"mlnode_status", resp.StatusCode},
		}, nil
	}
	if resp.StatusCode >= 500 {
		return nil, observability.Classify(observability.ReasonHTTP5xx, observability.WhereEngineMLNodeCall, fmt.Errorf("validation mlnode status %d", resp.StatusCode))
	}
	if resp.StatusCode >= 400 {
		return nil, observability.Classify(observability.ReasonHTTP4xx, observability.WhereEngineMLNodeCall, fmt.Errorf("validation mlnode status %d", resp.StatusCode))
	}

	respBytes, err := ReadHTTPBody(resp)
	if err != nil {
		return nil, observability.Classify(observability.ReasonValidationResponseErr, observability.WhereRuntimeProcessExecution, fmt.Errorf("read validation response: %w", err))
	}

	validationResponse, err := completionapi.NewCompletionResponseFromBytes(respBytes)
	if err != nil {
		return nil, observability.Classify(observability.ReasonValidationResponseErr, observability.WhereRuntimeProcessExecution, fmt.Errorf("parse validation response: %w", err))
	}

	originalResponse, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(originalResponsePayload)
	if err != nil {
		return nil, observability.Classify(observability.ReasonOriginalResponseParseErr, observability.WhereRuntimeProcessExecution, fmt.Errorf("parse original response: %w", err))
	}

	if validationUsage, err := validationResponse.GetUsage(); err == nil {
		if tokenCountInflated(req.InputTokens, validationUsage.PromptTokens) ||
			tokenCountInflated(req.OutputTokens, validationUsage.CompletionTokens) {
			return &devshardpkg.ValidateResult{
				Valid:  false,
				Reason: "inflated_tokens",
				Details: []any{
					"claimed_input", req.InputTokens,
					"validation_input", validationUsage.PromptTokens,
					"claimed_output", req.OutputTokens,
					"validation_output", validationUsage.CompletionTokens,
				},
			}, nil
		}
	}

	originalLogits := originalResponse.ExtractLogits()
	validationLogits := validationResponse.ExtractLogits()
	base := validationpkg.BaseValidationResult{
		InferenceId:   inferenceID,
		ResponseBytes: respBytes,
	}
	result := validationpkg.CompareLogits(originalLogits, validationLogits, base)
	out, err := EvaluateValidationResult(ctx, result, req, thresholds)
	if err != nil {
		return nil, err
	}
	appendLogitMismatchDetails(out, result, originalLogits, validationLogits)
	return out, nil
}

func tokenCountInflated(claimed, validation uint64) bool {
	const tokenCountTolerance uint64 = 3
	return claimed > validation && claimed-validation > tokenCountTolerance
}

func EvaluateValidationResult(
	ctx context.Context,
	result validationpkg.ValidationResult,
	req devshardpkg.ValidateRequest,
	thresholds *ValidationThresholdResolver,
) (*devshardpkg.ValidateResult, error) {
	switch r := result.(type) {
	case *validationpkg.SimilarityValidationResult:
		threshold, err := thresholds.Resolve(ctx, req.EscrowID, req.EpochID, req.Model)
		if err != nil {
			return nil, err
		}
		passValue := chaintypes.Decimal{Value: threshold.Value, Exponent: threshold.Exponent}
		valid := chaintypes.DecimalFromFloat(r.Value).ToDecimal().GreaterThan(passValue.ToDecimal())
		reason := "similarity_pass"
		if !valid {
			reason = "similarity_below"
		}
		return &devshardpkg.ValidateResult{
			Valid:   valid,
			Reason:  reason,
			Details: []any{"similarity", r.Value, "threshold", passValue.ToFloat()},
		}, nil
	case *validationpkg.DifferentLengthValidationResult:
		return &devshardpkg.ValidateResult{Valid: false, Reason: "different_length"}, nil
	case *validationpkg.DifferentTokensValidationResult:
		return &devshardpkg.ValidateResult{Valid: false, Reason: "different_tokens"}, nil
	case *validationpkg.InvalidInferenceResult:
		details := []any{"detail", r.Reason}
		if r.Error != nil {
			details = append(details, "error", r.Error.Error())
		}
		return &devshardpkg.ValidateResult{Valid: false, Reason: "invalid_inference", Details: details}, nil
	default:
		return nil, fmt.Errorf("unknown validation result type %T", result)
	}
}

func appendLogitMismatchDetails(
	out *devshardpkg.ValidateResult,
	result validationpkg.ValidationResult,
	original, validation []completionapi.Logprob,
) {
	switch result.(type) {
	case *validationpkg.DifferentLengthValidationResult:
		out.Details = append(out.Details,
			"original_logits_len", len(original),
			"validation_logits_len", len(validation))
	case *validationpkg.DifferentTokensValidationResult:
		idx, origTok, valTok := firstTokenMismatch(original, validation)
		out.Details = append(out.Details,
			"first_mismatch_index", idx,
			"original_token", origTok,
			"validation_token", valTok)
	}
}

func firstTokenMismatch(original, validation []completionapi.Logprob) (int, string, string) {
	n := len(original)
	if len(validation) < n {
		n = len(validation)
	}
	for i := 0; i < n; i++ {
		if original[i].Token != validation[i].Token {
			return i, original[i].Token, validation[i].Token
		}
	}
	return -1, "", ""
}

func ReadHTTPBody(resp *http.Response) ([]byte, error) {
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(resp.Body)
	return buf.Bytes(), err
}

func ResolveExecutorPubKeys(ctx context.Context, recorder PayloadAuthClient, executorAddress string) ([]string, error) {
	qc := recorder.NewInferenceQueryClient()

	grantees, err := qc.GranteesByMessageType(ctx, &chaintypes.QueryGranteesByMessageTypeRequest{
		GranterAddress: executorAddress,
		MessageTypeUrl: "/inference.inference.MsgStartInference",
	})
	if err != nil {
		return nil, fmt.Errorf("query executor grantees: %w", err)
	}
	pubkeys := make([]string, 0, len(grantees.Grantees)+1)
	for _, g := range grantees.Grantees {
		pubkeys = append(pubkeys, g.PubKey)
	}

	participant, err := qc.AccountByAddress(ctx, &chaintypes.QueryAccountByAddressRequest{
		Address: executorAddress,
	})
	if err != nil {
		return nil, fmt.Errorf("query executor participant: %w", err)
	}
	if participant.Pubkey != "" {
		pubkeys = append(pubkeys, participant.Pubkey)
	}
	return pubkeys, nil
}

func SignPayloadRequest(
	recorder PayloadAuthClient,
	inferenceID string,
	timestamp int64,
	validatorAddress string,
	epochID uint64,
) (string, error) {
	components := calculations.SignatureComponents{
		Payload:         inferenceID,
		EpochId:         epochID,
		Timestamp:       timestamp,
		TransferAddress: validatorAddress,
		ExecutorAddress: "",
	}

	signerAddressStr := recorder.GetSignerAddress()
	signerAddress, err := sdk.AccAddressFromBech32(signerAddressStr)
	if err != nil {
		return "", err
	}
	accountSigner := &cmd.AccountSigner{
		Addr:    signerAddress,
		Keyring: recorder.GetKeyring(),
	}
	return calculations.Sign(accountSigner, components, calculations.Developer)
}

func FetchPayloadsFromExecutor(
	ctx context.Context,
	httpClient *http.Client,
	br bridge.MainnetBridge,
	recorder PayloadAuthClient,
	req devshardpkg.ValidateRequest,
	inferenceID string,
	epochID uint64,
	requestPath string,
) ([]byte, []byte, error) {
	executorInfo, err := br.GetHostInfo(req.ExecutorAddress)
	if err != nil {
		return nil, nil, fmt.Errorf("get executor info: %w", err)
	}
	if executorInfo.URL == "" {
		return nil, nil, fmt.Errorf("executor has no URL")
	}

	requestURL, err := validationpkg.BuildPayloadRequestURL(executorInfo.URL, requestPath, inferenceID)
	if err != nil {
		return nil, nil, err
	}

	timestamp := time.Now().UnixNano()
	validatorAddress := recorder.GetAccountAddress()
	signature, err := SignPayloadRequest(recorder, inferenceID, timestamp, validatorAddress, epochID)
	if err != nil {
		return nil, nil, fmt.Errorf("sign request: %w", err)
	}

	payloadResp, err := fetchPayloadsHTTPWithTimeout(
		ctx,
		httpClient,
		PayloadFetchTimeout,
		requestURL,
		validatorAddress,
		timestamp,
		epochID,
		signature,
	)
	if err != nil {
		return nil, nil, err
	}

	encodedPubKeys, err := ResolveExecutorPubKeys(ctx, recorder, req.ExecutorAddress)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve executor pubkeys: %w", err)
	}

	if err := validationpkg.VerifyExecutorPayloadSignature(
		inferenceID,
		payloadResp.PromptPayload,
		payloadResp.ResponsePayload,
		payloadResp.ExecutorSignature,
		req.ExecutorAddress,
		encodedPubKeys,
	); err != nil {
		return nil, nil, fmt.Errorf("verify executor signature: %w", err)
	}

	promptHash := sha256.Sum256(payloadResp.PromptPayload)
	if !bytes.Equal(promptHash[:], req.PromptHash) {
		return nil, nil, fmt.Errorf("prompt hash mismatch: expected %x, got %x", req.PromptHash, promptHash[:])
	}

	responseHash := sha256.Sum256(payloadResp.ResponsePayload)
	if !bytes.Equal(responseHash[:], req.ResponseHash) {
		return nil, nil, fmt.Errorf("response hash mismatch: expected %x, got %x", req.ResponseHash, responseHash[:])
	}

	return payloadResp.PromptPayload, payloadResp.ResponsePayload, nil
}

func fetchPayloadsHTTPWithTimeout(
	ctx context.Context,
	httpClient *http.Client,
	timeout time.Duration,
	requestURL string,
	validatorAddress string,
	timestamp int64,
	epochID uint64,
	signature string,
) (*validationpkg.PayloadResponse, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return validationpkg.FetchPayloadsHTTP(
		fetchCtx,
		httpClient,
		requestURL,
		validatorAddress,
		timestamp,
		epochID,
		signature,
	)
}
