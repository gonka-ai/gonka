package poc

import (
	"context"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"

	"github.com/productscience/inference/x/inference/types"
)

const (
	POC_VALIDATE_BATCH_RETRIES     = 5
	POC_VALIDATE_SAMPLES_PER_BATCH = 200
)

type NodePoCOrchestrator interface {
	ValidateReceivedBatches(pocStageStartBlockHeight int64)
}

type NodePoCOrchestratorImpl struct {
	pubKey       string
	nodeBroker   *broker.Broker
	callbackUrl  string
	chainBridge  OrchestratorChainBridge
	phaseTracker *chainphase.ChainPhaseTracker
}

type OrchestratorChainBridge interface {
	PoCBatchesForStage(startPoCBlockHeight int64) (*types.QueryPocBatchesForStageResponse, error)
	GetBlockHash(height int64) (string, error)
	GetPocParams() (*types.PocParams, error)
}

type OrchestratorChainBridgeImpl struct {
	cosmosClient cosmos_client.CosmosMessageClient
	chainNodeUrl string
}

func (b *OrchestratorChainBridgeImpl) PoCBatchesForStage(startPoCBlockHeight int64) (*types.QueryPocBatchesForStageResponse, error) {
	response, err := b.cosmosClient.NewInferenceQueryClient().PocBatchesForStage(b.cosmosClient.GetContext(), &types.QueryPocBatchesForStageRequest{BlockHeight: startPoCBlockHeight})
	if err != nil {
		logging.Error("Failed to query PoC batches for stage", types.PoC, "error", err)
		return nil, err
	}
	return response, nil
}

func (b *OrchestratorChainBridgeImpl) GetPocParams() (*types.PocParams, error) {
	response, err := b.cosmosClient.NewInferenceQueryClient().Params(b.cosmosClient.GetContext(), &types.QueryParamsRequest{})
	if err != nil {
		logging.Error("Failed to query params", types.PoC, "error", err)
		return nil, err
	}
	pocParams := response.Params.PocParams
	return pocParams, nil
}

func (b *OrchestratorChainBridgeImpl) GetBlockHash(height int64) (string, error) {
	client, err := cosmos_client.NewRpcClient(b.chainNodeUrl)
	if err != nil {
		return "", err
	}

	block, err := client.Block(context.Background(), &height)
	if err != nil {
		return "", err
	}

	return block.Block.Hash().String(), err
}

func NewNodePoCOrchestratorForCosmosChain(pubKey string, nodeBroker *broker.Broker, callbackUrl string, chainNodeUrl string, cosmosClient cosmos_client.CosmosMessageClient, phaseTracker *chainphase.ChainPhaseTracker) NodePoCOrchestrator {
	return &NodePoCOrchestratorImpl{
		pubKey:      pubKey,
		nodeBroker:  nodeBroker,
		callbackUrl: callbackUrl,
		chainBridge: &OrchestratorChainBridgeImpl{
			cosmosClient: cosmosClient,
			chainNodeUrl: chainNodeUrl,
		},
		phaseTracker: phaseTracker,
	}
}

func NewNodePoCOrchestrator(pubKey string, nodeBroker *broker.Broker, callbackUrl string, chainBridge OrchestratorChainBridge, phaseTracker *chainphase.ChainPhaseTracker) NodePoCOrchestrator {
	return &NodePoCOrchestratorImpl{
		pubKey:       pubKey,
		nodeBroker:   nodeBroker,
		callbackUrl:  callbackUrl,
		chainBridge:  chainBridge,
		phaseTracker: phaseTracker,
	}
}

func (o *NodePoCOrchestratorImpl) ValidateReceivedBatches(pocStageStartBlockHeight int64) {
	logging.Info("ValidateReceivedBatches. Starting.", types.PoC, "pocStageStartBlockHeight", pocStageStartBlockHeight)
	epochState := o.phaseTracker.GetCurrentEpochState()
	// Use the parameter as storage key (trigger_height for confirmation PoC, validation start for regular PoC)
	startOfPoCBlockHeight := pocStageStartBlockHeight
	logging.Info("ValidateReceivedBatches. Current epoch state.", types.PoC,
		"pocStageStartBlockHeight", pocStageStartBlockHeight,
		"epochState.CurrentBlock.Height", epochState.CurrentBlock.Height,
		"epochState.CurrentPhase", epochState.CurrentPhase,
		"epochState.LatestEpoch.PocStartBlockHeight", epochState.LatestEpoch.PocStartBlockHeight,
		"epochState.LatestEpoch.EpochIndex", epochState.LatestEpoch.EpochIndex)

	// Determine block hash based on PoC type
	var blockHash string
	if epochState.CurrentPhase == types.InferencePhase && epochState.ActiveConfirmationPoCEvent != nil {
		// Confirmation PoC - use hash from event (hash of block generation_start_height - 1)
		blockHash = epochState.ActiveConfirmationPoCEvent.PocSeedBlockHash
		logging.Info("ValidateReceivedBatches. Using confirmation PoC block hash from event.", types.PoC,
			"pocStageStartBlockHeight", pocStageStartBlockHeight,
			"triggerHeight", epochState.ActiveConfirmationPoCEvent.TriggerHeight,
			"generationStartHeight", epochState.ActiveConfirmationPoCEvent.GenerationStartHeight,
			"blockHash", blockHash)
	} else {
		// Regular PoC - query hash at startOfPoCBlockHeight
		var err error
		blockHash, err = o.chainBridge.GetBlockHash(startOfPoCBlockHeight)
		if err != nil {
			logging.Error("ValidateReceivedBatches. Failed to get block hash", types.PoC,
				"pocStageStartBlockHeight", pocStageStartBlockHeight, "error", err)
			return
		}
		logging.Info("ValidateReceivedBatches. Got start of PoC block hash.", types.PoC,
			"pocStageStartBlockHeight", pocStageStartBlockHeight,
			"pocStartBlockHeight", startOfPoCBlockHeight,
			"blockHash", blockHash)
	}

	// 1. GET ALL SUBMITTED BATCHES!
	// FIXME: might be too long of a transaction, paging might be needed
	logging.Info("ValidateReceivedBatches. Querying batches from chain.", types.PoC,
		"pocStageStartBlockHeight", pocStageStartBlockHeight,
		"startOfPoCBlockHeight", startOfPoCBlockHeight)
	allParticipantsBatches, err := o.chainBridge.PoCBatchesForStage(startOfPoCBlockHeight)
	if err != nil {
		logging.Error("ValidateReceivedBatches. Failed to get PoC allParticipantsBatches", types.PoC, "pocStageStartBlockHeight", pocStageStartBlockHeight, "error", err)
		return
	}
	participants := make([]string, len(allParticipantsBatches.PocBatch))
	for i, participantBatches := range allParticipantsBatches.PocBatch {
		participants[i] = participantBatches.Participant
	}
	logging.Info("ValidateReceivedBatches. Got PoC allParticipantsBatches.", types.PoC,
		"pocStageStartBlockHeight", pocStageStartBlockHeight,
		"startOfPoCBlockHeight", startOfPoCBlockHeight,
		"numParticipants", len(participants),
		"participants", participants)

	nodes, err := o.nodeBroker.GetNodes()
	if err != nil {
		logging.Error("ValidateReceivedBatches. Failed to get nodes", types.PoC, "pocStageStartBlockHeight", pocStageStartBlockHeight, "error", err)
		return
	}
	logging.Info("ValidateReceivedBatches. Got nodes.", types.PoC, "pocStageStartBlockHeight", pocStageStartBlockHeight, "numNodes", len(nodes))
	nodes = filterNodes(nodes)
	logging.Info("ValidateReceivedBatches. Filtered nodes available for PoC validation.", types.PoC, "numNodes", len(nodes))

	if len(nodes) == 0 {
		logging.Error("ValidateReceivedBatches. No nodes available to validate PoC batches", types.PoC, "pocStageStartBlockHeight", pocStageStartBlockHeight)
		return
	}

	pocParams, err := o.chainBridge.GetPocParams()
	if err != nil {
		logging.Error("ValidateReceivedBatches. Failed to get chain parameters", types.PoC, "pocStageStartBlockHeight", pocStageStartBlockHeight, "error", err)
		return
	}
	samplesPerBatch := int64(pocParams.ValidationSampleSize)
	if pocParams.ValidationSampleSize == 0 {
		logging.Info("Defaulting to 200 samples per batch", types.PoC, "pocStageStartBlockHeight", pocStageStartBlockHeight)
		samplesPerBatch = POC_VALIDATE_SAMPLES_PER_BATCH
	}

	attemptCounter := 0
	successfulValidations := 0
	failedValidations := 0

	// Iterating over participants
	for _, participantBatches := range allParticipantsBatches.PocBatch {
		joinedBatch := mlnodeclient.ProofBatch{
			PublicKey:   participantBatches.HexPubKey,
			BlockHash:   blockHash,
			BlockHeight: startOfPoCBlockHeight,
		}

		uniqueNonces := make(map[int64]struct{})

		for _, b := range participantBatches.PocBatch {
			if len(b.Nonces) != len(b.Dist) {
				logging.Error("ValidateReceivedBatches. Nonces length mismatch. Skipping the batch", types.PoC,
					"participant", participantBatches.Participant,
					"batchId", b.BatchId)
				continue
			}

			for i, nonce := range b.Nonces {
				if _, exists := uniqueNonces[nonce]; !exists {
					uniqueNonces[nonce] = struct{}{}

					joinedBatch.Nonces = append(joinedBatch.Nonces, nonce)
					joinedBatch.Dist = append(joinedBatch.Dist, b.Dist[i])
				} else {
					logging.Info("ValidateReceivedBatches. Duplicate nonce found", types.PoC,
						"participant", participantBatches.Participant,
						"batchId", b.BatchId,
						"nonce", nonce)
				}
			}
		}

		batchToValidate := joinedBatch.SampleNoncesToValidate(o.pubKey, samplesPerBatch)

		validationSucceeded := false
		for attempt := range POC_VALIDATE_BATCH_RETRIES {
			node := nodes[attemptCounter%len(nodes)]
			attemptCounter++

			logging.Info("ValidateReceivedBatches. Sending sampled batch for validation.", types.PoC,
				"attempt", attempt,
				"length", len(batchToValidate.Nonces),
				"pocStageStartBlockHeight", pocStageStartBlockHeight,
				"node.Id", node.Node.Id, "node.Host", node.Node.Host,
				"participantBatches.Participant", participantBatches.Participant)
			logging.Debug("ValidateReceivedBatches. Sending batch", types.PoC, "node", node.Node.Host, "participantBatches", batchToValidate)

			// FIXME: copying: doesn't look good for large PoCBatch structures?
			nodeClient := o.nodeBroker.NewNodeClient(&node.Node)
			err = nodeClient.ValidateBatch(context.Background(), batchToValidate)
			if err != nil {
				logging.Error("ValidateReceivedBatches. Failed to send validate batch request to node", types.PoC, "pocStageStartBlockHeight", pocStageStartBlockHeight, "node", node.Node.Host, "error", err)
				continue
			}

			validationSucceeded = true
			break
		}

		if validationSucceeded {
			successfulValidations++
		} else {
			failedValidations++
			logging.Error("ValidateReceivedBatches. Failed to validate batch after all retry attempts", types.PoC,
				"pocStageStartBlockHeight", pocStageStartBlockHeight,
				"participantBatches.Participant", participantBatches.Participant,
				"maxAttempts", POC_VALIDATE_BATCH_RETRIES)
		}
	}

	logging.Info("ValidateReceivedBatches. Finished.", types.PoC,
		"pocStageStartBlockHeight", pocStageStartBlockHeight,
		"totalBatches", len(allParticipantsBatches.PocBatch),
		"successfulValidations", successfulValidations,
		"failedValidations", failedValidations)
}

func filterNodes(nodes []broker.NodeResponse) []broker.NodeResponse {
	filtered := make([]broker.NodeResponse, 0, len(nodes))
	for _, node := range nodes {
		if node.State.CurrentStatus == types.HardwareNodeStatus_POC && node.State.PocCurrentStatus == broker.PocStatusValidating {
			filtered = append(filtered, node)
		}
	}
	return filtered
}
