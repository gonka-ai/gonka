package inference

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	commonvalidation "common/validation"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/cmd/inferenced/cmd"
	"github.com/productscience/inference/x/inference/calculations"
	chaintypes "github.com/productscience/inference/x/inference/types"

	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/observability"
)

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
		ctx, nil, requestURL, validatorAddress, timestamp, epochID, signature,
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
		return nil, nil, fmt.Errorf("%w: prompt expected %x got %x", commonvalidation.ErrHashMismatch, req.PromptHash, promptHash[:])
	}

	responseHash := sha256.Sum256(payloadResp.ResponsePayload)
	if !bytes.Equal(responseHash[:], req.ResponseHash) {
		return nil, nil, fmt.Errorf("%w: response expected %x got %x", commonvalidation.ErrHashMismatch, req.ResponseHash, responseHash[:])
	}

	return payloadResp.PromptPayload, payloadResp.ResponsePayload, nil
}

func classifyExecuteValidationErr(err error) error {
	if err == nil {
		return nil
	}
	var classified *observability.ClassifiedError
	if errors.As(err, &classified) {
		return err
	}
	msg := err.Error()
	switch {
	case errors.Is(err, io.EOF) || strings.Contains(msg, "read"):
		return observability.Classify(observability.ReasonValidationReadErr, observability.WhereRuntimeValidate, err)
	case strings.Contains(msg, "unmarshal") || strings.Contains(msg, "parse validation"):
		return observability.Classify(observability.ReasonValidationParseErr, observability.WhereRuntimeValidate, err)
	default:
		return observability.Classify(observability.ReasonValidateErr, observability.WhereRuntimeValidate, err)
	}
}
