package validation

import (
	"context"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/payloadstorage"
	apiutils "decentralized-api/utils"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/cmd/inferenced/cmd"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

// ErrHashMismatch indicates executor served payload with valid signature but hash doesn't match on-chain commitment.
// This should trigger immediate invalidation (no retry).
var ErrHashMismatch = errors.New("hash mismatch: executor served wrong payload with valid signature")

// ErrEpochStale indicates inference epoch is too old (currentEpoch >= inferenceEpoch + 2).
// Validation is no longer useful - abort without invalidation.
var ErrEpochStale = errors.New("inference epoch too old, validation no longer useful")

// HTTP client with timeout for payload retrieval
var payloadRetrievalClient = &http.Client{}

// PayloadResponse matches the executor endpoint response
type PayloadResponse struct {
	InferenceId       string `json:"inference_id"`
	PromptPayload     []byte `json:"prompt_payload"`
	ResponsePayload   []byte `json:"response_payload"`
	ExecutorSignature string `json:"executor_signature"`
}

// RetrievePayloadsFromExecutor makes a single REST call to executor.
// Returns payloads or error. No retry logic - handled by caller.
func RetrievePayloadsFromExecutor(
	ctx context.Context,
	inferenceId string,
	executorAddress string,
	epochId uint64,
	recorder cosmosclient.CosmosMessageClient,
) (promptPayload, responsePayload []byte, err error) {
	queryClient := recorder.NewInferenceQueryClient()
	participantResp, err := queryClient.Participant(ctx, &types.QueryGetParticipantRequest{
		Index: executorAddress,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get executor participant: %w", err)
	}

	executorUrl := participantResp.Participant.InferenceUrl
	if executorUrl == "" {
		return nil, nil, fmt.Errorf("executor has no inference URL")
	}

	// Build URL with inference_id as query parameter
	baseUrl, err := url.JoinPath(executorUrl, "v1/inference/payloads")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build base URL: %w", err)
	}
	parsedUrl, err := url.Parse(baseUrl)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse base URL: %w", err)
	}
	query := parsedUrl.Query()
	query.Set("inference_id", inferenceId)
	parsedUrl.RawQuery = query.Encode()
	requestUrl := parsedUrl.String()

	timestamp := time.Now().UnixNano()
	validatorAddress := recorder.GetAccountAddress()

	signature, err := signPayloadRequest(inferenceId, timestamp, validatorAddress, epochId, recorder)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestUrl, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set(apiutils.XValidatorAddressHeader, validatorAddress)
	req.Header.Set(apiutils.XTimestampHeader, strconv.FormatInt(timestamp, 10))
	req.Header.Set(apiutils.XEpochIdHeader, strconv.FormatUint(epochId, 10))
	req.Header.Set(apiutils.AuthorizationHeader, signature)

	resp, err := payloadRetrievalClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil, fmt.Errorf("payload not found on executor")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("executor returned status %d: %s", resp.StatusCode, string(body))
	}

	var payloadResp PayloadResponse
	if err := json.NewDecoder(resp.Body).Decode(&payloadResp); err != nil {
		return nil, nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Verify executor signature - invalid signature triggers retry (could be network issue)
	grantees, err := queryClient.GranteesByMessageType(ctx, &types.QueryGranteesByMessageTypeRequest{
		GranterAddress: executorAddress,
		MessageTypeUrl: "/inference.inference.MsgStartInference",
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get executor grantees: %w", err)
	}
	executorPubkeys := make([]string, 0, len(grantees.Grantees)+1)
	for _, g := range grantees.Grantees {
		executorPubkeys = append(executorPubkeys, g.PubKey)
	}
	// Get executor's own pubkey
	executorParticipant, err := queryClient.InferenceParticipant(ctx, &types.QueryInferenceParticipantRequest{
		Address: executorAddress,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get executor pubkey: %w", err)
	}
	executorPubkeys = append(executorPubkeys, executorParticipant.Pubkey)

	if err := verifyExecutorPayloadSignature(
		inferenceId,
		payloadResp.PromptPayload,
		payloadResp.ResponsePayload,
		payloadResp.ExecutorSignature,
		executorAddress,
		executorPubkeys,
	); err != nil {
		// Signature invalid - could be network issue, corrupted data, etc.
		// Return error to trigger retry
		return nil, nil, fmt.Errorf("executor signature verification failed: %w", err)
	}

	logging.Debug("Executor signature verified successfully", types.Validation,
		"inferenceId", inferenceId, "executorAddress", executorAddress)

	// Verify hashes match on-chain commitment - hash mismatch = immediate invalidation (no retry)
	inference, err := queryClient.Inference(ctx, &types.QueryGetInferenceRequest{Index: inferenceId})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get inference from chain: %w", err)
	}

	// Check prompt hash
	if inference.Inference.PromptHash != "" {
		actualPromptHash, err := payloadstorage.ComputePromptHash(payloadResp.PromptPayload)
		if err != nil {
			// Hash computation failed - payload is malformed
			// Executor signed malformed data - immediate invalidation
			// TODO: Phase 7 - use executor's signed proof for fast invalidation
			logging.Error("Failed to compute prompt hash, executor served malformed payload", types.Validation,
				"inferenceId", inferenceId, "error", err)
			return nil, nil, ErrHashMismatch
		}
		if actualPromptHash != inference.Inference.PromptHash {
			// Hash mismatch - executor signed wrong payload - immediate invalidation
			// TODO: Phase 7 - use executor's signed proof for fast invalidation
			logging.Error("Prompt hash mismatch, executor served wrong payload", types.Validation,
				"inferenceId", inferenceId,
				"expectedHash", inference.Inference.PromptHash,
				"actualHash", actualPromptHash)
			return nil, nil, ErrHashMismatch
		}
	}

	// Check response hash
	if inference.Inference.ResponseHash != "" {
		actualResponseHash, err := payloadstorage.ComputeResponseHash(payloadResp.ResponsePayload)
		if err != nil {
			// Hash computation failed - payload is malformed
			// Executor signed malformed data - immediate invalidation
			// TODO: Phase 7 - use executor's signed proof for fast invalidation
			logging.Error("Failed to compute response hash, executor served malformed payload", types.Validation,
				"inferenceId", inferenceId, "error", err)
			return nil, nil, ErrHashMismatch
		}
		if actualResponseHash != inference.Inference.ResponseHash {
			// Hash mismatch - executor signed wrong payload - immediate invalidation
			// TODO: Phase 7 - use executor's signed proof for fast invalidation
			logging.Error("Response hash mismatch, executor served wrong payload", types.Validation,
				"inferenceId", inferenceId,
				"expectedHash", inference.Inference.ResponseHash,
				"actualHash", actualResponseHash)
			return nil, nil, ErrHashMismatch
		}
	}

	logging.Debug("Successfully retrieved and verified payloads from executor", types.Validation,
		"inferenceId", inferenceId, "executorAddress", executorAddress)

	return payloadResp.PromptPayload, payloadResp.ResponsePayload, nil
}

// DEPRECATED: retrievePayloadsFromChain queries chain for payload fields.
// Only used for inferences created before offchain payload upgrade.
// Will be removed in Phase 6 when payload fields are eliminated from chain.
func retrievePayloadsFromChain(
	ctx context.Context,
	inferenceId string,
	recorder cosmosclient.CosmosMessageClient,
) (promptPayload, responsePayload []byte, err error) {
	logging.Warn("Using DEPRECATED chain payload retrieval", types.Validation,
		"inferenceId", inferenceId)

	queryClient := recorder.NewInferenceQueryClient()
	response, err := queryClient.Inference(ctx, &types.QueryGetInferenceRequest{Index: inferenceId})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query inference: %w", err)
	}

	// Before off-chain, we simply used the unsafe conversion
	return []byte(response.Inference.PromptPayload), []byte(response.Inference.ResponsePayload), nil
}

// signPayloadRequest signs the payload retrieval request with validator's key
// Validator signs: inferenceId + epochId + timestamp + validatorAddress
// EpochId binding prevents replay attacks within epoch windows
func signPayloadRequest(
	inferenceId string,
	timestamp int64,
	validatorAddress string,
	epochId uint64,
	recorder cosmosclient.CosmosMessageClient,
) (string, error) {
	components := calculations.SignatureComponents{
		Payload:         inferenceId,
		EpochId:         epochId,
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

// verifyExecutorPayloadSignature verifies the executor's signature on the payload response.
// This provides non-repudiation: if executor serves wrong payload, validator has cryptographic proof.
// Executor signs: inferenceId + promptHash + responseHash (with timestamp=0)
func verifyExecutorPayloadSignature(
	inferenceId string,
	promptPayload []byte,
	responsePayload []byte,
	signature string,
	executorAddress string,
	executorPubkeys []string,
) error {
	if signature == "" {
		return fmt.Errorf("executor signature is empty")
	}

	promptHash := apiutils.GenerateSHA256HashBytes(promptPayload)
	responseHash := apiutils.GenerateSHA256HashBytes(responsePayload)
	payload := inferenceId + promptHash + responseHash

	components := calculations.SignatureComponents{
		Payload:         payload,
		Timestamp:       0, // Executor uses timestamp=0 for non-repudiation signatures
		TransferAddress: executorAddress,
		ExecutorAddress: "",
	}

	return calculations.ValidateSignatureWithGrantees(components, calculations.Developer, executorPubkeys, signature)
}
