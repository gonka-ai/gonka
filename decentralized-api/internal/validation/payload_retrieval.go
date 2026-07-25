package validation

import (
	"context"
	"fmt"
	"time"

	"common/logging"
	commonvalidation "common/validation"
	"decentralized-api/cosmosclient"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/cmd/inferenced/cmd"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

// RetrievePayloadsFromExecutor makes a single REST call to executor.
// Returns payloads or error. No retry logic - handled by caller.
// This is the chain validation path that resolves executor info from chain state.
func RetrievePayloadsFromExecutor(
	ctx context.Context,
	inferenceId string,
	executorAddress string,
	epochId uint64,
	recorder cosmosclient.CosmosMessageClient,
) (promptPayload, responsePayload []byte, err error) {
	queryClient := recorder.NewInferenceQueryClient()

	// Resolve executor URL from chain
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

	// Build request URL
	requestUrl, err := commonvalidation.BuildPayloadRequestURL(executorUrl, "v1/inference/payloads", inferenceId)
	if err != nil {
		return nil, nil, err
	}

	// Sign request using chain recorder
	timestamp := time.Now().UnixNano()
	validatorAddress := recorder.GetAccountAddress()
	signature, err := signPayloadRequest(inferenceId, timestamp, validatorAddress, epochId, recorder)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign request: %w", err)
	}

	// Fetch payloads
	payloadResp, err := commonvalidation.FetchPayloadsHTTP(ctx, commonvalidation.PayloadRetrievalClient, requestUrl, validatorAddress, timestamp, epochId, signature)
	if err != nil {
		return nil, nil, err
	}

	// Get executor pubkeys from chain for signature verification
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
	executorParticipant, err := queryClient.AccountByAddress(ctx, &types.QueryAccountByAddressRequest{
		Address: executorAddress,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get executor pubkey: %w", err)
	}
	executorPubkeys = append(executorPubkeys, executorParticipant.Pubkey)

	// Verify executor signature
	if err := commonvalidation.VerifyExecutorPayloadSignature(
		inferenceId,
		payloadResp.PromptPayload,
		payloadResp.ResponsePayload,
		payloadResp.ExecutorSignature,
		executorAddress,
		executorPubkeys,
	); err != nil {
		return nil, nil, fmt.Errorf("executor signature verification failed: %w", err)
	}
	logging.Debug("Executor signature verified successfully", types.Validation,
		"inferenceId", inferenceId, "executorAddress", executorAddress)

	// Get expected hashes from chain
	inference, err := queryClient.Inference(ctx, &types.QueryGetInferenceRequest{Index: inferenceId})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get inference from chain: %w", err)
	}

	// Verify hashes
	if err := commonvalidation.VerifyPayloadHashes(
		payloadResp.PromptPayload,
		payloadResp.ResponsePayload,
		inference.Inference.PromptHash,
		inference.Inference.ResponseHash,
		inferenceId,
	); err != nil {
		return nil, nil, err
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

// signPayloadRequest signs the payload retrieval request with validator's key.
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
