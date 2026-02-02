package poc

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"

	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
)

// OffChainValidator handles off-chain PoC validation using MMR proofs.
type OffChainValidator struct {
	recorder     cosmosclient.CosmosMessageClient
	nodeBroker   *broker.Broker
	phaseTracker *chainphase.ChainPhaseTracker
	callbackUrl  string
	pubKey       string
	chainNodeUrl string

	config ValidationConfig
}

// ValidationConfig contains configuration for off-chain validation.
type ValidationConfig struct {
	WorkerCount    int
	RequestTimeout time.Duration
	MaxRetries     int
	RetryBackoff   time.Duration
}

// DefaultValidationConfig returns the default configuration.
func DefaultValidationConfig() ValidationConfig {
	return ValidationConfig{
		WorkerCount:    10,
		RequestTimeout: 20 * time.Second,
		MaxRetries:     15,
		RetryBackoff:   3 * time.Second,
	}
}

// validateResult represents the outcome of validating a participant.
type validateResult int

const (
	validateSuccess       validateResult = iota // Validation succeeded
	validateFailPermanent                       // Permanent failure (fraud, invalid proof) - no retry
	validateFailRetry                           // Transient failure (network, ML node) - can retry
)

// participantWork represents a single participant to validate.
type participantWork struct {
	address    string
	url        string
	pubKey     string
	count      uint32
	rootHash   []byte
	attempt    int       // current attempt number (0-based)
	retryAfter time.Time // don't process before this time
}

// NewOffChainValidator creates a new off-chain validator.
func NewOffChainValidator(
	recorder cosmosclient.CosmosMessageClient,
	nodeBroker *broker.Broker,
	phaseTracker *chainphase.ChainPhaseTracker,
	callbackUrl string,
	pubKey string,
	chainNodeUrl string,
	config ValidationConfig,
) *OffChainValidator {
	return &OffChainValidator{
		recorder:     recorder,
		nodeBroker:   nodeBroker,
		phaseTracker: phaseTracker,
		callbackUrl:  callbackUrl,
		pubKey:       pubKey,
		chainNodeUrl: chainNodeUrl,
		config:       config,
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

	// Get PoC params
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

	// Get available ML nodes for validation with retry
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

	// Stop generation on all nodes before validation
	v.stopGenerationOnAllNodes(nodes)

	// Query all store commits for this stage
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

	// Build work items with participant URLs
	workItems := make([]participantWork, 0, len(commitsResp.Commits))
	for _, commit := range commitsResp.Commits {
		// Get participant's inference URL
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

		// Get participant's public key for ML node (from commit query)
		if commit.HexPubKey == "" {
			logging.Warn("OffChainValidator: participant has no public key", types.PoC,
				"address", commit.ParticipantAddress)
			continue
		}

		workItems = append(workItems, participantWork{
			address:  commit.ParticipantAddress,
			url:      participantResp.Participant.InferenceUrl,
			pubKey:   commit.HexPubKey,
			count:    commit.Count,
			rootHash: commit.RootHash,
		})
	}

	if len(workItems) == 0 {
		logging.Warn("OffChainValidator: no valid work items", types.PoC)
		return
	}

	// Randomize order to avoid thundering herd
	rand.Shuffle(len(workItems), func(i, j int) {
		workItems[i], workItems[j] = workItems[j], workItems[i]
	})

	// Create proof client
	proofClient := NewProofClient(v.recorder, ProofClientConfig{Timeout: v.config.RequestTimeout})

	// Create work channel - buffered to allow re-queueing failed items
	// Size: initial items + potential retries
	workChan := make(chan participantWork, len(workItems)*2)
	var wg sync.WaitGroup

	// Track statistics
	var statsMu sync.Mutex
	successCount := 0
	failCount := 0
	pendingCount := len(workItems)

	// Context for coordinating shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start workers
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
				&statsMu,
				&successCount,
				&failCount,
				&pendingCount,
			)
		}(i)
	}

	// Send initial work items
	for _, item := range workItems {
		workChan <- item
	}

	// Wait for all workers to complete
	wg.Wait()
	close(workChan)

	logging.Info("OffChainValidator: validation complete", types.PoC,
		"pocStageStartBlockHeight", pocStageStartBlockHeight,
		"totalParticipants", len(workItems),
		"successful", successCount,
		"failed", failCount)
}

// worker processes participants from the work channel.
// Failed items are re-queued for retry instead of blocking on retries.
func (v *OffChainValidator) worker(
	ctx context.Context,
	cancel context.CancelFunc,
	workerID int,
	workChan chan participantWork,
	proofClient *ProofClient,
	nodes []broker.NodeResponse,
	pocHeight int64,
	samplingBlockHash string,
	pocStartBlockHash string,
	pocParams *types.PocParams,
	sampleSize int,
	statsMu *sync.Mutex,
	successCount *int,
	failCount *int,
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

			// Not ready yet? Put back at end of queue
			if time.Now().Before(work.retryAfter) {
				workChan <- work
				continue
			}

			result := v.validateParticipant(
				workerID,
				work,
				proofClient,
				nodes,
				&nodeCounter,
				pocHeight,
				samplingBlockHash,
				pocStartBlockHash,
				pocParams,
				sampleSize,
			)

			var reportAddr string

			statsMu.Lock()
			switch result {
			case validateSuccess:
				*successCount++
				*pendingCount--
			case validateFailPermanent:
				*failCount++
				*pendingCount--
				// Report participant as invalid to chain
				// Uncomment when stabilized
				// reportAddr = work.address
			case validateFailRetry:
				// Re-queue for retry if under max attempts
				if work.attempt < v.config.MaxRetries-1 {
					work.attempt++
					work.retryAfter = time.Now().Add(v.config.RetryBackoff)
					// Non-blocking send - if channel is full, count as failed
					select {
					case workChan <- work:
						logging.Debug("OffChainValidator: re-queued for retry", types.PoC,
							"participant", work.address, "attempt", work.attempt)
					default:
						*failCount++
						*pendingCount--
						logging.Warn("OffChainValidator: queue full, marking as failed", types.PoC,
							"participant", work.address)
					}
				} else {
					*failCount++
					*pendingCount--
					logging.Warn("OffChainValidator: max retries exceeded, reporting as invalid", types.PoC,
						"participant", work.address, "attempts", work.attempt+1)
					// Report participant as invalid to chain. We probably should separate only to report failed network requests.
					// reportAddr = work.address
				}
			}

			done := *pendingCount <= 0
			statsMu.Unlock()

			if reportAddr != "" {
				v.reportInvalidParticipant(pocHeight, reportAddr)
			}

			if done {
				cancel()
				return
			}
		}
	}
}

// validateParticipant validates a single participant.
// Returns validateResult indicating success, permanent failure, or retryable failure.
// samplingBlockHash: fresh hash for random sampling (anti-cheat)
// pocStartBlockHash: original PoC start block hash (must match generation for MLNode)
func (v *OffChainValidator) validateParticipant(
	workerID int,
	work participantWork,
	proofClient *ProofClient,
	nodes []broker.NodeResponse,
	nodeCounter *int,
	pocHeight int64,
	samplingBlockHash string,
	pocStartBlockHash string,
	pocParams *types.PocParams,
	sampleSize int,
) validateResult {
	ctx := context.Background()

	logging.Debug("OffChainValidator: validating participant", types.PoC,
		"worker", workerID, "participant", work.address, "count", work.count, "attempt", work.attempt)

	// Sample leaf indices using fresh hash (anti-cheat: prevents validators from predicting sample)
	leafIndices := sampleLeafIndices(v.pubKey, samplingBlockHash, pocHeight, work.count, sampleSize)

	// Fetch and verify proofs
	verified, err := proofClient.FetchAndVerifyProofs(ctx, work.url, ProofRequest{
		PocStageStartBlockHeight: pocHeight,
		RootHash:                 work.rootHash,
		Count:                    work.count,
		LeafIndices:              leafIndices,
		ParticipantAddress:       work.address,
	})
	if err != nil {
		logging.Warn("OffChainValidator: proof fetch/verify failed", types.PoC,
			"participant", work.address, "attempt", work.attempt, "error", err)
		// Proof verification failures and incomplete coverage are permanent - no point retrying
		if errors.Is(err, ErrProofVerificationFailed) || errors.Is(err, ErrIncompleteCoverage) {
			return validateFailPermanent
		}
		// Transient error (network/timeout) - retry
		return validateFailRetry
	}

	// Check for duplicate nonces (fraud) - permanent failure
	if err := CheckDuplicateNonces(verified); err != nil {
		logging.Warn("OffChainValidator: duplicate nonces detected (fraud)", types.PoC,
			"participant", work.address, "error", err)
		return validateFailPermanent
	}

	// Convert verified artifacts to ML node format
	artifacts := make([]mlnodeclient.ArtifactV2, len(verified))
	nonces := make([]int64, len(verified))
	for i, a := range verified {
		artifacts[i] = mlnodeclient.ArtifactV2{
			Nonce:     int64(a.Nonce),
			VectorB64: a.VectorB64,
		}
		nonces[i] = int64(a.Nonce)
	}

	// Send to ML node for statistical validation
	// IMPORTANT: Use pocStartBlockHash (not samplingBlockHash) to match generation seed
	validationCallbackUrl := v.callbackUrl + "/v2/poc-batches"
	validationReq := mlnodeclient.PoCGenerateRequestV2{
		BlockHash:   pocStartBlockHash, // Must match the hash used during generation
		BlockHeight: pocHeight,
		PublicKey:   work.pubKey,
		NodeCount:   len(nodes),
		Nonces:      nonces,
		Params: mlnodeclient.PoCParamsV2{
			Model:  pocParams.ModelId,
			SeqLen: pocParams.SeqLen,
		},
		URL: validationCallbackUrl,
		Validation: &mlnodeclient.ValidationV2{
			Artifacts: artifacts,
		},
		StatTest: mlnodeclient.DefaultStatTestParamsV2(),
	}

	// Try sending to ML node (single attempt per call - retries handled by queue)
	node := nodes[*nodeCounter%len(nodes)]
	*nodeCounter++

	validationReq.NodeId = int(node.Node.NodeNum)

	nodeClient := v.nodeBroker.NewNodeClient(&node.Node)
	_, err = nodeClient.GenerateV2(ctx, validationReq)
	if err == nil {
		logging.Debug("OffChainValidator: sent to ML node", types.PoC,
			"participant", work.address, "node", node.Node.Host)
		return validateSuccess
	}

	logging.Warn("OffChainValidator: ML node request failed", types.PoC,
		"participant", work.address, "node", node.Node.Host, "attempt", work.attempt, "error", err)
	return validateFailRetry
}

// sampleLeafIndices generates deterministic leaf indices using lazy Fisher-Yates.
// Important: count comes from on-chain commits and can be very large - must stay O(sampleSize).
func sampleLeafIndices(validatorPubKey string, blockHash string, blockHeight int64, count uint32, sampleSize int) []uint32 {
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

	seedInput := fmt.Sprintf("%s:%s:%d", validatorPubKey, blockHash, blockHeight)
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

// getBlockHash returns the block hash for sampling randomness.
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

// getNodesWithRetry gets nodes for PoC validation with retry logic.
// Waits for nodes to become available with up to 30 retries.
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

		nodes = filterNodesForValidation(nodes)
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
// - Accept nodes in INFERENCE status
// - Exclude FAILED or administratively disabled nodes
func filterNodesForValidation(nodes []broker.NodeResponse) []broker.NodeResponse {
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

		// Exclude administratively disabled nodes
		if !node.State.AdminState.Enabled {
			logging.Debug("filterNodesForValidation: Skipping administratively disabled node", types.PoC, "node_id", node.Node.Id)
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

// reportInvalidParticipant submits a validation result with ValidatedWeight=-1 (invalid) to chain.
// This is called when validation fails permanently (e.g., retry exhaustion).
func (v *OffChainValidator) reportInvalidParticipant(pocHeight int64, participantAddress string) {
	msg := &inference.MsgSubmitPocValidationsV2{
		PocStageStartBlockHeight: pocHeight,
		Validations: []*inference.PoCValidationPayloadV2{
			{
				ParticipantAddress: participantAddress,
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
