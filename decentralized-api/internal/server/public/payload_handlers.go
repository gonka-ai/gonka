package public

import (
	"context"
	"decentralized-api/internal/apitypes"
	"decentralized-api/logging"
	"decentralized-api/payloadstorage"
	"decentralized-api/utils"
	"errors"
	"net/http"
	"strconv"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/cmd/inferenced/cmd"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

// PayloadResponse is an alias for the shared type to avoid import cycles.
type PayloadResponse = apitypes.PayloadResponse

// getInferencePayloads serves payloads to validators for validation
func (s *Server) getInferencePayloads(ctx echo.Context) error {
	validatorAddress := ctx.Request().Header.Get(utils.XValidatorAddressHeader)
	if validatorAddress == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "X-Validator-Address header required")
	}

	return s.getInferencePayloadsImpl(ctx, validatorAddress, s.payloadStorage, "payloadStorage")
}

// getInferencePrompt serves prompts to executors, pinging nodes, and validators for validation
func (s *Server) getInferencePrompt(ctx echo.Context) error {
	requesterAddress := ctx.Request().Header.Get(utils.XRequesterAddressHeader)
	if requesterAddress == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "X-Requester-Address header required")
	}

	// Test mode: block direct retrieval for VoterRecoverySeed inferences when requester is the executor.
	// Voters (non-executor addresses) can still retrieve the payload.
	inferenceId := ctx.QueryParam("inference_id")
	if s.configManager.GetApiConfig().TestMode && inferenceId != "" && IsVoterTestBlocked(inferenceId) {
		queryClient := s.recorder.NewInferenceQueryClient()
		inferenceResp, err := queryClient.Inference(ctx.Request().Context(), &types.QueryGetInferenceRequest{Index: inferenceId})
		if err == nil && inferenceResp.Inference.AssignedTo == requesterAddress {
			logging.Debug("VoterRecoverySeed: blocking direct retrieval for executor", types.Voting,
				"inferenceId", inferenceId, "executorAddress", requesterAddress)
			return echo.NewHTTPError(http.StatusServiceUnavailable, "simulated failure for voter recovery test")
		}
	}

	return s.getInferencePayloadsImpl(ctx, requesterAddress, s.promptStorage, "promptStorage")
}

func (s *Server) getInferencePayloadsImpl(
	ctx echo.Context,
	requesterAddress string,
	storage payloadstorage.PayloadStorage,
	storageName string,
) error {
	inferenceId := ctx.QueryParam("inference_id")
	timestampStr := ctx.Request().Header.Get(utils.XTimestampHeader)
	epochIdStr := ctx.Request().Header.Get(utils.XEpochIdHeader)
	signature := ctx.Request().Header.Get(utils.AuthorizationHeader)

	if inferenceId == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "inference_id required")
	}

	if timestampStr == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "X-Timestamp header required")
	}
	if epochIdStr == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "X-Epoch-Id header required")
	}
	if signature == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "Authorization header required")
	}

	timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid timestamp format")
	}

	epochId, err := strconv.ParseUint(epochIdStr, 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid epoch_id format")
	}

	if err := s.validatePayloadRequestTimestamp(timestamp); err != nil {
		logging.Warn("Payload request timestamp validation failed", types.Validation,
			"inferenceId", inferenceId, "requesterAddress", requesterAddress, "error", err)
		return err
	}

	// First, query the inference to get its actual epoch ID for authorization
	queryClient := s.recorder.NewInferenceQueryClient()
	inferenceResp, err := queryClient.Inference(ctx.Request().Context(), &types.QueryGetInferenceRequest{Index: inferenceId})
	if err != nil {
		logging.Error("Failed to query inference for epochId verification", types.Validation,
			"inferenceId", inferenceId, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to verify inference")
	}

	// Security: Use the inference's actual epoch ID for authorization, not the header value
	// This prevents authorization bypass where attacker provides epoch where they are active
	// but inference belongs to a different epoch
	inferenceEpochId := inferenceResp.Inference.EpochId
	if inferenceEpochId != epochId {
		logging.Warn("EpochId mismatch: header epochId doesn't match inference's epoch", types.Validation,
			"inferenceId", inferenceId,
			"headerEpochId", epochId,
			"inferenceEpochId", inferenceEpochId)
		// Use inference's epoch for authorization check
		epochId = inferenceEpochId
	}

	// Verify requester is active participant at the INFERENCE's epoch (not header epoch)
	if err := s.verifyActiveParticipant(ctx, requesterAddress, epochId); err != nil {
		logging.Warn("Requester not active at inference epoch", types.Validation,
			"requesterAddress", requesterAddress, "epochId", epochId, "error", err)
		return err
	}

	// Get requester's pubkeys (including grantees/warm keys) for signature verification
	requesterPubkeys, err := s.getAllowedPubKeys(ctx, requesterAddress)
	if err != nil {
		logging.Error("Failed to get requester pubkeys", types.Validation,
			"requesterAddress", requesterAddress, "error", err)
		return echo.NewHTTPError(http.StatusUnauthorized, "requester not found")
	}

	// Verify signature (requester signs: inferenceId + timestamp + requesterAddress + epochId)
	if err := validatePayloadRequestSignature(inferenceId, timestamp, requesterAddress, epochId, requesterPubkeys, signature); err != nil {
		logging.Warn("Invalid payload request signature", types.Validation,
			"inferenceId", inferenceId, "requesterAddress", requesterAddress, "error", err)
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid signature")
	}

	promptPayload, responsePayload, actualEpochId, err := s.retrievePayloadsWithAdjacentEpochs(
		ctx.Request().Context(),
		inferenceId,
		epochId,
		storage,
	)
	if err != nil {
		if errors.Is(err, payloadstorage.ErrNotFound) {
			logging.Info("Payload not found in storage (checked adjacent epochs)", types.Validation,
				"inferenceId", inferenceId, "epochId", epochId, "storageName", storageName)
			return echo.NewHTTPError(http.StatusNotFound, "payload not found")
		}
		logging.Error("Failed to retrieve payloads from storage", types.Validation,
			"inferenceId", inferenceId, "storageName", storageName, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to retrieve payloads")
	}
	if actualEpochId != epochId {
		logging.Warn("Payload found in adjacent epoch (epoch boundary race)", types.Validation,
			"inferenceId", inferenceId, "requestedEpochId", epochId, "actualEpochId", actualEpochId)
	}

	// Sign response with executor's warm key
	executorSignature, err := s.signPayloadResponse(inferenceId, promptPayload, responsePayload)
	if err != nil {
		logging.Error("Failed to sign payload response", types.Validation,
			"inferenceId", inferenceId, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to sign response")
	}

	logging.Info("Serving payloads to requester", types.Validation,
		"inferenceId", inferenceId, "requesterAddress", requesterAddress, "epochId", epochId)

	return ctx.JSON(http.StatusOK, PayloadResponse{
		InferenceId:       inferenceId,
		PromptPayload:     promptPayload,
		ResponsePayload:   responsePayload,
		ExecutorSignature: executorSignature,
	})
}

// validatePayloadRequestTimestamp checks if request timestamp is within acceptable window
func (s *Server) validatePayloadRequestTimestamp(timestamp int64) error {
	now := time.Now().UnixNano()
	requestAge := now - timestamp

	// Reject requests older than 60 seconds
	maxAge := int64(60 * time.Second)
	if requestAge > maxAge {
		return echo.NewHTTPError(http.StatusBadRequest, "request timestamp too old")
	}

	// Reject requests more than 10 seconds in the future
	maxFuture := int64(10 * time.Second)
	if requestAge < -maxFuture {
		return echo.NewHTTPError(http.StatusBadRequest, "request timestamp in the future")
	}

	return nil
}

// verifyActiveParticipant checks if address is active participant at given epoch.
// Uses cached EpochGroupData.ValidationWeights for efficiency.
func (s *Server) verifyActiveParticipant(ctx echo.Context, address string, epochId uint64) error {
	isActive, err := s.epochGroupDataCache.IsActiveParticipant(ctx.Request().Context(), epochId, address)
	if err != nil {
		logging.Error("Failed to check active participant", types.Validation,
			"address", address, "epochId", epochId, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to verify participant")
	}

	if !isActive {
		return echo.NewHTTPError(http.StatusUnauthorized, "not an active participant")
	}
	return nil
}

// validatePayloadRequestSignature verifies requester's signature on the request.
// Requester signs: inferenceId + epochId + timestamp + requesterAddress
// EpochId binding prevents replay attacks within epoch windows
func validatePayloadRequestSignature(inferenceId string, timestamp int64, requesterAddress string, epochId uint64, pubkeys []string, signature string) error {
	components := calculations.SignatureComponents{
		Payload:         inferenceId,
		EpochId:         epochId,
		Timestamp:       timestamp,
		TransferAddress: requesterAddress,
		ExecutorAddress: "",
	}
	return calculations.ValidateSignatureWithGrantees(components, calculations.Developer, pubkeys, signature)
}

// retrievePayloadsWithAdjacentEpochs tries to retrieve payloads from storage,
// checking adjacent epochs if not found under the primary epochId.
// This handles the rare epoch boundary race condition where storage uses
// phaseTracker's epoch but retrieval uses inference's EpochId from chain.
// Returns the payloads and the actual epochId where they were found.
func (s *Server) retrievePayloadsWithAdjacentEpochs(
	ctx context.Context,
	inferenceId string,
	epochId uint64,
	storage payloadstorage.PayloadStorage,
) ([]byte, []byte, uint64, error) {
	// Try primary epochId first
	prompt, response, err := storage.Retrieve(ctx, inferenceId, epochId)
	if err == nil {
		return prompt, response, epochId, nil
	}
	if !errors.Is(err, payloadstorage.ErrNotFound) {
		return nil, nil, 0, err
	}

	// Try adjacent epochs (epoch boundary race condition)
	adjacentEpochs := []uint64{}
	if epochId > 0 {
		adjacentEpochs = append(adjacentEpochs, epochId-1)
	}
	adjacentEpochs = append(adjacentEpochs, epochId+1)

	for _, adjEpoch := range adjacentEpochs {
		prompt, response, err := storage.Retrieve(ctx, inferenceId, adjEpoch)
		if err == nil {
			return prompt, response, adjEpoch, nil
		}
		if err != payloadstorage.ErrNotFound {
			return nil, nil, 0, err
		}
	}

	return nil, nil, 0, payloadstorage.ErrNotFound
}

// signPayloadResponse signs the payload response with executor's key
// Uses timestamp=0 since the signature is for non-repudiation, not replay protection
// (replay protection is handled at request level with requester's timestamp)
func (s *Server) signPayloadResponse(inferenceId string, promptPayload, responsePayload []byte) (string, error) {
	// Sign inferenceId + prompt hash + response hash
	promptHash := utils.GenerateSHA256HashBytes(promptPayload)
	responseHash := utils.GenerateSHA256HashBytes(responsePayload)
	payload := inferenceId + promptHash + responseHash

	components := calculations.SignatureComponents{
		Payload:         payload,
		Timestamp:       0, // No timestamp - signature is for non-repudiation only
		TransferAddress: s.recorder.GetAccountAddress(),
		ExecutorAddress: "",
	}

	signerAddressStr := s.recorder.GetSignerAddress()
	signerAddress, err := sdk.AccAddressFromBech32(signerAddressStr)
	if err != nil {
		return "", err
	}
	accountSigner := &cmd.AccountSigner{
		Addr:    signerAddress,
		Keyring: s.recorder.GetKeyring(),
	}

	return calculations.Sign(accountSigner, components, calculations.Developer)
}
