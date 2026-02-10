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

	"github.com/productscience/inference/x/inference/types"
)

const (
	POC_VALIDATE_GET_NODES_RETRIES     = 30
	POC_VALIDATE_GET_NODES_RETRY_DELAY = 5 * time.Second
	POC_VALIDATE_BATCH_RETRIES         = 5
)

type OnChainValidator struct {
	recorder         cosmosclient.CosmosMessageClient
	nodeBroker       *broker.Broker
	phaseTracker     *chainphase.ChainPhaseTracker
	callbackUrl      string
	pubKey           string
	validatorAddress string
	chainNodeUrl     string

	config ValidationConfig
}

func NewOnChainValidator(
	recorder cosmosclient.CosmosMessageClient,
	nodeBroker *broker.Broker,
	phaseTracker *chainphase.ChainPhaseTracker,
	callbackUrl string,
	pubKey string,
	validatorAddress string,
	chainNodeUrl string,
	config ValidationConfig,
) *OnChainValidator {
	return &OnChainValidator{
		recorder:         recorder,
		nodeBroker:       nodeBroker,
		phaseTracker:     phaseTracker,
		callbackUrl:      callbackUrl,
		pubKey:           pubKey,
		validatorAddress: validatorAddress,
		chainNodeUrl:     chainNodeUrl,
		config:           config,
	}
}

func (v *OnChainValidator) ValidateAll(pocStageStartBlockHeight int64, pocStartBlockHash string) {
	logging.Info("OnChainValidator: starting V1 validation", types.PoC,
		"pocStageStartBlockHeight", pocStageStartBlockHeight,
		"pocStartBlockHash", pocStartBlockHash)

	if pocStartBlockHash == "" {
		logging.Error("OnChainValidator: pocStartBlockHash is empty", types.PoC)
		return
	}

	epochState := v.phaseTracker.GetCurrentEpochState()
	if epochState == nil {
		logging.Error("OnChainValidator: epoch state is nil", types.PoC)
		return
	}

	samplingBlockHash := v.getSamplingBlockHash(epochState)
	if samplingBlockHash == "" {
		logging.Error("OnChainValidator: failed to get sampling block hash", types.PoC)
		return
	}

	// Get PoC params
	queryClient := v.recorder.NewInferenceQueryClient()
	paramsResp, err := queryClient.Params(context.Background(), &types.QueryParamsRequest{})
	if err != nil {
		logging.Error("OnChainValidator: failed to get params", types.PoC, "error", err)
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
		logging.Error("OnChainValidator: failed to get nodes for validation", types.PoC,
			"pocStageStartBlockHeight", pocStageStartBlockHeight, "error", err)
		return
	}
	if len(nodes) == 0 {
		logging.Error("OnChainValidator: no nodes available", types.PoC)
		return
	}

	// Query all PoC batches for this stage from chain
	batchesResp, err := queryClient.PocBatchesForStage(context.Background(),
		&types.QueryPocBatchesForStageRequest{
			BlockHeight: pocStageStartBlockHeight,
		})
	if err != nil {
		logging.Error("OnChainValidator: failed to query batches", types.PoC, "error", err)
		return
	}

	if len(batchesResp.PocBatch) == 0 {
		logging.Info("OnChainValidator: no batches found for stage", types.PoC,
			"pocStageStartBlockHeight", pocStageStartBlockHeight)
		return
	}

	logging.Info("OnChainValidator: found participants with batches", types.PoC,
		"count", len(batchesResp.PocBatch))

	// Build work items from batches
	workItems := make([]v1ValidateWork, 0)
	for _, participantBatches := range batchesResp.PocBatch {
		// Collect all nonces and distances from all batches for this participant
		// Use uniqueNonces map to deduplicate - prevents malicious inflation of work count
		allNonces := make([]int64, 0)
		allDist := make([]float64, 0)
		uniqueNonces := make(map[int64]struct{})

		for _, batch := range participantBatches.PocBatch {
			// Validate length match - skip malformed batches
			if len(batch.Nonces) != len(batch.Dist) {
				logging.Warn("OnChainValidator: skipping batch with length mismatch", types.PoC,
					"participant", participantBatches.Participant,
					"noncesLen", len(batch.Nonces),
					"distLen", len(batch.Dist))
				continue
			}

			for i, nonce := range batch.Nonces {
				if _, exists := uniqueNonces[nonce]; !exists {
					uniqueNonces[nonce] = struct{}{}
					allNonces = append(allNonces, nonce)
					allDist = append(allDist, batch.Dist[i])
				} else {
					logging.Debug("OnChainValidator: duplicate nonce found", types.PoC,
						"participant", participantBatches.Participant,
						"nonce", nonce)
				}
			}
		}

		if len(allNonces) == 0 {
			continue
		}

		workItems = append(workItems, v1ValidateWork{
			participantAddress: participantBatches.Participant,
			hexPubKey:          participantBatches.HexPubKey,
			nonces:             allNonces,
			dist:               allDist,
			blockHeight:        pocStageStartBlockHeight,
			pocStartBlockHash:  pocStartBlockHash,
			samplingBlockHash:  samplingBlockHash,
		})
	}

	if len(workItems) == 0 {
		logging.Warn("OnChainValidator: no valid work items", types.PoC)
		return
	}

	// Randomize order
	rand.Shuffle(len(workItems), func(i, j int) {
		workItems[i], workItems[j] = workItems[j], workItems[i]
	})

	// Process work items with workers
	workChan := make(chan v1ValidateWork, len(workItems))
	var wg sync.WaitGroup

	var statsMu sync.Mutex
	successCount := 0
	failCount := 0

	numWorkers := v.config.WorkerCount
	if numWorkers > len(workItems) {
		numWorkers = len(workItems)
	}

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			v.v1Worker(
				workerID,
				workChan,
				nodes,
				pocParams,
				sampleSize,
				&statsMu,
				&successCount,
				&failCount,
			)
		}(i)
	}

	// Send work items
	for _, item := range workItems {
		workChan <- item
	}
	close(workChan)

	wg.Wait()

	logging.Info("OnChainValidator: validation complete", types.PoC,
		"pocStageStartBlockHeight", pocStageStartBlockHeight,
		"totalParticipants", len(workItems),
		"successful", successCount,
		"failed", failCount)
}

type v1ValidateWork struct {
	participantAddress string
	hexPubKey          string
	nonces             []int64
	dist               []float64
	blockHeight        int64
	pocStartBlockHash  string
	samplingBlockHash  string
}

func (v *OnChainValidator) v1Worker(
	workerID int,
	workChan <-chan v1ValidateWork,
	nodes []broker.NodeResponse,
	pocParams *types.PocParams,
	sampleSize int,
	statsMu *sync.Mutex,
	successCount *int,
	failCount *int,
) {
	ctx := context.Background()
	nodeCounter := workerID

	for work := range workChan {
		logging.Debug("OnChainValidator: validating participant", types.PoC,
			"worker", workerID, "participant", work.participantAddress, "nonces", len(work.nonces))

		sampledBatch := sampleNoncesV1(
			v.pubKey,
			work.samplingBlockHash,
			work.blockHeight,
			work.nonces,
			work.dist,
			int64(sampleSize),
		)

		batch := mlnodeclient.ProofBatchV1{
			PublicKey:   work.hexPubKey,
			BlockHash:   work.pocStartBlockHash,
			BlockHeight: work.blockHeight,
			Nonces:      sampledBatch.nonces,
			Dist:        sampledBatch.dist,
		}

		// Send to ML node with retry
		// Extend retries if more nodes available to maximize success probability
		validationSucceeded := false
		retries := POC_VALIDATE_BATCH_RETRIES
		if len(nodes) > retries {
			retries = len(nodes)
		}
		for attempt := 0; attempt < retries; attempt++ {
			node := nodes[nodeCounter%len(nodes)]
			nodeCounter++

			batch.NodeNum = node.Node.NodeNum

			logging.Info("OnChainValidator: sending batch for validation", types.PoC,
				"attempt", attempt,
				"participant", work.participantAddress,
				"node", node.Node.Host,
				"nonces", len(sampledBatch.nonces))

			nodeClient := v.nodeBroker.NewNodeClient(&node.Node)
			err := nodeClient.ValidateBatchV1(ctx, batch)
			if err != nil {
				logging.Warn("OnChainValidator: ValidateBatchV1 failed", types.PoC,
					"participant", work.participantAddress,
					"node", node.Node.Host,
					"attempt", attempt,
					"error", err)
				continue
			}

			logging.Debug("OnChainValidator: sent to ML node", types.PoC,
				"participant", work.participantAddress, "node", node.Node.Host)
			validationSucceeded = true
			break
		}

		statsMu.Lock()
		if validationSucceeded {
			*successCount++
		} else {
			logging.Error("OnChainValidator: failed to validate batch after all retry attempts", types.PoC,
				"participant", work.participantAddress,
				"maxAttempts", retries)
			*failCount++
		}
		statsMu.Unlock()
	}
}

type sampledBatch struct {
	nonces []int64
	dist   []float64
}

func sampleNoncesV1(validatorPubKey, samplingBlockHash string, blockHeight int64, nonces []int64, dist []float64, sampleSize int64) sampledBatch {
	totalNonces := int64(len(nonces))
	if sampleSize >= totalNonces {
		return sampledBatch{nonces: nonces, dist: dist}
	}

	indices := deterministicSampleIndicesV1(
		validatorPubKey,
		samplingBlockHash,
		blockHeight,
		sampleSize,
		totalNonces,
	)

	sampledNonces := make([]int64, sampleSize)
	sampledDist := make([]float64, sampleSize)

	for i, idx := range indices {
		sampledNonces[i] = nonces[idx]
		if idx < len(dist) {
			sampledDist[i] = dist[idx]
		}
	}

	return sampledBatch{nonces: sampledNonces, dist: sampledDist}
}

func deterministicSampleIndicesV1(validatorPubKey, samplingBlockHash string, blockHeight, nSamples, totalItems int64) []int {
	if nSamples >= totalItems {
		indices := make([]int, totalItems)
		for i := int64(0); i < totalItems; i++ {
			indices[i] = int(i)
		}
		return indices
	}

	seedInput := fmt.Sprintf("%s:%s:%d", validatorPubKey, samplingBlockHash, blockHeight)
	hash := sha256.Sum256([]byte(seedInput))
	seed := int64(binary.BigEndian.Uint64(hash[:8]))

	source := rand.NewSource(seed)
	rng := rand.New(source)
	indices := rng.Perm(int(totalItems))[:nSamples]

	return indices
}

func (v *OnChainValidator) getSamplingBlockHash(epochState *chainphase.EpochState) string {
	if epochState.CurrentBlock.Hash != "" {
		return epochState.CurrentBlock.Hash
	}

	if epochState.CurrentPhase == types.InferencePhase && epochState.ActiveConfirmationPoCEvent != nil {
		return epochState.ActiveConfirmationPoCEvent.PocSeedBlockHash
	}

	if v.chainNodeUrl == "" {
		logging.Warn("OnChainValidator: no chain node URL", types.PoC)
		return ""
	}

	client, err := cosmosclient.NewRpcClient(v.chainNodeUrl)
	if err != nil {
		logging.Error("OnChainValidator: failed to create RPC client", types.PoC, "error", err)
		return ""
	}

	freshBlockHeight := epochState.CurrentBlock.Height
	if freshBlockHeight <= 0 {
		logging.Error("OnChainValidator: current block height not available", types.PoC)
		return ""
	}

	block, err := client.Block(context.Background(), &freshBlockHeight)
	if err != nil {
		logging.Error("OnChainValidator: failed to get block", types.PoC, "height", freshBlockHeight, "error", err)
		return ""
	}

	return block.Block.Hash().String()
}

func (v *OnChainValidator) getNodesWithRetry(pocStageStartBlockHeight int64) ([]broker.NodeResponse, error) {
	return v.getNodesWithRetryConfig(
		pocStageStartBlockHeight,
		POC_VALIDATE_GET_NODES_RETRIES,
		POC_VALIDATE_GET_NODES_RETRY_DELAY,
	)
}

func (v *OnChainValidator) getNodesWithRetryConfig(
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
			logging.Error("OnChainValidator: failed to get nodes", types.PoC,
				"pocStageStartBlockHeight", pocStageStartBlockHeight,
				"error", err,
				"attempt", attempt)
			return nil, err
		}

		logging.Info("OnChainValidator: got nodes", types.PoC,
			"pocStageStartBlockHeight", pocStageStartBlockHeight,
			"numNodes", len(nodes),
			"attempt", attempt)

		nodes = filterNodesForV1Validation(nodes)
		logging.Info("OnChainValidator: filtered nodes for validation", types.PoC,
			"numNodes", len(nodes),
			"attempt", attempt)

		if len(nodes) != 0 {
			logging.Info("OnChainValidator: returning filtered nodes", types.PoC,
				"numNodes", len(nodes),
				"attempt", attempt)
			return nodes, nil
		}

		if attempt == retries-1 {
			break
		}
		time.Sleep(delay)
	}

	logging.Error("OnChainValidator: failed to get nodes after all retry attempts", types.PoC,
		"pocStageStartBlockHeight", pocStageStartBlockHeight,
		"numAttempts", retries)
	return nil, errors.New("no nodes available for PoC validation after retries")
}

func filterNodesForV1Validation(nodes []broker.NodeResponse) []broker.NodeResponse {
	filtered := make([]broker.NodeResponse, 0, len(nodes))
	for _, node := range nodes {
		if node.State.CurrentStatus == types.HardwareNodeStatus_POC &&
			node.State.PocCurrentStatus == broker.PocStatusValidating {
			filtered = append(filtered, node)
		}
	}
	return filtered
}
