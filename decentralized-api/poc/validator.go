package poc

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net/url"
	"slices"
	"sync"
	"time"

	"common/logging"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/mlnodeclient"
	"decentralized-api/poc/artifacts"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

const (
	POC_VALIDATE_GET_NODES_RETRIES     = 30
	POC_VALIDATE_GET_NODES_RETRY_DELAY = 5 * time.Second
	maxRetryBackoff                    = 45 * time.Second
)

// proofFetcher abstracts proof retrieval so it can be stubbed in tests.
type proofFetcher interface {
	FetchAndVerifyProofs(ctx context.Context, participantUrl string, req ProofRequest) ([]VerifiedArtifact, error)
	FetchAndVerifyProofsByNonce(ctx context.Context, participantUrl string, req ProofByNonceRequest) ([]VerifiedArtifact, error)
}

// nodeBrokerFacade is the subset of broker.Broker used by OffChainValidator.
type nodeBrokerFacade interface {
	NewNodeClient(node *broker.Node) mlnodeclient.MLNodeClient
	GetNodes() ([]broker.NodeResponse, error)
}

// OffChainValidator handles off-chain PoC validation using SMST proofs.
type OffChainValidator struct {
	recorder         cosmosclient.CosmosMessageClient
	nodeBroker       nodeBrokerFacade
	phaseTracker     *chainphase.ChainPhaseTracker
	callbackUrl      string
	pubKey           string
	validatorAddress string
	chainNodeUrl     string

	config ValidationConfig
	// guard is the optional DAPI-only early-share guard. A nil guard is a no-op.
	guard         *EarlyShareGuard
	artifactStore *artifacts.ManagedArtifactStore
}

// ValidationConfig contains configuration for off-chain validation.
type ValidationConfig struct {
	WorkerCount        int
	RequestTimeout     time.Duration
	MaxRetries         int
	RetryBackoff       time.Duration
	PhaseCheckInterval time.Duration
}

// DefaultValidationConfig returns the default configuration.
func DefaultValidationConfig() ValidationConfig {
	return ValidationConfig{
		WorkerCount:        10,
		RequestTimeout:     20 * time.Second,
		MaxRetries:         25,
		RetryBackoff:       3 * time.Second,
		PhaseCheckInterval: 3 * time.Second,
	}
}

func retryBackoffDelay(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = DefaultValidationConfig().RetryBackoff
	}
	if attempt < 0 {
		attempt = 0
	}
	delay := time.Duration(float64(base) * math.Pow(1.5, float64(attempt)))
	if delay > maxRetryBackoff {
		return maxRetryBackoff
	}
	return delay
}

// Reject a commit whose highest nonce exceeds its artifact count by this factor.
const porosityThreshold = 100.0

// validateResult represents the outcome of validating a participant.
type validateResult int

const (
	validateDispatched    validateResult = iota // Handed to an ML node; its verdict comes on the callback
	validateFailPermanent                       // Validatee at fault (fraud, invalid proof) - vote -1, no retry
	validateFailRetry                           // Validatee unreachable - retry, vote -1 once attempts run out
	validateAbstain                             // We cannot validate - no vote, no retry
	validateAbstainRetry                        // We cannot validate yet - retry, no vote once attempts run out
)

// participantWork represents a single participant to validate.
type participantWork struct {
	address    string
	modelId    string
	url        string
	pubKey     string
	count      uint32
	rootHash   []byte
	attempt    int       // current attempt number (0-based)
	retryAfter time.Time // don't process before this time

	checksPassed bool               // validatee checks already passed; retries only redo the local dispatch
	verified     []VerifiedArtifact // artifacts from those checks, reused by every retry
}

type modelSamplingData struct {
	entries     []calculations.WeightEntry
	totalWeight int64
}

func hasModelList(snapshotFound bool, modelSampling map[string]*modelSamplingData) bool {
	return snapshotFound && len(modelSampling) > 0
}

func buildValidationCallbackURL(callbackBase, modelID string) string {
	return callbackBase + "/v2/poc-batches/" + url.PathEscape(url.PathEscape(modelID))
}

// absInt32 returns the absolute value of an int32 as int64,
// safely handling math.MinInt32 which overflows when negated in int32.
func absInt32(n int32) int64 {
	v := int64(n)
	if v < 0 {
		return -v
	}
	return v
}

func maxNonceValue(artifacts []VerifiedArtifact) int64 {
	var maxAbs int64
	for _, artifact := range artifacts {
		if a := absInt32(artifact.Nonce); a > maxAbs {
			maxAbs = a
		}
	}
	return maxAbs
}

func isPorosityTooHigh(artifacts []VerifiedArtifact, totalCount uint32) (maxNonce int64, porosity float64, tooHigh bool) {
	if len(artifacts) == 0 || totalCount == 0 {
		return 0, 0, false
	}

	maxNonce = maxNonceValue(artifacts)
	porosity = float64(maxNonce) / float64(totalCount)
	return maxNonce, porosity, porosity >= porosityThreshold
}

// NewOffChainValidator creates a new off-chain validator.
func NewOffChainValidator(
	recorder cosmosclient.CosmosMessageClient,
	nodeBroker nodeBrokerFacade,
	phaseTracker *chainphase.ChainPhaseTracker,
	callbackUrl string,
	pubKey string,
	validatorAddress string,
	chainNodeUrl string,
	config ValidationConfig,
	guard *EarlyShareGuard,
	artifactStore *artifacts.ManagedArtifactStore,
) *OffChainValidator {
	return &OffChainValidator{
		recorder:         recorder,
		nodeBroker:       nodeBroker,
		phaseTracker:     phaseTracker,
		callbackUrl:      callbackUrl,
		pubKey:           pubKey,
		validatorAddress: validatorAddress,
		chainNodeUrl:     chainNodeUrl,
		config:           config,
		guard:            guard,
		artifactStore:    artifactStore,
	}
}

// SyncArtifactStoreStage pins GetCurrentPocStageHeight in RAM while synced.
// The previous stage is unloaded only when that height changes (next PoC or
// confirmation PoC). Nil/unsynced leaves the pin alone. Invalid height leaves
// the pin alone (do not fail-closed deactivate, that can drop a live tree).
func (v *OffChainValidator) SyncArtifactStoreStage(epochState chainphase.EpochState) {
	if v.artifactStore == nil {
		return
	}
	if epochState.IsNilOrNotSynced() {
		return
	}
	height := GetCurrentPocStageHeight(&epochState)
	if height <= 0 {
		return
	}
	v.artifactStore.ActivateStage(height)
}

// MaybeCaptureEarlyShare is invoked once per block by the dispatcher. From the
// first-fraction height until the end of the generation window it attempts the
// early-share capture. The capture query is pinned to the exact first-fraction
// height (see EarlyShareGuard.MaybeCapture), so a capture that runs a few
// blocks late (after a restart, a slow query, or a transient chain error)
// still records the identical consensus snapshot every other validator gets.
// MaybeCapture is idempotent, so per-block re-invocation acts as a retry loop
// that stops at the first completed capture.
func (v *OffChainValidator) MaybeCaptureEarlyShare(epochState chainphase.EpochState) {
	if v.artifactStore != nil {
		v.maybeWarmEarlySnapshot(epochState)
	}
	if v.guard.Enabled() {
		stage, target, ok := EarlyShareCaptureTarget(&epochState, v.guard.FirstFraction())
		if ok && epochState.CurrentBlock.Height >= target {
			go v.guard.MaybeCapture(context.Background(), v.recorder.NewInferenceQueryClient(), stage, target, epochState.CurrentBlock.Height)
		}
	}
}

func (v *OffChainValidator) maybeWarmEarlySnapshot(epochState chainphase.EpochState) {
	// Use the guard's configured fraction so the warm-up targets the same
	// checkpoint height validators capture at. FirstFraction is nil-safe and
	// falls back to the default when the guard is absent.
	stage, target, ok := EarlyShareCaptureTarget(&epochState, v.guard.FirstFraction())
	if !ok || epochState.CurrentBlock.Height != target {
		return
	}
	if v.validatorAddress == "" {
		return
	}

	stageStores, err := v.artifactStore.GetStoresForStage(stage)
	if err != nil || len(stageStores) == 0 {
		return
	}

	queryClient := v.recorder.NewInferenceQueryClient()
	for _, stageStore := range stageStores {
		if stageStore.Store == nil {
			continue
		}
		modelID := stageStore.ModelID
		store := stageStore.Store
		go func(modelID string, store artifacts.ArtifactStore) {
			resp, err := queryClient.PoCV2StoreCommit(context.Background(), &types.QueryPoCV2StoreCommitRequest{
				PocStageStartBlockHeight: stage,
				ParticipantAddress:       v.validatorAddress,
				ModelId:                  modelID,
			})
			if err != nil || !resp.Found || resp.Count == 0 {
				if err != nil {
					logging.Debug("OffChainValidator: early snapshot warm-up query failed", types.PoC,
						"stage", stage, "modelId", modelID, "error", err)
				}
				return
			}
			store.WarmSnapshot(resp.Count)
		}(modelID, store)
	}
}

func (v *OffChainValidator) ValidateAll(pocStageStartBlockHeight int64, pocStartBlockHash string) {
	logging.Info("OffChainValidator: starting validation", types.PoC,
		"pocStageStartBlockHeight", pocStageStartBlockHeight,
		"pocStartBlockHash", pocStartBlockHash)

	if pocStartBlockHash == "" {
		logging.Error("OffChainValidator: PoC start block hash is empty", types.PoC)
		return
	}

	epochState := v.phaseTracker.GetCurrentEpochState()
	if epochState == nil {
		logging.Error("OffChainValidator: epoch state is nil", types.PoC)
		return
	}

	samplingBlockHash := v.getSamplingBlockHash(epochState)
	if samplingBlockHash == "" {
		logging.Error("OffChainValidator: failed to get sampling block hash", types.PoC)
		return
	}

	logging.Info("OffChainValidator: block hashes", types.PoC,
		"samplingBlockHash", samplingBlockHash,
		"pocStartBlockHash", pocStartBlockHash)

	queryClient := v.recorder.NewInferenceQueryClient()
	paramsResp, err := queryClient.Params(context.Background(), &types.QueryParamsRequest{})
	if err != nil {
		logging.Error("OffChainValidator: failed to get params", types.PoC, "error", err)
		return
	}
	pocParams := paramsResp.Params.PocParams
	sampleSize := int(pocParams.ValidationSampleSize)
	if sampleSize == 0 {
		sampleSize = 200
	}

	nodes, err := v.getNodesWithRetry(pocStageStartBlockHeight)
	if err != nil {
		logging.Error("OffChainValidator: failed to get nodes for validation", types.PoC,
			"pocStageStartBlockHeight", pocStageStartBlockHeight, "error", err)
		return
	}
	if len(nodes) == 0 {
		logging.Error("OffChainValidator: no nodes available", types.PoC)
		return
	}

	v.stopGenerationOnAllNodes(nodes)

	commitsResp, err := queryClient.AllPoCV2StoreCommitsForStage(context.Background(),
		&types.QueryAllPoCV2StoreCommitsForStageRequest{
			PocStageStartBlockHeight: pocStageStartBlockHeight,
		})
	if err != nil {
		logging.Error("OffChainValidator: failed to query commits", types.PoC, "error", err)
		return
	}

	if len(commitsResp.Commits) == 0 {
		logging.Info("OffChainValidator: no commits found for stage", types.PoC,
			"pocStageStartBlockHeight", pocStageStartBlockHeight)
		return
	}

	logging.Info("OffChainValidator: found participants with commits", types.PoC,
		"count", len(commitsResp.Commits))

	// Query validation snapshot for per-model sampling and authoritative model gating.
	// An empty model set is treated as non-authoritative so bootstrap/startup epochs
	// do not accidentally exclude all validation work.
	validationSlots := int(pocParams.ValidationSlots)
	var snapshotAppHash string
	var snapshotTotalNetworkWeight int64
	snapshotFound := false
	modelSampling := make(map[string]*modelSamplingData)
	// modelVotingPowers holds established per-model voting power (model_id ->
	// participant -> voting power) for the early-share guard's weighted median.
	modelVotingPowers := make(map[string]map[string]int64)
	snapshotResp, err := queryClient.PoCValidationSnapshot(context.Background(),
		&types.QueryPoCValidationSnapshotRequest{
			PocStageStartHeight: pocStageStartBlockHeight,
		})
	if err != nil {
		if validationSlots > 0 {
			logging.Warn("OffChainValidator: failed to query validation snapshot, falling back to O(N^2)", types.PoC,
				"error", err)
			validationSlots = 0
		}
	} else if snapshotResp.Found && snapshotResp.Snapshot != nil {
		snapshotFound = true
		snapshotAppHash = snapshotResp.Snapshot.AppHash
		snapshotTotalNetworkWeight = snapshotResp.Snapshot.TotalNetworkWeight
		for _, mvw := range snapshotResp.Snapshot.ModelVotingPowers {
			weights := types.VotingPowerSliceToMap(mvw.VotingPowers)
			modelVotingPowers[mvw.ModelId] = weights
			entries, total := calculations.PrepareSortedEntries(weights)
			modelSampling[mvw.ModelId] = &modelSamplingData{entries: entries, totalWeight: total}
		}
		if validationSlots > 0 {
			logging.Info("OffChainValidator: using per-model validation snapshot for sampling", types.PoC,
				"appHash", snapshotAppHash,
				"validationSlots", validationSlots,
				"numModels", len(modelSampling),
			)
		}
	} else if validationSlots > 0 {
		logging.Warn("OffChainValidator: validation snapshot not found, falling back to O(N^2)", types.PoC)
		validationSlots = 0
	}

	// Build work items with participant URLs
	workItems := make([]participantWork, 0, len(commitsResp.Commits))
	skippedNotAssigned := 0
	skippedExcludedModel := 0
	authoritativeModelAllowlist := hasModelList(snapshotFound, modelSampling)
	for _, commit := range commitsResp.Commits {
		if authoritativeModelAllowlist {
			if _, allowed := modelSampling[commit.ModelId]; !allowed {
				skippedExcludedModel++
				continue
			}
		}

		// If sampling is enabled, check if we're assigned to validate this participant-model pair.
		// Only the model-local share of slots is sampled; the remainder behaves as abstention.
		if validationSlots > 0 {
			sampling, hasSampling := modelSampling[commit.ModelId]
			if hasSampling {
				sampledSlots := calculations.ComputeSampledSlotCount(sampling.totalWeight, snapshotTotalNetworkWeight, validationSlots)
				assignedValidators := calculations.GetSlotsFromSorted(
					snapshotAppHash,
					commit.ParticipantAddress,
					commit.ModelId,
					sampling.entries,
					sampling.totalWeight,
					sampledSlots,
				)
				if !slices.Contains(assignedValidators, v.validatorAddress) {
					skippedNotAssigned++
					continue
				}
			}
		}

		participantResp, err := queryClient.Participant(context.Background(),
			&types.QueryGetParticipantRequest{Index: commit.ParticipantAddress})
		if err != nil {
			logging.Warn("OffChainValidator: failed to get participant", types.PoC,
				"address", commit.ParticipantAddress, "error", err)
			continue
		}

		if participantResp.Participant.InferenceUrl == "" {
			logging.Warn("OffChainValidator: participant has no URL", types.PoC,
				"address", commit.ParticipantAddress)
			continue
		}

		// The ML node needs the participant's public key, which only the commit carries.
		if commit.HexPubKey == "" {
			logging.Warn("OffChainValidator: participant has no public key", types.PoC,
				"address", commit.ParticipantAddress)
			continue
		}

		workItems = append(workItems, participantWork{
			address:  commit.ParticipantAddress,
			modelId:  commit.ModelId,
			url:      participantResp.Participant.InferenceUrl,
			pubKey:   commit.HexPubKey,
			count:    commit.Count,
			rootHash: commit.RootHash,
		})
	}

	if validationSlots > 0 || snapshotFound {
		logging.Info("OffChainValidator: filtered work items before validation", types.PoC,
			"totalCommits", len(commitsResp.Commits),
			"assignedToUs", len(workItems),
			"skippedNotAssigned", skippedNotAssigned,
			"skippedExcludedModel", skippedExcludedModel,
		)
	}

	if len(workItems) == 0 {
		logging.Warn("OffChainValidator: no valid work items", types.PoC)
		return
	}

	// Early-share guard: precompute per-participant decisions over the whole
	// stage (weighted median needs the full distribution), advancing miss-streak
	// state only for the participants this validator is assigned to. A nil
	// runtime means the guard is disabled or skipped (fail open).
	var gr *guardRuntime
	if v.guard.Enabled() {
		assigned := make(map[string]bool, len(workItems))
		for _, item := range workItems {
			assigned[earlyShareKey(item.address, item.modelId)] = true
		}
		// A confirmation PoC (CPoC) run is identified by an active confirmation
		// event during the inference phase whose trigger height matches this
		// stage. Only a passing CPoC clears the miss streak.
		isConfirmation := epochState.ActiveConfirmationPoCEvent != nil &&
			epochState.CurrentPhase == types.InferencePhase &&
			pocStageStartBlockHeight == epochState.ActiveConfirmationPoCEvent.TriggerHeight
		decisions := v.guard.Evaluate(context.Background(), pocStageStartBlockHeight, isConfirmation, commitsResp.Commits, modelVotingPowers, assigned)
		if len(decisions) > 0 {
			gr = &guardRuntime{
				guard:             v.guard,
				decisions:         decisions,
				stage:             pocStageStartBlockHeight,
				validatorPubKey:   v.pubKey,
				samplingBlockHash: samplingBlockHash,
			}
		}
	}

	// Randomize order to avoid thundering herd
	rand.Shuffle(len(workItems), func(i, j int) {
		workItems[i], workItems[j] = workItems[j], workItems[i]
	})

	proofClient := NewProofClient(v.recorder, ProofClientConfig{
		Timeout: v.config.RequestTimeout,
	})

	// Buffered so a worker re-queueing a retry never blocks on a full channel.
	workChan := make(chan participantWork, len(workItems)*2)
	var wg sync.WaitGroup

	var statsMu sync.Mutex
	dispatchedCount := 0
	failCount := 0
	abstainCount := 0
	pendingCount := len(workItems)

	// Context for coordinating shutdown. The phase watcher cancels as soon
	// as the chain stops accepting validation results for this PoC stage.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v.cancelWhenValidationPhaseEnds(ctx, cancel, pocStageStartBlockHeight)

	numWorkers := v.config.WorkerCount
	if numWorkers > len(workItems) {
		numWorkers = len(workItems)
	}
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			v.worker(
				ctx,
				cancel,
				workerID,
				workChan,
				proofClient,
				nodes,
				pocStageStartBlockHeight,
				samplingBlockHash,
				pocStartBlockHash,
				pocParams,
				sampleSize,
				gr,
				&statsMu,
				&dispatchedCount,
				&failCount,
				&abstainCount,
				&pendingCount,
			)
		}(i)
	}

	for _, item := range workItems {
		workChan <- item
	}

	wg.Wait()
	close(workChan)

	logging.Info("OffChainValidator: validation complete", types.PoC,
		"pocStageStartBlockHeight", pocStageStartBlockHeight,
		"totalParticipants", len(workItems),
		"dispatched", dispatchedCount,
		"votedInvalid", failCount,
		"abstained", abstainCount)
}

func (v *OffChainValidator) cancelWhenValidationPhaseEnds(ctx context.Context, cancel context.CancelFunc, pocStageStartBlockHeight int64) {
	phaseCheckInterval := v.config.PhaseCheckInterval
	if phaseCheckInterval <= 0 {
		phaseCheckInterval = 3 * time.Second
	}

	go func() {
		if v.cancelIfValidationPhaseEnded(cancel, pocStageStartBlockHeight) {
			return
		}

		ticker := time.NewTicker(phaseCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if v.cancelIfValidationPhaseEnded(cancel, pocStageStartBlockHeight) {
					return
				}
			}
		}
	}()
}

func (v *OffChainValidator) cancelIfValidationPhaseEnded(cancel context.CancelFunc, pocStageStartBlockHeight int64) bool {
	state := v.phaseTracker.GetCurrentEpochState()
	if !shouldStopValidationForStage(state, pocStageStartBlockHeight) {
		return false
	}

	logging.Info("OffChainValidator: validation phase ended, stopping workers", types.PoC,
		"currentPhase", state.CurrentPhase,
		"blockHeight", state.CurrentBlock.Height,
		"pocStageStartBlockHeight", pocStageStartBlockHeight)
	cancel()
	return true
}

func shouldStopValidationForStage(state *chainphase.EpochState, pocStageStartBlockHeight int64) bool {
	// A nil or not-synced tracker reading is transient (startup, RPC lag,
	// catch-up), not evidence that the validation window ended. Cancelling on
	// it would permanently abandon all in-flight validation for the stage, so
	// treat it as "wait for the next tick" and only stop on a synced reading
	// that positively says the phase ended or the stage changed.
	if state.IsNilOrNotSynced() {
		return false
	}
	return !ShouldAcceptValidatedArtifacts(state) || GetCurrentPocStageHeight(state) != pocStageStartBlockHeight
}

// worker processes participants from the work channel. Retryable failures go back
// into the channel rather than blocking the worker on their backoff.
func (v *OffChainValidator) worker(
	ctx context.Context,
	cancel context.CancelFunc,
	workerID int,
	workChan chan participantWork,
	proofClient proofFetcher,
	nodes []broker.NodeResponse,
	pocHeight int64,
	samplingBlockHash string,
	pocStartBlockHash string,
	pocParams *types.PocParams,
	sampleSize int,
	gr *guardRuntime,
	statsMu *sync.Mutex,
	dispatchedCount *int,
	failCount *int,
	abstainCount *int,
	pendingCount *int,
) {
	nodeCounter := workerID // Start from different nodes per worker

	for {
		select {
		case <-ctx.Done():
			return
		case work, ok := <-workChan:
			if !ok {
				return
			}

			// Not ready yet? Put back at end of queue and sleep briefly to avoid busy-wait spin.
			if time.Now().Before(work.retryAfter) {
				select {
				case workChan <- work:
				case <-ctx.Done():
					return
				}

				select {
				case <-time.After(100 * time.Millisecond):
				case <-ctx.Done():
					return
				}
				continue
			}

			result := v.validateParticipant(
				ctx,
				workerID,
				&work,
				proofClient,
				nodes,
				&nodeCounter,
				pocHeight,
				samplingBlockHash,
				pocStartBlockHash,
				pocParams,
				sampleSize,
				gr,
			)

			// Retryable results go back into the queue until the attempt budget is
			// spent, then settle as their class: validatee failures vote -1, local
			// failures abstain. A window that closed under the attempt makes the
			// failure ours whatever its cause, so it abstains too.
			if result == validateFailRetry || result == validateAbstainRetry {
				switch {
				case ctx.Err() != nil:
					logging.Warn("OffChainValidator: abstaining, validation window closed", types.PoC,
						"participant", work.address, "modelId", work.modelId, "attempts", work.attempt+1)
					result = validateAbstain

				case work.attempt < v.config.MaxRetries-1:
					work.attempt++
					delay := retryBackoffDelay(v.config.RetryBackoff, work.attempt-1)
					work.retryAfter = time.Now().Add(delay)
					select {
					case workChan <- work:
						logging.Debug("OffChainValidator: re-queued for retry", types.PoC,
							"participant", work.address, "attempt", work.attempt, "delay", delay)
						continue
					case <-ctx.Done():
						return
					}

				case result == validateAbstainRetry:
					logging.Warn("OffChainValidator: abstaining after local failure retry exhaustion", types.PoC,
						"participant", work.address, "modelId", work.modelId, "attempts", work.attempt+1)
					result = validateAbstain

				default:
					logging.Warn("OffChainValidator: max retries exceeded for transient validation failure", types.PoC,
						"participant", work.address, "attempts", work.attempt+1)
					result = validateFailPermanent
				}
			}

			var reportParticipant string
			var reportModelID string

			statsMu.Lock()
			switch result {
			case validateDispatched:
				*dispatchedCount++
			case validateFailPermanent:
				*failCount++
				reportParticipant = work.address
				reportModelID = work.modelId
			case validateAbstain:
				*abstainCount++
			}
			*pendingCount--

			done := *pendingCount <= 0
			statsMu.Unlock()

			if reportParticipant != "" {
				v.reportInvalidParticipant(pocHeight, reportParticipant, reportModelID)
			}

			if done {
				cancel()
				return
			}
		}
	}
}

// validateParticipant validates a single participant in two phases.
//
// checkValidateeProofs judges the validatee and runs once, its result cached on the
// work item. dispatchToMLNode is local work and can only abstain, so once the checks
// pass no local failure can turn the item into a -1.
//
// samplingBlockHash: fresh hash for random sampling (anti-cheat)
// pocStartBlockHash: original PoC start block hash (must match generation for MLNode)
func (v *OffChainValidator) validateParticipant(
	ctx context.Context,
	workerID int,
	work *participantWork,
	proofClient proofFetcher,
	nodes []broker.NodeResponse,
	nodeCounter *int,
	pocHeight int64,
	samplingBlockHash string,
	pocStartBlockHash string,
	pocParams *types.PocParams,
	sampleSize int,
	gr *guardRuntime,
) validateResult {
	logging.Debug("OffChainValidator: validating participant", types.PoC,
		"worker", workerID, "participant", work.address, "count", work.count,
		"attempt", work.attempt, "checksPassed", work.checksPassed)

	if !work.checksPassed {
		if failure, ok := v.checkValidateeProofs(ctx, work, proofClient, pocHeight, samplingBlockHash, sampleSize, gr); !ok {
			return failure
		}
	}

	return v.dispatchToMLNode(ctx, work, nodes, nodeCounter, pocHeight, pocStartBlockHash, pocParams)
}

// checkValidateeProofs checks proofs against the committed root, duplicate nonces,
// porosity and the early-share guard. This is the only phase that can vote -1, since
// only here do we see evidence about the validatee; failures on our own side abstain.
// It reports whether the validatee passed and, if not, the result the worker settles on.
func (v *OffChainValidator) checkValidateeProofs(
	ctx context.Context,
	work *participantWork,
	proofClient proofFetcher,
	pocHeight int64,
	samplingBlockHash string,
	sampleSize int,
	gr *guardRuntime,
) (failure validateResult, passed bool) {
	// Sample leaf indices using fresh hash (anti-cheat: prevents validators from predicting sample)
	leafIndices := sampleLeafIndices(v.pubKey, samplingBlockHash, pocHeight, work.modelId, work.count, sampleSize)

	verified, err := proofClient.FetchAndVerifyProofs(ctx, work.url, ProofRequest{
		PocStageStartBlockHeight: pocHeight,
		ModelId:                  work.modelId,
		RootHash:                 work.rootHash,
		Count:                    work.count,
		LeafIndices:              leafIndices,
		ParticipantAddress:       work.address,
	})
	if err != nil {
		logging.Warn("OffChainValidator: proof fetch/verify failed", types.PoC,
			"participant", work.address, "attempt", work.attempt, "error", err)
		// The validatee's fault, and retrying cannot change it.
		if errors.Is(err, ErrProofVerificationFailed) || errors.Is(err, ErrIncompleteCoverage) || errors.Is(err, ErrInvalidVectorData) {
			return validateFailPermanent, false
		}
		// We never sent a request, so we learned nothing about the validatee.
		if errors.Is(err, ErrLocalRequestFailure) {
			return validateAbstainRetry, false
		}
		// Transient on the validatee side (network, timeout).
		return validateFailRetry, false
	}

	// Check for duplicate nonces in response (defense-in-depth).
	// SMST proofs with index-binding structurally prevent cross-index duplication,
	// but this guards against a malformed response returning the same artifact twice.
	if err := CheckDuplicateNonces(verified); err != nil {
		logging.Warn("OffChainValidator: duplicate nonces detected (fraud)", types.PoC,
			"participant", work.address, "error", err)
		return validateFailPermanent, false
	}

	if maxNonce, porosity, invalid := isPorosityTooHigh(verified, work.count); invalid {
		logging.Warn("OffChainValidator: porosity too high", types.PoC,
			"participant", work.address,
			"maxNonce", maxNonce,
			"count", work.count,
			"porosity", porosity)
		return validateFailPermanent, false
	}

	// Early-share guard: compare the early checkpoint against the final
	// commitment. The inclusion check fails immediately on a cryptographic
	// mismatch; the low-early-share decision is miss-streak gated and was
	// precomputed in ValidateAll. In observe mode the guard only logs.
	if gr != nil && gr.guard != nil {
		if dec, ok := gr.decisions[earlyShareKey(work.address, work.modelId)]; ok {
			outcome, reason := gr.guard.decide(ctx, proofClient, gr.stage, *work, dec, gr.validatorPubKey, gr.samplingBlockHash)
			switch outcome {
			case earlyGuardVoteNo:
				if gr.guard.cfg.Enforcing() {
					logging.Warn("OffChainValidator: early-share guard vote no (enforce)", types.PoC,
						"participant", work.address, "modelId", work.modelId, "reason", reason)
					return validateFailPermanent, false
				}
				logging.Info("OffChainValidator: early-share guard would vote no (observe)", types.PoC,
					"participant", work.address, "modelId", work.modelId, "reason", reason)
			case earlyGuardRetry:
				if gr.guard.cfg.Enforcing() {
					logging.Warn("OffChainValidator: early-share guard retry (enforce)", types.PoC,
						"participant", work.address, "modelId", work.modelId, "reason", reason)
					return validateFailRetry, false
				}
				logging.Info("OffChainValidator: early-share guard would retry (observe)", types.PoC,
					"participant", work.address, "modelId", work.modelId, "reason", reason)
			case earlyGuardAbstainRetry:
				if gr.guard.cfg.Enforcing() {
					logging.Warn("OffChainValidator: early-share guard abstains after local failure (enforce)", types.PoC,
						"participant", work.address, "modelId", work.modelId, "reason", reason)
					return validateAbstainRetry, false
				}
				logging.Info("OffChainValidator: early-share guard would abstain after local failure (observe)", types.PoC,
					"participant", work.address, "modelId", work.modelId, "reason", reason)
			}
		}
	}

	work.verified = verified
	work.checksPassed = true
	return failure, true
}

// dispatchToMLNode hands the verified artifacts to a local ML node for statistical
// validation. Its verdict, including a -1 on detected fraud, arrives later on the
// /validated callback. Failures here are all on our side, so they abstain.
func (v *OffChainValidator) dispatchToMLNode(
	ctx context.Context,
	work *participantWork,
	nodes []broker.NodeResponse,
	nodeCounter *int,
	pocHeight int64,
	pocStartBlockHash string,
	pocParams *types.PocParams,
) validateResult {
	// Convert verified artifacts to ML node format
	artifacts := make([]mlnodeclient.ArtifactV2, len(work.verified))
	nonces := make([]int64, len(work.verified))
	for i, a := range work.verified {
		artifacts[i] = mlnodeclient.ArtifactV2{
			Nonce:     int64(a.Nonce),
			VectorB64: a.VectorB64,
		}
		nonces[i] = int64(a.Nonce)
	}

	// IMPORTANT: Use pocStartBlockHash (not samplingBlockHash) to match generation seed
	validationCallbackUrl := buildValidationCallbackURL(v.callbackUrl, work.modelId)
	// Commits are gated on governance models, not on PoC params, so a model can
	// legitimately be committed without a config here.
	modelConfig, ok := pocParams.GetModelConfig(work.modelId)
	if !ok {
		logging.Warn("OffChainValidator: abstaining, missing model config for validation work", types.PoC,
			"participant", work.address, "modelId", work.modelId)
		return validateAbstain
	}

	// nodes is a snapshot taken once per stage, so retrying cannot make an executor appear.
	modelNodes := filterValidationNodesForModel(nodes, work.modelId)
	if len(modelNodes) == 0 {
		logging.Info("OffChainValidator: abstaining, no validation executors for model", types.PoC,
			"participant", work.address, "modelId", work.modelId)
		return validateAbstain
	}

	validationReq := mlnodeclient.PoCGenerateRequestV2{
		BlockHash:   pocStartBlockHash, // Must match the hash used during generation
		BlockHeight: pocHeight,
		PublicKey:   work.pubKey,
		NodeCount:   len(modelNodes),
		Nonces:      nonces,
		Params: mlnodeclient.PoCParamsV2{
			Model:  modelConfig.ModelId,
			SeqLen: modelConfig.SeqLen,
		},
		URL: validationCallbackUrl,
		Validation: &mlnodeclient.ValidationV2{
			Artifacts: artifacts,
		},
		StatTest:       mlnodeclient.StatTestParamsFromChain(modelConfig.StatTest),
		PocStrongerRng: pocParams.PocStrongerRngEnabled,
	}

	// Try sending to ML node (single attempt per call - retries handled by queue)
	node := modelNodes[*nodeCounter%len(modelNodes)]
	*nodeCounter++

	validationReq.NodeId = int(node.Node.NodeNum)

	nodeClient := v.nodeBroker.NewNodeClient(&node.Node)
	_, err := nodeClient.GenerateV2(ctx, validationReq)
	if err == nil {
		logging.Debug("OffChainValidator: sent to ML node", types.PoC,
			"participant", work.address, "node", node.Node.Host)
		return validateDispatched
	}

	logging.Warn("OffChainValidator: ML node request failed", types.PoC,
		"participant", work.address, "node", node.Node.Host, "attempt", work.attempt, "error", err)
	return validateAbstainRetry
}

// sampleLeafIndices generates deterministic leaf indices using lazy Fisher-Yates.
// Important: count comes from on-chain commits and can be very large - must stay O(sampleSize).
func sampleLeafIndices(validatorPubKey string, blockHash string, blockHeight int64, modelId string, count uint32, sampleSize int) []uint32 {
	if count == 0 {
		return nil
	}

	n := int(count)
	if sampleSize <= 0 {
		return nil
	}
	if sampleSize >= n {
		indices := make([]uint32, n)
		for i := 0; i < n; i++ {
			indices[i] = uint32(i)
		}
		return indices
	}

	seedInput := fmt.Sprintf("%s:%s:%d:%s", validatorPubKey, blockHash, blockHeight, modelId)
	hash := sha256.Sum256([]byte(seedInput))
	seed := int64(binary.BigEndian.Uint64(hash[:8]))

	source := rand.NewSource(seed)
	rng := rand.New(source)

	// Lazy Fisher-Yates: track swaps instead of full array
	swaps := make(map[uint32]uint32, sampleSize*2)
	get := func(i uint32) uint32 {
		if v, ok := swaps[i]; ok {
			return v
		}
		return i
	}

	result := make([]uint32, sampleSize)
	for i := 0; i < sampleSize; i++ {
		j := i + rng.Intn(n-i)
		ii := uint32(i)
		jj := uint32(j)

		vi := get(ii)
		vj := get(jj)
		swaps[ii] = vj
		swaps[jj] = vi

		result[i] = vj
	}

	return result
}

// getSamplingBlockHash returns the block hash used as sampling randomness.
func (v *OffChainValidator) getSamplingBlockHash(epochState *chainphase.EpochState) string {
	if epochState.CurrentBlock.Hash != "" {
		return epochState.CurrentBlock.Hash
	}

	if epochState.CurrentPhase == types.InferencePhase && epochState.ActiveConfirmationPoCEvent != nil {
		return epochState.ActiveConfirmationPoCEvent.PocSeedBlockHash
	}

	if v.chainNodeUrl == "" {
		logging.Warn("OffChainValidator: no chain node URL", types.PoC)
		return ""
	}

	client, err := cosmosclient.NewRpcClient(v.chainNodeUrl)
	if err != nil {
		logging.Error("OffChainValidator: failed to create RPC client", types.PoC, "error", err)
		return ""
	}

	freshBlockHeight := epochState.CurrentBlock.Height
	if freshBlockHeight <= 0 {
		logging.Error("OffChainValidator: current block height not available", types.PoC)
		return ""
	}

	block, err := client.Block(context.Background(), &freshBlockHeight)
	if err != nil {
		logging.Error("OffChainValidator: failed to get block", types.PoC, "height", freshBlockHeight, "error", err)
		return ""
	}

	return block.Block.Hash().String()
}

func (v *OffChainValidator) stopGenerationOnAllNodes(nodes []broker.NodeResponse) {
	logging.Info("OffChainValidator: stopping generation on all nodes", types.PoC,
		"numNodes", len(nodes))

	ctx := context.Background()
	successCount := 0
	failCount := 0

	for _, node := range nodes {
		nodeClient := v.nodeBroker.NewNodeClient(&node.Node)
		_, err := nodeClient.StopPowV2(ctx)
		if err != nil {
			logging.Warn("OffChainValidator: StopPowV2 failed", types.PoC,
				"node", node.Node.Host, "error", err)
			failCount++
		} else {
			successCount++
		}
	}

	logging.Info("OffChainValidator: stop generation complete", types.PoC,
		"success", successCount, "failed", failCount)
}

// getNodesWithRetry waits for nodes to become available for PoC validation,
// retrying up to POC_VALIDATE_GET_NODES_RETRIES times.
func (v *OffChainValidator) getNodesWithRetry(pocStageStartBlockHeight int64) ([]broker.NodeResponse, error) {
	return v.getNodesWithRetryConfig(
		pocStageStartBlockHeight,
		POC_VALIDATE_GET_NODES_RETRIES,
		POC_VALIDATE_GET_NODES_RETRY_DELAY,
	)
}

// getNodesWithRetryConfig allows tests to supply custom retry settings.
func (v *OffChainValidator) getNodesWithRetryConfig(
	pocStageStartBlockHeight int64,
	retries int,
	delay time.Duration,
) ([]broker.NodeResponse, error) {
	if retries <= 0 {
		retries = 1
	}

	for attempt := 0; attempt < retries; attempt++ {
		nodes, err := v.nodeBroker.GetNodes()
		if err != nil {
			logging.Error("OffChainValidator: failed to get nodes", types.PoC,
				"pocStageStartBlockHeight", pocStageStartBlockHeight,
				"error", err,
				"attempt", attempt)
			return nil, err
		}

		logging.Info("OffChainValidator: got nodes", types.PoC,
			"pocStageStartBlockHeight", pocStageStartBlockHeight,
			"numNodes", len(nodes),
			"attempt", attempt)

		epochState := v.phaseTracker.GetCurrentEpochState()
		if epochState == nil {
			logging.Error("OffChainValidator: epoch state is nil during node filtering", types.PoC,
				"pocStageStartBlockHeight", pocStageStartBlockHeight,
				"attempt", attempt)
			return nil, errors.New("epoch state is nil during node filtering")
		}

		nodes = filterNodesForValidation(nodes, epochState.LatestEpoch.EpochIndex, epochState.CurrentPhase)
		logging.Info("OffChainValidator: filtered nodes for validation", types.PoC,
			"numNodes", len(nodes),
			"attempt", attempt)

		if len(nodes) != 0 {
			logging.Info("OffChainValidator: returning filtered nodes", types.PoC,
				"numNodes", len(nodes),
				"attempt", attempt)
			return nodes, nil
		}

		if attempt == retries-1 {
			break
		}
		time.Sleep(delay)
	}

	logging.Error("OffChainValidator: failed to get nodes after all retry attempts", types.PoC,
		"pocStageStartBlockHeight", pocStageStartBlockHeight,
		"numAttempts", retries)
	return nil, errors.New("no nodes available for PoC validation after retries")
}

// filterNodesForValidation returns nodes available for PoC validation.
// - Accept nodes in POC status with any sub-status
// - Accept nodes in INFERENCE status (unless preserved for inference via POC_SLOT)
// - Exclude FAILED, nodes that are not operational for the current epoch/phase, or POC_SLOT-preserved nodes
func filterNodesForValidation(nodes []broker.NodeResponse, latestEpoch uint64, currentPhase types.EpochPhase) []broker.NodeResponse {
	filtered := make([]broker.NodeResponse, 0, len(nodes))
	for _, node := range nodes {
		// Exclude failed nodes
		if node.State.CurrentStatus == types.HardwareNodeStatus_FAILED {
			logging.Debug("filterNodesForValidation: Skipping FAILED node", types.PoC, "node_id", node.Node.Id)
			continue
		}

		// Exclude unknown status nodes
		if node.State.CurrentStatus == types.HardwareNodeStatus_UNKNOWN {
			logging.Debug("filterNodesForValidation: Skipping UNKNOWN node", types.PoC, "node_id", node.Node.Id)
			continue
		}

		// Exclude nodes that are not operational for the current epoch/phase.
		if !node.State.ShouldBeOperational(latestEpoch, currentPhase) {
			logging.Debug("filterNodesForValidation: Skipping non-operational node", types.PoC,
				"node_id", node.Node.Id,
				"latest_epoch", latestEpoch,
				"current_phase", currentPhase,
				"admin_state", node.State.AdminState)
			continue
		}

		// Exclude nodes preserved for inference (POC_SLOT allocation)
		if node.State.ShouldContinueInference() {
			logging.Debug("filterNodesForValidation: Skipping node preserved for inference", types.PoC, "node_id", node.Node.Id)
			continue
		}

		// Accept nodes in POC status (any sub-status)
		if node.State.CurrentStatus == types.HardwareNodeStatus_POC {
			filtered = append(filtered, node)
			continue
		}

		// Accept nodes in INFERENCE status
		if node.State.CurrentStatus == types.HardwareNodeStatus_INFERENCE {
			filtered = append(filtered, node)
			continue
		}

		logging.Debug("filterNodesForValidation: Skipping node with status", types.PoC,
			"node_id", node.Node.Id, "status", node.State.CurrentStatus.String())
	}
	return filtered
}

func filterValidationNodesForModel(nodes []broker.NodeResponse, modelID string) []broker.NodeResponse {
	filtered := make([]broker.NodeResponse, 0, len(nodes))
	for _, node := range nodes {
		if len(node.State.EpochMLNodes) > 0 {
			if _, ok := node.State.EpochMLNodes[modelID]; ok {
				filtered = append(filtered, node)
			}
			continue
		}
		// No epoch assignment yet (first epoch or node just joined).
		// Fall back to first node-supported model.
		nodeModelID, ok := broker.ResolveNodeModelID(node.State.EpochMLNodes, node.Node.Models)
		if ok && nodeModelID == modelID {
			filtered = append(filtered, node)
		}
	}
	return filtered
}

// reportInvalidParticipant votes ValidatedWeight=-1 on chain. Reserved for failures
// the validatee is responsible for; anything on our side abstains instead.
func (v *OffChainValidator) reportInvalidParticipant(pocHeight int64, participantAddress, modelID string) {
	msg := &types.MsgSubmitPocValidationsV2{
		PocStageStartBlockHeight: pocHeight,
		Validations: []*types.PoCValidationEntryV2{
			{
				ParticipantAddress: participantAddress,
				ModelId:            modelID,
				ValidatedWeight:    -1, // Invalid
			},
		},
	}
	if err := v.recorder.SubmitPocValidationsV2(msg); err != nil {
		logging.Error("OffChainValidator: failed to report invalid participant", types.PoC,
			"participant", participantAddress, "error", err)
	} else {
		logging.Info("OffChainValidator: reported participant as invalid", types.PoC,
			"participant", participantAddress)
	}
}
