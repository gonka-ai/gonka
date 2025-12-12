package validation

import (
	"bytes"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/completionapi"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/utils"
	"decentralized-api/logging"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/google/uuid"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrPayloadUnavailable indicates payloads could not be retrieved after all retries
// and the inference is post-upgrade (no on-chain fallback available).
var ErrPayloadUnavailable = errors.New("payload unavailable after all retries")

type InferenceValidator struct {
	recorder      cosmosclient.CosmosMessageClient
	nodeBroker    *broker.Broker
	configManager *apiconfig.ConfigManager
	phaseTracker  *chainphase.ChainPhaseTracker
}

func NewInferenceValidator(
	nodeBroker *broker.Broker,
	configManager *apiconfig.ConfigManager,
	recorder cosmosclient.CosmosMessageClient,
	phaseTracker *chainphase.ChainPhaseTracker) *InferenceValidator {
	return &InferenceValidator{
		nodeBroker:    nodeBroker,
		configManager: configManager,
		recorder:      recorder,
		phaseTracker:  phaseTracker,
	}
}

func (s *InferenceValidator) VerifyInvalidation(events map[string][]string, recorder cosmosclient.InferenceCosmosClient) {
	inferenceIds, ok := events["inference_validation.inference_id"]
	if !ok || len(inferenceIds) == 0 {
		logging.Error("No inference_id found in events", types.Validation)
		return
	}
	inferenceId := inferenceIds[0]

	logging.Debug("Verifying invalidation", types.Validation, "inference_id", inferenceId)

	queryClient := recorder.NewInferenceQueryClient()

	r, err := queryClient.Inference(recorder.GetContext(), &types.QueryGetInferenceRequest{Index: inferenceId})
	if err != nil {
		// FIXME: what should we do with validating the transaction?
		logging.Warn("Failed to query Inference for revalidation.", types.Validation, "error", err)
		return
	}

	logInferencesToValidate([]string{inferenceId})
	go func() {
		s.validateInferenceAndSendValMessage(r.Inference, recorder, true)
	}()

}

// shouldValidateInference determines if the current participant should validate a specific inference
// This function extracts the core validation decision logic for reuse in recovery scenarios
func (s *InferenceValidator) shouldValidateInference(
	inferenceDetails *types.InferenceValidationDetails,
	seed int64,
	validatorPower uint64,
	validatorAddress string,
	validationParams *types.ValidationParams,
) (bool, string) {
	// Skip if this participant is the executor
	if inferenceDetails.ExecutorId == validatorAddress {
		return false, "Skipping validation: participant is the executor"
	}

	// Skip if total power is invalid
	if inferenceDetails.TotalPower <= inferenceDetails.ExecutorPower {
		return false, "Skipping validation: total power is less than or equal to executor power"
	}

	// Use the same validation logic as real-time validations
	shouldValidate, message := calculations.ShouldValidate(
		seed,
		inferenceDetails,
		uint32(inferenceDetails.TotalPower),
		uint32(validatorPower),
		uint32(inferenceDetails.ExecutorPower),
		validationParams)

	return shouldValidate, message
}

func (s *InferenceValidator) getNodeModelsAtEpoch(epochIndex uint64, address string) (map[string]bool, error) {
	supportedModels := make(map[string]bool)
	parentEpochData, err := s.nodeBroker.GetChainBridge().GetEpochGroupDataByModelId(epochIndex, "")
	if err != nil {
		logging.Error("Failed to get epoch group data by model id", types.ValidationRecovery, "error", err)
		return nil, fmt.Errorf("failed to get epoch group data by model id: %w", err)
	}
	for _, modelId := range parentEpochData.EpochGroupData.SubGroupModels {
		subgroupResp, err := s.nodeBroker.GetChainBridge().GetEpochGroupDataByModelId(parentEpochData.EpochGroupData.EpochIndex, modelId)
		if err != nil {
			logging.Error("Failed to get subgroup epoch data", types.ValidationRecovery, "model_id", modelId, "error", err)
			continue
		}
		if subgroupResp == nil {
			logging.Warn("Subgroup epoch data response is nil", types.ValidationRecovery, "model_id", modelId)
			continue
		}

		subgroup := subgroupResp.EpochGroupData
		if subgroup.ModelSnapshot == nil {
			logging.Error("ModelSnapshot is nil in subgroup", types.ValidationRecovery, "model_id", modelId)
			continue
		}

		for _, weightInfo := range subgroup.ValidationWeights {
			if weightInfo.MemberAddress == address {
				supportedModels[modelId] = true
			}
		}
	}
	logging.Info("Supported models at epoch", types.ValidationRecovery, "epochIndex", epochIndex, "supportedModels", supportedModels, "address", address)

	return supportedModels, nil
}

func (s *InferenceValidator) getCurrentSupportedModels() (map[string]bool, error) {
	supportedModels := make(map[string]bool)
	nodes, err := s.nodeBroker.GetNodes()
	if err != nil {
		logging.Error("Failed to get nodes from broker", types.ValidationRecovery, "error", err)
		return nil, fmt.Errorf("failed to get nodes: %w", err)
	}
	for _, node := range nodes {
		nodeState := node.State
		for model := range nodeState.EpochModels {
			supportedModels[model] = true
		}
	}
	logging.Debug("Supported models", types.ValidationRecovery, "supportedModels", supportedModels)
	return supportedModels, nil
}

// processInferencePageForMissedValidations processes a single page of inferences and returns missed validations from that page.
// This is the batch processing function that filters by epoch, queries validation parameters, and checks validation status.
func (s *InferenceValidator) processInferencePageForMissedValidations(
	inferencePage []types.Inference,
	epochIndex uint64,
	seed int64,
	validatorPower uint64,
	address string,
	alreadyValidated map[string]bool,
	supportedModels map[string]bool,
	validationParams *types.ValidationParams,
	queryClient types.QueryClient,
) ([]types.Inference, error) {
	// Filter inferences by epoch
	var epochInferences []types.Inference
	for _, inf := range inferencePage {
		if inf.EpochId == epochIndex {
			epochInferences = append(epochInferences, inf)
		}
	}

	if len(epochInferences) == 0 {
		return nil, nil
	}

	// Create a map for quick lookup and collect inference IDs
	inferenceMap := make(map[string]types.Inference)
	inferenceIds := make([]string, len(epochInferences))
	for i, inf := range epochInferences {
		inferenceIds[i] = inf.InferenceId
		inferenceMap[inf.InferenceId] = inf
	}

	// Query validation parameters for this batch
	const batchSize = 1000
	var allValidationDetails []*types.InferenceValidationDetails

	for i := 0; i < len(inferenceIds); i += batchSize {
		end := i + batchSize
		if end > len(inferenceIds) {
			end = len(inferenceIds)
		}

		batch := inferenceIds[i:end]
		batchResp, err := queryClient.GetInferenceValidationParameters(s.recorder.GetContext(), &types.QueryGetInferenceValidationParametersRequest{
			Ids:       batch,
			Requester: address,
		})
		if err != nil {
			logging.Error("Failed to get validation parameters for batch", types.ValidationRecovery,
				"batchSize", len(batch),
				"error", err)
			return nil, fmt.Errorf("failed to get validation parameters: %w", err)
		}

		allValidationDetails = append(allValidationDetails, batchResp.Details...)
	}

	// Check each inference to see if it should have been validated but wasn't
	var missedValidations []types.Inference
	for _, inferenceDetails := range allValidationDetails {
		if !supportedModels[inferenceDetails.Model] {
			continue
		}

		// Check if this participant should validate this inference
		shouldValidate, _ := s.shouldValidateInference(
			inferenceDetails,
			seed,
			validatorPower,
			address,
			validationParams)

		// If should validate but didn't, add to missed list
		if shouldValidate && !alreadyValidated[inferenceDetails.InferenceId] {
			if inference, exists := inferenceMap[inferenceDetails.InferenceId]; exists {
				missedValidations = append(missedValidations, inference)
				logging.Debug("Found missed validation in page", types.ValidationRecovery, "inferenceId", inferenceDetails.InferenceId)
			}
		}
	}

	return missedValidations, nil
}

// DetectMissedValidations identifies which validations were missed for a specific epoch
// Returns a list of inference objects that the current participant should have validated but didn't
func (s *InferenceValidator) DetectMissedValidations(epochIndex uint64, seed int64) ([]types.Inference, error) {
	logging.Info("Starting missed validation detection", types.ValidationRecovery, "epochIndex", epochIndex, "seed", seed)

	queryClient := s.recorder.NewInferenceQueryClient()
	address := s.recorder.GetAddress()

	// Pre-fetch static data needed for all pages

	// Get validation params
	params, err := queryClient.Params(s.recorder.GetContext(), &types.QueryParamsRequest{})
	if err != nil {
		logging.Error("Failed to get params", types.ValidationRecovery, "error", err)
		return nil, fmt.Errorf("failed to get params: %w", err)
	}

	// Get what validations were already submitted by this participant
	epochGroupValidationsResp, err := queryClient.EpochGroupValidations(s.recorder.GetContext(), &types.QueryGetEpochGroupValidationsRequest{
		Participant: address,
		EpochIndex:  epochIndex,
	})

	// Create a set of already validated inference IDs
	alreadyValidated := make(map[string]bool)
	if err == nil {
		for _, inferenceId := range epochGroupValidationsResp.EpochGroupValidations.ValidatedInferences {
			alreadyValidated[inferenceId] = true
		}
	} else {
		if status.Code(err) == codes.NotFound {
			logging.Info("No epoch group validations found", types.ValidationRecovery, "participant", address, "epochIndex", epochIndex)
		} else {
			logging.Warn("Failed to get epoch group validations", types.ValidationRecovery, "error", err, "participant", address, "epochIndex", epochIndex)
		}
	}

	supportedModels, err := s.getNodeModelsAtEpoch(epochIndex, address)
	if err != nil {
		logging.Error("Failed to get supported models at epoch", types.ValidationRecovery, "error", err)
		return nil, fmt.Errorf("failed to get supported models at epoch: %w", err)
	}

	// Get validator power from the first batch that has epoch-matching inferences
	var validatorPower uint64
	var validatorPowerFetched bool

	// Process inferences page by page without accumulating all in memory
	var missedValidations []types.Inference
	var nextKey []byte
	const pageSize = 1000
	pageNumber := 0

	for {
		pageNumber++
		req := &types.QueryAllInferenceRequest{
			Pagination: &query.PageRequest{
				Key:   nextKey,
				Limit: pageSize,
			},
		}

		resp, err := queryClient.InferenceAll(s.recorder.GetContext(), req)
		if err != nil {
			logging.Error("Failed to query inferences page", types.ValidationRecovery, "error", err, "pageNumber", pageNumber)
			return nil, fmt.Errorf("failed to query inferences: %w", err)
		}

		logging.Debug("Processing inference page", types.ValidationRecovery,
			"pageNumber", pageNumber,
			"pageSize", len(resp.Inference),
			"hasMorePages", resp.Pagination != nil && len(resp.Pagination.NextKey) > 0)

		// Filter this page by epoch to check if we need to fetch validator power
		if !validatorPowerFetched {
			for _, inf := range resp.Inference {
				if inf.EpochId == epochIndex {
					// Found at least one epoch-matching inference, fetch validator power
					powerResp, err := queryClient.GetInferenceValidationParameters(s.recorder.GetContext(), &types.QueryGetInferenceValidationParametersRequest{
						Ids:       []string{inf.InferenceId},
						Requester: address,
					})
					if err != nil {
						logging.Error("Failed to get validator power", types.ValidationRecovery, "error", err)
						return nil, fmt.Errorf("failed to get validator power: %w", err)
					}
					validatorPower = powerResp.ValidatorPower
					validatorPowerFetched = true
					logging.Debug("Fetched validator power", types.ValidationRecovery, "validatorPower", validatorPower)
					break
				}
			}
		}

		// Process this page using the batch processor (only if we have validator power)
		if validatorPowerFetched {
			pageMissed, err := s.processInferencePageForMissedValidations(
				resp.Inference,
				epochIndex,
				seed,
				validatorPower,
				address,
				alreadyValidated,
				supportedModels,
				params.Params.ValidationParams,
				queryClient,
			)
			if err != nil {
				logging.Error("Failed to process inference page", types.ValidationRecovery, "error", err, "pageNumber", pageNumber)
				return nil, fmt.Errorf("failed to process inference page %d: %w", pageNumber, err)
			}

			if len(pageMissed) > 0 {
				missedValidations = append(missedValidations, pageMissed...)
				logging.Debug("Found missed validations in page", types.ValidationRecovery,
					"pageNumber", pageNumber,
					"missedCount", len(pageMissed))
			}
		}

		// Check if there are more pages
		if resp.Pagination == nil || len(resp.Pagination.NextKey) == 0 {
			break
		}
		nextKey = resp.Pagination.NextKey
	}

	logging.Info("Missed validation detection complete", types.ValidationRecovery,
		"epochIndex", epochIndex,
		"pagesProcessed", pageNumber,
		"missedValidations", len(missedValidations))

	return missedValidations, nil
}

// ExecuteRecoveryValidations executes validation for a list of missed inferences
// This function uses the inference data already obtained and executes validations in parallel goroutines
// It waits for all validations to complete before returning
func (s *InferenceValidator) ExecuteRecoveryValidations(missedInferences []types.Inference) (int, error) {

	availableModels, err := s.getCurrentSupportedModels()
	if err != nil {
		logging.Error("Failed to get currently available models", types.ValidationRecovery, "error", err)
		return 0, fmt.Errorf("failed to get currently available models: %w", err)
	}

	missedInferencesToValidate := []types.Inference{}
	for _, inf := range missedInferences {
		if availableModels[inf.Model] {
			missedInferencesToValidate = append(missedInferencesToValidate, inf)
		} else {
			logging.Info("Can't recover validation for inference, model not available", types.ValidationRecovery, "inferenceId", inf.InferenceId, "model", inf.Model)
		}
	}

	if len(missedInferences) > len(missedInferencesToValidate) {
		logging.Warn("Some inferences can't be recovered, model not available", types.ValidationRecovery, "missedInferences", len(missedInferences), "missedInferencesToValidate", len(missedInferencesToValidate))
	}

	if len(missedInferencesToValidate) == 0 {
		logging.Info("No missed validations to execute", types.ValidationRecovery)
		return 0, nil
	}

	logging.Info("Starting recovery validation execution", types.ValidationRecovery, "missedValidations", len(missedInferencesToValidate))

	var wg sync.WaitGroup

	// Execute recovery validations in parallel goroutines with WaitGroup synchronization
	for _, inf := range missedInferencesToValidate {
		wg.Add(1)
		go func(inference types.Inference) {
			defer wg.Done()

			logging.Info("Executing recovery validation", types.ValidationRecovery, "inferenceId", inference.InferenceId)

			// Use existing validation infrastructure
			// The validateInferenceAndSendValMessage function handles all validation logic, node locking, and message sending
			// Cast the interface back to concrete type (safe since it's always *InferenceCosmosClient)
			concreteRecorder := s.recorder.(*cosmosclient.InferenceCosmosClient)
			s.validateInferenceAndSendValMessage(inference, *concreteRecorder, false)

			logging.Info("Recovery validation completed", types.ValidationRecovery, "inferenceId", inference.InferenceId)
		}(inf)
	}

	// Wait for all recovery validations to complete
	logging.Info("Waiting for all recovery validations to complete", types.ValidationRecovery, "count", len(missedInferences))
	wg.Wait()

	logging.Info("All recovery validations completed", types.ValidationRecovery, "count", len(missedInferences))
	return len(missedInferencesToValidate), nil
}

func (s *InferenceValidator) WaitForValidationsToBeRecorded() {
	const maxTimeoutBlocks = 60
	epochLength := s.phaseTracker.GetEpochParams().EpochLength
	timeoutBlocks := min(epochLength/10, maxTimeoutBlocks)

	time.Sleep(5 * time.Duration(timeoutBlocks) * time.Second)
}

func (s *InferenceValidator) SampleInferenceToValidate(ids []string, transactionRecorder cosmosclient.InferenceCosmosClient) {
	if ids == nil {
		logging.Debug("No inferences to validate", types.Validation)
		return
	}

	logging.Debug("Sampling inf transactions to validate", types.Validation)

	queryClient := transactionRecorder.NewInferenceQueryClient()

	r, err := queryClient.GetInferenceValidationParameters(transactionRecorder.GetContext(), &types.QueryGetInferenceValidationParametersRequest{
		Ids:       ids,
		Requester: transactionRecorder.GetAddress(),
	})
	if err != nil {
		// FIXME: what should we do with validating the transaction?
		logging.Warn("Failed to query GetInferenceValidationParameters.", types.Validation, "error", err)
		return
	}

	params, err := queryClient.Params(transactionRecorder.GetContext(), &types.QueryParamsRequest{})
	if err != nil {
		logging.Error("Failed to get params", types.Validation, "error", err)
		return
	}

	supportedModels, err := s.getCurrentSupportedModels()
	if err != nil {
		logging.Error("Failed to get currently available models", types.Validation, "error", err)
		return
	}

	logInferencesToSample(r.Details)

	address := transactionRecorder.GetAddress()
	currentSeed := s.configManager.GetCurrentSeed().Seed
	var toValidateIds []string

	for _, inferenceWithExecutor := range r.Details {
		if !supportedModels[inferenceWithExecutor.Model] {
			logging.Debug("Skipping inference by not supported model", types.Validation, "inferenceId", inferenceWithExecutor.InferenceId, "model", inferenceWithExecutor.Model)
			continue
		}
		// Use the extracted validation decision logic
		shouldValidate, message := s.shouldValidateInference(
			inferenceWithExecutor,
			currentSeed,
			r.ValidatorPower,
			address,
			params.Params.ValidationParams)

		logging.Info(message, types.Validation, "inferenceId", inferenceWithExecutor.InferenceId, "seed", currentSeed, "validator", address)

		if shouldValidate {
			toValidateIds = append(toValidateIds, inferenceWithExecutor.InferenceId)
		}
	}

	logInferencesToValidate(toValidateIds)
	for _, inf := range toValidateIds {
		go func() {
			response, err := queryClient.Inference(transactionRecorder.GetContext(), &types.QueryGetInferenceRequest{Index: inf})
			if err != nil {
				logging.Error("Failed to get inference by id", types.Validation, "id", response, "error", err)
				return
			}
			s.validateInferenceAndSendValMessage(response.Inference, transactionRecorder, false)
		}()
	}
}

func logInferencesToSample(inferences []*types.InferenceValidationDetails) {
	var ids []struct {
		InferenceId string
		ExecutorId  string
	}

	for _, inf := range inferences {
		ids = append(ids, struct {
			InferenceId string
			ExecutorId  string
		}{
			InferenceId: inf.InferenceId,
			ExecutorId:  inf.ExecutorId,
		})
	}

	logging.Info("Inferences to sample", types.Validation, "ids", ids)
}

func logInferencesToValidate(toValidate []string) {
	var ids []string
	for _, inf := range toValidate {
		ids = append(ids, inf)
	}
	logging.Info("Inferences to validate", types.Validation, "inferences", ids)
}

func (s *InferenceValidator) validateInferenceAndSendValMessage(inf types.Inference, transactionRecorder cosmosclient.InferenceCosmosClient, revalidation bool) {
	promptPayload, responsePayload, err := s.retrievePayloadsWithRetry(inf)
	if err != nil {
		if errors.Is(err, ErrPayloadUnavailable) {
			// Post-upgrade inference: executor unavailable after 20 min of retries
			s.checkAndInvalidateUnavailable(inf, transactionRecorder, revalidation)
			return
		}
		if errors.Is(err, ErrHashMismatch) {
			// Executor served wrong payload with valid signature - immediate invalidation
			s.submitHashMismatchInvalidation(inf, transactionRecorder, revalidation)
			return
		}
		if errors.Is(err, ErrEpochStale) {
			// Epoch too old - validation no longer useful, just return
			logging.Info("Validation aborted: epoch stale", types.Validation,
				"inferenceId", inf.InferenceId, "inferenceEpoch", inf.EpochId)
			return
		}
		logging.Error("Failed to retrieve payloads", types.Validation,
			"inferenceId", inf.InferenceId, "error", err)
		return
	}

	// Check for duplicate AFTER payload retrieval - catches race conditions
	// where we already validated during the wait (up to 20 min)
	if !revalidation && s.isAlreadyValidated(inf.InferenceId, inf.EpochId, transactionRecorder) {
		logging.Info("Inference already validated by us, skipping", types.Validation,
			"inferenceId", inf.InferenceId)
		return
	}

	const maxRetries = 5
	const retryInterval = 4 * time.Minute

	var valResult ValidationResult

	// Retry logic for LockNode operation
	for attempt := 1; attempt <= maxRetries; attempt++ {
		valResult, err = broker.LockNode(s.nodeBroker, inf.Model, func(node *broker.Node) (ValidationResult, error) {
			return s.validateWithPayloads(inf, node, promptPayload, responsePayload)
		})

		if err == nil {
			// Success, break out of retry loop
			break
		}

		// For all errors, check if we should retry
		if attempt < maxRetries {
			logging.Warn("Failed to validate inference, retrying", types.Validation,
				"id", inf.InferenceId,
				"attempt", attempt,
				"maxRetries", maxRetries,
				"error", err,
				"nextRetryIn", retryInterval)
			time.Sleep(retryInterval)
		} else {
			// Final attempt failed - check if it's ErrNoNodesAvailable for special handling
			if errors.Is(err, broker.ErrNoNodesAvailable) {
				logging.Warn("Failed to validate inference after all retry attempts. No nodes available, probably unsupported model.", types.Validation, "id", inf.InferenceId, "attempts", maxRetries, "error", err)
				return
			} else {
				logging.Error("Failed to validate inference after all retry attempts", types.Validation,
					"id", inf.InferenceId,
					"attempts", maxRetries,
					"error", err)
				return
			}
		}
	}

	msgValidation, err := ToMsgValidation(valResult)
	if err != nil {
		logging.Error("Failed to convert to MsgValidation.", types.Validation, "id", inf.InferenceId, "error", err)
		return
	}
	msgValidation.Revalidation = revalidation

	if err = transactionRecorder.ReportValidation(msgValidation); err != nil {
		logging.Error("Failed to report validation.", types.Validation, "id", inf.InferenceId, "error", err)
		return
	}

	logging.Info("Successfully validated inference", types.Validation, "id", inf.InferenceId)
}

// isEpochStale returns true if inference epoch is too old for validation to be useful.
// Validation is pointless when currentEpoch >= inferenceEpoch + 2.
func (s *InferenceValidator) isEpochStale(inferenceEpochId uint64) bool {
	epochState := s.phaseTracker.GetCurrentEpochState()
	if epochState == nil {
		return false // Conservative: continue if state unknown
	}
	return epochState.LatestEpoch.EpochIndex >= inferenceEpochId+2
}

// isAlreadyValidated checks if this validator already submitted validation for the inference.
// Used to avoid duplicate work when multiple sources trigger validation for same inference.
func (s *InferenceValidator) isAlreadyValidated(inferenceId string, epochId uint64, recorder cosmosclient.InferenceCosmosClient) bool {
	queryClient := recorder.NewInferenceQueryClient()
	resp, err := queryClient.EpochGroupValidations(s.recorder.GetContext(), &types.QueryGetEpochGroupValidationsRequest{
		Participant: recorder.GetAddress(),
		EpochIndex:  epochId,
	})
	if err != nil {
		return false // Conservative: proceed if check fails
	}
	for _, id := range resp.EpochGroupValidations.ValidatedInferences {
		if id == inferenceId {
			return true
		}
	}
	return false
}

// retrievePayloadsWithRetry retrieves payloads from executor with retry logic.
// For pre-upgrade inferences (PromptPayload not empty), falls back to chain retrieval.
// For post-upgrade inferences, returns ErrPayloadUnavailable for caller to handle invalidation.
// Returns ErrHashMismatch immediately (no retry) when executor serves wrong payload with valid signature.
// Returns ErrEpochStale if inference epoch becomes too old during retries.
func (s *InferenceValidator) retrievePayloadsWithRetry(inf types.Inference) ([]byte, []byte, error) {
	const maxRetries = 10
	const retryInterval = 2 * time.Minute // 10 * 2 min = 20 min total

	ctx := s.recorder.GetContext()
	var lastErr error

	logging.Debug("Starting payload retrieval from executor", types.Validation,
		"inferenceId", inf.InferenceId, "executedBy", inf.ExecutedBy, "epochId", inf.EpochId)

	for attempt := 1; attempt <= maxRetries; attempt++ {
		// Check epoch staleness before each attempt
		if s.isEpochStale(inf.EpochId) {
			logging.Info("Epoch stale, stopping payload retrieval", types.Validation,
				"inferenceId", inf.InferenceId, "inferenceEpoch", inf.EpochId)
			return nil, nil, ErrEpochStale
		}

		promptPayload, responsePayload, err := RetrievePayloadsFromExecutor(
			ctx, inf.InferenceId, inf.ExecutedBy, inf.EpochId, s.recorder)

		if err == nil {
			logging.Debug("Successfully retrieved payloads from executor", types.Validation,
				"inferenceId", inf.InferenceId, "attempt", attempt)
			return promptPayload, responsePayload, nil
		}

		// Hash mismatch = executor signed wrong data = immediate invalidation (no retry)
		if errors.Is(err, ErrHashMismatch) {
			logging.Error("Hash mismatch detected, will invalidate immediately", types.Validation,
				"inferenceId", inf.InferenceId, "attempt", attempt)
			return nil, nil, ErrHashMismatch
		}

		lastErr = err
		logging.Warn("Payload retrieval failed, will retry", types.Validation,
			"inferenceId", inf.InferenceId,
			"attempt", attempt,
			"maxRetries", maxRetries,
			"error", err)

		// Wait between retries (skip sleep on final attempt since we're done)
		if attempt < maxRetries {
			time.Sleep(retryInterval)
		}
	}

	// Check if this is a pre-upgrade inference (has on-chain payload)
	if inf.PromptPayload != "" {
		logging.Warn("Retries exhausted, falling back to chain retrieval for pre-upgrade inference", types.Validation,
			"inferenceId", inf.InferenceId, "lastError", lastErr)
		return retrievePayloadsFromChain(ctx, inf.InferenceId, s.recorder)
	}

	// Post-upgrade inference: no on-chain fallback available
	logging.Warn("Retries exhausted for post-upgrade inference, will invalidate", types.Validation,
		"inferenceId", inf.InferenceId, "lastError", lastErr)
	return nil, nil, ErrPayloadUnavailable
}

// checkAndInvalidateUnavailable checks if inference is already invalidated by consensus,
// and if not, submits an invalidation for payload unavailability.
func (s *InferenceValidator) checkAndInvalidateUnavailable(inf types.Inference, transactionRecorder cosmosclient.InferenceCosmosClient, revalidation bool) {
	ctx := s.recorder.GetContext()
	queryClient := transactionRecorder.NewInferenceQueryClient()

	// Query current inference status from chain
	response, err := queryClient.Inference(ctx, &types.QueryGetInferenceRequest{Index: inf.InferenceId})
	if err != nil {
		logging.Error("Failed to query inference status for unavailability invalidation", types.Validation,
			"inferenceId", inf.InferenceId, "error", err)
		return
	}

	// Check if already invalidated by consensus
	if response.Inference.Status == types.InferenceStatus_INVALIDATED {
		logging.Info("Inference already invalidated by consensus, skipping unavailability invalidation", types.Validation,
			"inferenceId", inf.InferenceId)
		return
	}

	// Submit invalidation for payload unavailability
	logging.Warn("Submitting invalidation for payload unavailability", types.Validation,
		"inferenceId", inf.InferenceId, "currentStatus", response.Inference.Status)

	msgValidation := &inference.MsgValidation{
		Id:           uuid.New().String(),
		InferenceId:  inf.InferenceId,
		ResponseHash: "", // No response available
		Value:        0,  // Invalidation
		Revalidation: revalidation,
	}

	if err := transactionRecorder.ReportValidation(msgValidation); err != nil {
		logging.Error("Failed to report unavailability invalidation", types.Validation,
			"inferenceId", inf.InferenceId, "error", err)
		return
	}

	logging.Info("Successfully submitted unavailability invalidation", types.Validation,
		"inferenceId", inf.InferenceId)
}

// submitHashMismatchInvalidation submits an invalidation when executor served wrong payload
// with a valid signature (hash mismatch detected).
// TODO: Phase 7 - use executor's signed proof for fast invalidation without voting
func (s *InferenceValidator) submitHashMismatchInvalidation(inf types.Inference, transactionRecorder cosmosclient.InferenceCosmosClient, revalidation bool) {
	ctx := s.recorder.GetContext()
	queryClient := transactionRecorder.NewInferenceQueryClient()

	// Query current inference status from chain
	response, err := queryClient.Inference(ctx, &types.QueryGetInferenceRequest{Index: inf.InferenceId})
	if err != nil {
		logging.Error("Failed to query inference status for hash mismatch invalidation", types.Validation,
			"inferenceId", inf.InferenceId, "error", err)
		return
	}

	// Check if already invalidated by consensus
	if response.Inference.Status == types.InferenceStatus_INVALIDATED {
		logging.Info("Inference already invalidated by consensus, skipping hash mismatch invalidation", types.Validation,
			"inferenceId", inf.InferenceId)
		return
	}

	// Submit invalidation for hash mismatch (executor served wrong data)
	logging.Warn("Submitting invalidation for hash mismatch (executor served wrong payload)", types.Validation,
		"inferenceId", inf.InferenceId, "currentStatus", response.Inference.Status)

	msgValidation := &inference.MsgValidation{
		Id:           uuid.New().String(),
		InferenceId:  inf.InferenceId,
		ResponseHash: "", // Wrong payload - don't use its hash
		Value:        0,  // Invalidation
		Revalidation: revalidation,
	}

	if err := transactionRecorder.ReportValidation(msgValidation); err != nil {
		logging.Error("Failed to report hash mismatch invalidation", types.Validation,
			"inferenceId", inf.InferenceId, "error", err)
		return
	}

	logging.Info("Successfully submitted hash mismatch invalidation", types.Validation,
		"inferenceId", inf.InferenceId)
}

// validateWithPayloads validates inference using provided payloads.
func (s *InferenceValidator) validateWithPayloads(inference types.Inference, inferenceNode *broker.Node, promptPayload, responsePayload []byte) (ValidationResult, error) {
	logging.Debug("Validating inference", types.Validation, "id", inference.InferenceId)

	if inference.Status == types.InferenceStatus_STARTED {
		logging.Error("Inference not finished", types.Validation, "status", inference.Status, "inference", inference)
		return nil, errors.New("Inference is not finished. id = " + inference.InferenceId)
	}

	var requestMap map[string]interface{}
	if err := json.Unmarshal(promptPayload, &requestMap); err != nil {
		return &InvalidInferenceResult{inference.InferenceId, "Failed to unmarshal promptPayload.", err}, nil
	}

	originalResponse, err := unmarshalResponsePayload(responsePayload)
	if err != nil {
		return &InvalidInferenceResult{inference.InferenceId, "Failed to unmarshal responsePayload.", err}, nil
	}

	enforcedTokens, err := originalResponse.GetEnforcedTokens()
	if err != nil {
		return &InvalidInferenceResult{inference.InferenceId, "Failed to get enforced string.", err}, nil
	}

	// From here on, errors are on the part of the validator, not the inference that was passed in
	requestMap["enforced_tokens"] = enforcedTokens
	requestMap["stream"] = false
	requestMap["skip_special_tokens"] = false
	delete(requestMap, "stream_options")

	requestBody, err := json.Marshal(requestMap)
	if err != nil {
		return nil, err
	}

	completionsUrl, err := url.JoinPath(inferenceNode.InferenceUrlWithVersion(s.configManager.GetCurrentNodeVersion()), "v1/chat/completions")
	if err != nil {
		logging.Error("Failed to join url", types.Validation, "url", inferenceNode.InferenceUrlWithVersion(s.configManager.GetCurrentNodeVersion()), "error", err)
		return nil, err
	}

	resp, err := http.Post(
		completionsUrl,
		"application/json",
		bytes.NewReader(requestBody),
	)
	if err != nil {
		return nil, err
	}

	respBodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	logging.Debug("responseValidation", types.Validation, "validation", string(respBodyBytes))
	responseValidation, err := completionapi.NewCompletionResponseFromBytes(respBodyBytes)
	if err != nil {
		logging.Error("Failed to unmarshal responseValidation", types.Validation, "id", inference.InferenceId, "error", err)
		return nil, err
	}

	originalLogits := originalResponse.ExtractLogits()
	validationLogits := responseValidation.ExtractLogits()
	baseResult := BaseValidationResult{
		InferenceId:   inference.InferenceId,
		ResponseBytes: respBodyBytes,
	}
	if len(originalLogits) == 0 || len(validationLogits) == 0 {
		logging.Error("No logits found in original or validation response", types.Validation, "id", inference.InferenceId, "originalLogits", originalLogits, "validationLogits", validationLogits)
		return nil, errors.New("no logits found in original or validation response")
	}

	return compareLogits(originalLogits, validationLogits, baseResult), nil
}

func unmarshalResponse(inference *types.Inference) (completionapi.CompletionResponse, error) {
	return unmarshalResponsePayload([]byte(inference.ResponsePayload))
}

// unmarshalResponsePayload parses response payload string into CompletionResponse.
func unmarshalResponsePayload(responsePayload []byte) (completionapi.CompletionResponse, error) {
	resp, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(responsePayload)

	if err != nil {
		logging.Error("Failed to unmarshal responsePayload", types.Validation, "error", err)
	}

	switch resp.(type) {
	case *completionapi.StreamedCompletionResponse:
		logging.Debug("Unmarshalled responsePayload into StreamedResponse", types.Validation)
	case *completionapi.JsonCompletionResponse:
		logging.Debug("Unmarshalled responsePayload into JsonResponse", types.Validation)
	default:
		logging.Error("Failed to unmarshal responsePayload into StreamedResponse or JsonResponse", types.Validation)
	}

	return resp, err
}

type ValidationResult interface {
	GetInferenceId() string

	GetValidationResponseBytes() []byte

	IsSuccessful() bool
}

type BaseValidationResult struct {
	InferenceId   string
	ResponseBytes []byte
}

func (r BaseValidationResult) GetInferenceId() string {
	return r.InferenceId
}

func (r BaseValidationResult) GetValidationResponseBytes() []byte {
	return r.ResponseBytes
}

type DifferentLengthValidationResult struct {
	BaseValidationResult
}

func (DifferentLengthValidationResult) IsSuccessful() bool {
	return false
}

type DifferentTokensValidationResult struct {
	BaseValidationResult
}

func (DifferentTokensValidationResult) IsSuccessful() bool {
	return false
}

type SimilarityValidationResult struct {
	BaseValidationResult
	Value float64
}

func (r SimilarityValidationResult) IsSuccessful() bool {
	return r.Value > 0.99
}

type InvalidInferenceResult struct {
	InferenceId string
	Reason      string
	Error       error
}

func (r InvalidInferenceResult) IsSuccessful() bool {
	return false
}

func (r InvalidInferenceResult) GetInferenceId() string {
	return r.InferenceId
}

func (r InvalidInferenceResult) GetValidationResponseBytes() []byte {
	return []byte{}
}

func compareLogits(
	originalLogits []completionapi.Logprob,
	validationLogits []completionapi.Logprob,
	baseComparisonResult BaseValidationResult,
) ValidationResult {
	if len(originalLogits) != len(validationLogits) {
		logging.Warn("Different length of logits", types.Validation, "originalLogits", originalLogits, "validationLogits", validationLogits, "lengthOriginal", len(originalLogits), "lengthValidation", len(validationLogits))
	}
	if len(validationLogits) < len(originalLogits) {
		logging.Warn("Validation logits are shorter than original logits", types.Validation, "originalLogits", originalLogits, "validationLogits", validationLogits, "lengthOriginal", len(originalLogits), "lengthValidation", len(validationLogits))
		return &DifferentLengthValidationResult{baseComparisonResult}
	}

	for i := range originalLogits {
		o := originalLogits[i]
		v := validationLogits[i]
		if o.Token != v.Token {
			logging.Error("Different tokens in logits", types.Validation, "originalLogits", originalLogits, "validationLogits", validationLogits)

			return &DifferentTokensValidationResult{baseComparisonResult}
		}
	}
	similarity := customSimilarity(originalLogits, validationLogits)

	return &SimilarityValidationResult{BaseValidationResult: baseComparisonResult, Value: similarity}
}

func customSimilarity(
	originalLogprobs []completionapi.Logprob,
	validationLogprobs []completionapi.Logprob,
) float64 {
	distance, err := customDistance(originalLogprobs, validationLogprobs)
	if err != nil {
		logging.Error("Error calculating custom distance", types.Validation, "error", err)
		return 0
	}
	similarity := 1 - distance
	if similarity < 0 {
		logging.Error("Similarity value is negative", types.Validation, "similarity", similarity)
		return 0
	}
	return similarity
}

func customDistance(
	originalLogprobs []completionapi.Logprob,
	validationLogprobs []completionapi.Logprob,
) (float64, error) {
	distance := 0.0
	for i := range originalLogprobs {
		o := originalLogprobs[i]
		v := validationLogprobs[i]
		posDistance, err := positionDistance(o.TopLogprobs, v.TopLogprobs)
		if err != nil {
			logging.Error("Error calculating position distance", types.Validation, "error", err)
			return math.Inf(1), err
		}
		distance += posDistance
	}
	totalLogprobs := max(100, len(originalLogprobs)) * len(originalLogprobs[0].TopLogprobs)

	return distance / float64(totalLogprobs), nil
}

func positionDistance(
	originalLogprobs []completionapi.TopLogprobs,
	validationLogprobs []completionapi.TopLogprobs,
) (float64, error) {
	if len(originalLogprobs) == 0 || len(validationLogprobs) == 0 {
		return 0.0, fmt.Errorf("empty logprobs provided")
	}
	distance := 0.0

	originalLogprobMap := make(map[string]float64)
	for _, o := range originalLogprobs {
		originalLogprobMap[o.Token] = o.Logprob
	}
	sortedLogprobs := make([]float64, 0, len(originalLogprobMap))
	for _, logprob := range originalLogprobMap {
		sortedLogprobs = append(sortedLogprobs, logprob)
	}

	sort.Float64s(sortedLogprobs)

	var minOriginalLogprob1, minOriginalLogprob2 float64
	if len(sortedLogprobs) >= 2 {
		minOriginalLogprob1 = sortedLogprobs[0]
		minOriginalLogprob2 = sortedLogprobs[1]
	} else if len(sortedLogprobs) == 1 {
		minOriginalLogprob1 = sortedLogprobs[0]
		minOriginalLogprob2 = minOriginalLogprob1 - 100.0
	}

	// Estimate the next logprob value (2 as fine)
	nextOriginalLogprob := minOriginalLogprob1 - (minOriginalLogprob2 - minOriginalLogprob1)

	for _, v := range validationLogprobs {
		var originalLogprob float64
		if origProb, exists := originalLogprobMap[v.Token]; exists {
			originalLogprob = origProb
		} else {
			originalLogprob = nextOriginalLogprob
		}

		denom := 1e-6 + math.Abs(v.Logprob) + math.Abs(originalLogprob)
		distance += math.Abs(v.Logprob-originalLogprob) / denom / 2.0
	}

	return distance, nil
}

func ToMsgValidation(result ValidationResult) (*inference.MsgValidation, error) {
	// Match type of result from implementations of ValidationResult
	var simVal float64
	switch result.(type) {
	case *DifferentLengthValidationResult:
		logging.Warn("Different length validation result", types.Validation)
		simVal = 0
	case *DifferentTokensValidationResult:
		logging.Warn("Different tokens validation result", types.Validation)
		simVal = 0
	case *SimilarityValidationResult:
		simVal = result.(*SimilarityValidationResult).Value
		logging.Info("Cosine similarity validation result", types.Validation, "cosineSimValue", simVal)
	case *InvalidInferenceResult:
		simVal = 0
		logging.Warn("Invalid inference result", types.Validation, "reason", result.(*InvalidInferenceResult).Reason, "inferenceId", result.GetInferenceId(), "error", result.(*InvalidInferenceResult).Error)
	default:
		logging.Error("Unknown validation result type", types.Validation, "type", fmt.Sprintf("%T", result), "result", result)
		return nil, errors.New("unknown validation result type")
	}

	responseHash, _, err := utils.GetResponseHash(result.GetValidationResponseBytes())
	if err != nil {
		logging.Error("Failed to get response hash", types.Validation, "error", err)
		return nil, err
	}

	return &inference.MsgValidation{
		Id:           uuid.New().String(),
		InferenceId:  result.GetInferenceId(),
		ResponseHash: responseHash,
		Value:        simVal,
	}, nil
}
