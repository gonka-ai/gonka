package inference

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"
	"time"

	commonvalidation "common/validation"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/cmd/inferenced/cmd"
	"github.com/productscience/inference/x/inference/calculations"
	chaintypes "github.com/productscience/inference/x/inference/types"

	devshardpkg "devshard"
	"devshard/bridge"
)

func validateInference(
	ctx context.Context,
	req devshardpkg.ValidateRequest,
	br bridge.MainnetBridge,
	recorder PayloadAuthClient,
	payloadEpoch uint64,
	requestPath string,
	execute mlRequestExecutor,
) (*devshardpkg.ValidateResult, error) {
	inferenceID := strconv.FormatUint(req.InferenceID, 10)

	promptPayload, responsePayload, err := fetchPayloadsFromExecutor(
		ctx, br, recorder, req, inferenceID, payloadEpoch, requestPath,
	)
	if err != nil {
		return nil, fmt.Errorf("fetch payloads from executor: %w", err)
	}

	result, err := commonvalidation.ExecuteValidation(
		ctx,
		inferenceID,
		promptPayload,
		responsePayload,
		func(ctx context.Context, body []byte) (*http.Response, error) {
			return execute(ctx, req.Model, body)
		},
		req.InputTokens, req.OutputTokens,
	)
	if err != nil {
		return nil, err
	}
	return &devshardpkg.ValidateResult{Valid: result.IsSuccessful()}, nil
}

func signPayloadRequest(
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

	signerAddress, err := sdk.AccAddressFromBech32(recorder.GetSignerAddress())
	if err != nil {
		return "", err
	}
	accountSigner := &cmd.AccountSigner{
		Addr:    signerAddress,
		Keyring: recorder.GetKeyring(),
	}
	return calculations.Sign(accountSigner, components, calculations.Developer)
}

func resolveExecutorPubKeys(ctx context.Context, recorder PayloadAuthClient, executorAddress string) ([]string, error) {
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

func fetchPayloadsFromExecutor(
	ctx context.Context,
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

	requestURL, err := commonvalidation.BuildPayloadRequestURL(executorInfo.URL, requestPath, inferenceID)
	if err != nil {
		return nil, nil, err
	}

	timestamp := time.Now().UnixNano()
	validatorAddress := recorder.GetAccountAddress()
	signature, err := signPayloadRequest(recorder, inferenceID, timestamp, validatorAddress, epochID)
	if err != nil {
		return nil, nil, fmt.Errorf("sign request: %w", err)
	}

	payloadResp, err := commonvalidation.FetchPayloadsHTTP(
		ctx, requestURL, validatorAddress, timestamp, epochID, signature,
	)
	if err != nil {
		return nil, nil, err
	}

	encodedPubKeys, err := resolveExecutorPubKeys(ctx, recorder, req.ExecutorAddress)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve executor pubkeys: %w", err)
	}

	if err := commonvalidation.VerifyExecutorPayloadSignature(
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
