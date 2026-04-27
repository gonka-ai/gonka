package subnet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/completionapi"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/validation"
	"decentralized-api/logging"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/cmd/inferenced/cmd"
	"github.com/productscience/inference/x/inference/calculations"
	chaintypes "github.com/productscience/inference/x/inference/types"

	"subnet"
	"subnet/bridge"
)

// ValidationAdapter implements subnet.ValidationEngine by re-executing inference
// with enforced tokens and comparing logits.
type ValidationAdapter struct {
	broker       *broker.Broker
	nodeVersion  string
	phaseTracker *chainphase.ChainPhaseTracker
	httpClient   *http.Client
	bridge       bridge.MainnetBridge
	recorder     cosmosclient.CosmosMessageClient
}

func NewValidationAdapter(
	b *broker.Broker,
	nodeVersion string,
	phaseTracker *chainphase.ChainPhaseTracker,
	httpClient *http.Client,
	br bridge.MainnetBridge,
	recorder cosmosclient.CosmosMessageClient,
) *ValidationAdapter {
	return &ValidationAdapter{
		broker:       b,
		nodeVersion:  nodeVersion,
		phaseTracker: phaseTracker,
		httpClient:   httpClient,
		bridge:       br,
		recorder:     recorder,
	}
}

func (v *ValidationAdapter) Validate(ctx context.Context, req subnet.ValidateRequest) (*subnet.ValidateResult, error) {
	inferenceID := strconv.FormatUint(req.InferenceID, 10)
	epochID := req.EpochID
	if epochID == 0 {
		epochID = currentEpochID(v.phaseTracker)
	}

	// Fetch payloads from executor
	promptPayload, responsePayload, err := v.fetchPayloadsFromExecutor(ctx, req, inferenceID, epochID)
	if err != nil {
		return nil, fmt.Errorf("fetch payloads from executor: %w", err)
	}

	// The stored prompt is the original user request (before ModifyRequestBody).
	// Apply the same modifications (logprobs, seed, max_tokens) so the
	// validation re-execution matches the original execution environment.
	seed := int32(req.InferenceID)
	modified, err := completionapi.ModifyRequestBody(promptPayload, seed)
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

	// Validation-specific overrides on top of ModifyRequestBody output.
	requestMap["enforced_tokens"] = enforcedTokens
	requestMap["stream"] = false
	delete(requestMap, "stream_options")

	validationBody, err := json.Marshal(requestMap)
	if err != nil {
		return nil, fmt.Errorf("marshal validation body: %w", err)
	}

	resp, err := broker.DoWithLockedNodeHTTPRetry(v.broker, req.Model, nil, 3,
		func(node *broker.Node) (*http.Response, *broker.ActionError) {
			url := node.InferenceUrlWithVersion(v.nodeVersion) + "/v1/chat/completions"
			httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(validationBody))
			if reqErr != nil {
				return nil, broker.NewApplicationActionError(reqErr)
			}
			httpReq.Header.Set("Content-Type", "application/json")
			httpResp, postErr := v.httpClient.Do(httpReq)
			if postErr != nil {
				return nil, broker.NewTransportActionError(postErr)
			}
			return httpResp, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("broker validate: %w", err)
	}
	defer resp.Body.Close()

	// 400/422 from ML node means enforced tokens not supported; treat as valid
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
		return &subnet.ValidateResult{Valid: true}, nil
	}

	var respBytes []byte
	respBytes, err = readBody(resp)
	if err != nil {
		return nil, fmt.Errorf("read validation response: %w", err)
	}

	validationResponse, err := completionapi.NewCompletionResponseFromBytes(respBytes)
	if err != nil {
		return nil, fmt.Errorf("parse validation response: %w", err)
	}

	if validationUsage, err := validationResponse.GetUsage(); err == nil {
		if req.InputTokens > validationUsage.PromptTokens || req.OutputTokens > validationUsage.CompletionTokens {
			logging.Warn("subnet validation failed: inflated token counts",
				chaintypes.Validation, "inferenceId", inferenceID,
				"claimedInput", req.InputTokens, "validationInput", validationUsage.PromptTokens,
				"claimedOutput", req.OutputTokens, "validationOutput", validationUsage.CompletionTokens)
			return &subnet.ValidateResult{Valid: false}, nil
		}
	}

	originalLogits := originalResponse.ExtractLogits()
	validationLogits := validationResponse.ExtractLogits()

	base := validation.BaseValidationResult{
		InferenceId:   inferenceID,
		ResponseBytes: respBytes,
	}

	result := validation.CompareLogits(originalLogits, validationLogits, base)

	return &subnet.ValidateResult{Valid: result.IsSuccessful()}, nil
}

// fetchPayloadsFromExecutor retrieves payloads from the executor host using subnet session endpoint.
func (v *ValidationAdapter) fetchPayloadsFromExecutor(ctx context.Context, req subnet.ValidateRequest, inferenceID string, epochID uint64) ([]byte, []byte, error) {
	// Resolve executor URL from bridge
	executorInfo, err := v.bridge.GetHostInfo(req.ExecutorAddress)
	if err != nil {
		return nil, nil, fmt.Errorf("get executor info: %w", err)
	}
	if executorInfo.URL == "" {
		return nil, nil, fmt.Errorf("executor has no URL")
	}

	// Build request URL for subnet session endpoint
	requestURL, err := validation.BuildPayloadRequestURL(executorInfo.URL, fmt.Sprintf("v1/subnet/sessions/%s/payloads", req.EscrowID), inferenceID)
	if err != nil {
		return nil, nil, err
	}

	// Sign request
	timestamp := time.Now().UnixNano()
	validatorAddress := v.recorder.GetAccountAddress()
	signature, err := v.signPayloadRequest(inferenceID, timestamp, validatorAddress, epochID)
	if err != nil {
		return nil, nil, fmt.Errorf("sign request: %w", err)
	}

	// Fetch payloads using shared helper
	payloadResp, err := validation.FetchPayloadsHTTP(ctx, v.httpClient, requestURL, validatorAddress, timestamp, epochID, signature)
	if err != nil {
		return nil, nil, err
	}

	// Resolve executor pubkeys from chain (cold key + warm keys)
	encodedPubKeys, err := v.resolveExecutorPubKeys(ctx, req.ExecutorAddress)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve executor pubkeys: %w", err)
	}

	// Verify executor signature
	if err := validation.VerifyExecutorPayloadSignature(
		inferenceID,
		payloadResp.PromptPayload,
		payloadResp.ResponsePayload,
		payloadResp.ExecutorSignature,
		req.ExecutorAddress,
		encodedPubKeys,
	); err != nil {
		return nil, nil, fmt.Errorf("verify executor signature: %w", err)
	}

	// Verify prompt hash: the stored payload is already canonical (from
	// subnet.CanonicalizeJSON), so direct sha256 matches CanonicalPromptHash.
	promptHash := sha256.Sum256(payloadResp.PromptPayload)
	if !bytes.Equal(promptHash[:], req.PromptHash) {
		return nil, nil, fmt.Errorf("prompt hash mismatch: expected %x, got %x", req.PromptHash, promptHash[:])
	}

	// Verify response hash: raw sha256 of stored response bytes.
	responseHash := sha256.Sum256(payloadResp.ResponsePayload)
	if !bytes.Equal(responseHash[:], req.ResponseHash) {
		return nil, nil, fmt.Errorf("response hash mismatch: expected %x, got %x", req.ResponseHash, responseHash[:])
	}

	return payloadResp.PromptPayload, payloadResp.ResponsePayload, nil
}

// resolveExecutorPubKeys queries InferenceParticipant (cold key) and
// GranteesByMessageType (warm keys) to collect all pubkeys that the executor
// might use to sign payloads. Returns base64-encoded pubkey strings.
func (v *ValidationAdapter) resolveExecutorPubKeys(ctx context.Context, executorAddress string) ([]string, error) {
	qc := v.recorder.NewInferenceQueryClient()

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

	participant, err := qc.InferenceParticipant(ctx, &chaintypes.QueryInferenceParticipantRequest{
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

// signPayloadRequest signs the payload retrieval request.
func (v *ValidationAdapter) signPayloadRequest(inferenceID string, timestamp int64, validatorAddress string, epochID uint64) (string, error) {
	components := calculations.SignatureComponents{
		Payload:         inferenceID,
		EpochId:         epochID,
		Timestamp:       timestamp,
		TransferAddress: validatorAddress,
		ExecutorAddress: "",
	}

	signerAddressStr := v.recorder.GetSignerAddress()
	signerAddress, err := sdk.AccAddressFromBech32(signerAddressStr)
	if err != nil {
		return "", err
	}
	accountSigner := &cmd.AccountSigner{
		Addr:    signerAddress,
		Keyring: v.recorder.GetKeyring(),
	}

	return calculations.Sign(accountSigner, components, calculations.Developer)
}

func readBody(resp *http.Response) ([]byte, error) {
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(resp.Body)
	return buf.Bytes(), err
}

// Compile-time check.
var _ subnet.ValidationEngine = (*ValidationAdapter)(nil)
