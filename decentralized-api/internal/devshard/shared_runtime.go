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
	devshardserver "devshard/server"
)

type MLRequestExecutor func(ctx context.Context, model string, body []byte) (*http.Response, error)

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
		return nil, fmt.Errorf("modify request body: %w", err)
	}

	resp, err := execute(ctx, req.Model, modified.NewBody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	processed, err := ProcessExecutionHTTPResponse(req, resp, inferenceID)
	if err != nil {
		return nil, err
	}

	// Store the canonicalized ORIGINAL prompt (not the modified one with seed).
	promptPayload, err := devshardpkg.CanonicalizeJSON(req.Prompt)
	if err != nil {
		return nil, fmt.Errorf("canonicalize prompt: %w", err)
	}

	if err := payloadStore.Store(
		ctx,
		devshardserver.PayloadKey(req.EscrowID, req.InferenceID),
		payloadEpoch,
		promptPayload,
		processed.ResponseBody,
	); err != nil {
		return nil, fmt.Errorf("store payloads: %w", err)
	}

	return &devshardpkg.ExecuteResult{
		ResponseHash: processed.ResponseHash,
		InputTokens:  processed.InputTokens,
		OutputTokens: processed.OutputTokens,
		ResponseBody: processed.ResponseBody,
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
	logPrefix string,
	chainParams ChainParamsProvider,
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
		return nil, fmt.Errorf("fetch payloads from executor: %w", err)
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

	return EvaluateValidationResponse(resp, req, inferenceID, logPrefix, responsePayload)
}

type ProcessedExecutionResponse struct {
	ResponseHash []byte
	InputTokens  uint64
	OutputTokens uint64
	ResponseBody []byte
}

func ProcessExecutionHTTPResponse(
	req devshardpkg.ExecuteRequest,
	resp *http.Response,
	inferenceID string,
) (*ProcessedExecutionResponse, error) {
	processor := completionapi.NewExecutorResponseProcessor(inferenceID)

	contentType := resp.Header.Get("Content-Type")
	isSSE := strings.HasPrefix(contentType, "text/event-stream")

	if req.ResponseWriter != nil && isSSE {
		public.ProxyResponse(resp, req.ResponseWriter, true, processor, inferenceID)
	} else {
		if err := completionapi.ProcessHTTPResponse(resp, processor); err != nil {
			return nil, fmt.Errorf("process response: %w", err)
		}
	}

	completionResp, err := processor.GetResponse()
	if err != nil {
		return nil, fmt.Errorf("get completion response: %w", err)
	}

	bodyBytes, err := completionResp.GetBodyBytes()
	if err != nil {
		return nil, fmt.Errorf("get body bytes: %w", err)
	}

	if req.ResponseWriter != nil && !isSSE {
		fmt.Fprintf(req.ResponseWriter, "data: %s\n\ndata: [DONE]\n\n", bodyBytes)
		if f, ok := req.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
	}

	hash := sha256.Sum256(bodyBytes)
	usage, err := completionResp.GetUsage()
	if err != nil {
		return nil, fmt.Errorf("get usage: %w", err)
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
		return nil, fmt.Errorf("modify request body for validation: %w", err)
	}

	var requestMap map[string]interface{}
	if err := json.Unmarshal(modified.NewBody, &requestMap); err != nil {
		return nil, fmt.Errorf("unmarshal modified prompt: %w", err)
	}

	originalResponse, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(responsePayload)
	if err != nil {
		return nil, fmt.Errorf("parse original response: %w", err)
	}

	enforcedTokens, err := originalResponse.GetEnforcedTokens()
	if err != nil {
		return nil, fmt.Errorf("get enforced tokens: %w", err)
	}

	requestMap["enforced_tokens"] = enforcedTokens
	requestMap["stream"] = false
	delete(requestMap, "stream_options")

	validationBody, err := json.Marshal(requestMap)
	if err != nil {
		return nil, fmt.Errorf("marshal validation body: %w", err)
	}
	return validationBody, nil
}

func EvaluateValidationResponse(
	resp *http.Response,
	req devshardpkg.ValidateRequest,
	inferenceID string,
	logPrefix string,
	originalResponsePayload []byte,
) (*devshardpkg.ValidateResult, error) {
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
		return &devshardpkg.ValidateResult{Valid: true}, nil
	}

	respBytes, err := ReadHTTPBody(resp)
	if err != nil {
		return nil, fmt.Errorf("read validation response: %w", err)
	}

	validationResponse, err := completionapi.NewCompletionResponseFromBytes(respBytes)
	if err != nil {
		return nil, fmt.Errorf("parse validation response: %w", err)
	}

	originalResponse, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(originalResponsePayload)
	if err != nil {
		return nil, fmt.Errorf("parse original response: %w", err)
	}

	if validationUsage, err := validationResponse.GetUsage(); err == nil {
		if req.InputTokens > validationUsage.PromptTokens || req.OutputTokens > validationUsage.CompletionTokens {
			logging.Warn(logPrefix+" validation failed: inflated token counts",
				chaintypes.Validation, "inferenceId", inferenceID,
				"claimedInput", req.InputTokens, "validationInput", validationUsage.PromptTokens,
				"claimedOutput", req.OutputTokens, "validationOutput", validationUsage.CompletionTokens)
			return &devshardpkg.ValidateResult{Valid: false}, nil
		}
	}

	base := validationpkg.BaseValidationResult{
		InferenceId:   inferenceID,
		ResponseBytes: respBytes,
	}
	result := validationpkg.CompareLogits(
		originalResponse.ExtractLogits(),
		validationResponse.ExtractLogits(),
		base,
	)
	return &devshardpkg.ValidateResult{Valid: result.IsSuccessful()}, nil
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

	payloadResp, err := validationpkg.FetchPayloadsHTTP(
		ctx,
		httpClient,
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
