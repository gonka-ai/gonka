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
	ValidateReceivedBatches(startOfValStageHeight int64)
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
	response, err := b.cosmosClient.NewInferenceQueryClient().PocBatchesForStage(*b.cosmosClient.GetContext(), &types.QueryPocBatchesForStageRequest{BlockHeight: startPoCBlockHeight})
	if err != nil {
		logging.Error("Failed to query PoC batches for stage", types.PoC, "error", err)
		return nil, err
	}
	return response, nil
}

func (b *OrchestratorChainBridgeImpl) GetPocParams() (*types.PocParams, error) {
	response, err := b.cosmosClient.NewInferenceQueryClient().Params(*b.cosmosClient.GetContext(), &types.QueryParamsRequest{})
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

func (o *NodePoCOrchestratorImpl) ValidateReceivedBatches(startOfValStageHeight int64) {
	logging.Info("ValidateReceivedBatches. Starting.", types.PoC, "startOfValStageHeight", startOfValStageHeight)
	epochState := o.phaseTracker.GetCurrentEpochState()
	startOfPoCBlockHeight := epochState.LatestEpoch.PocStartBlockHeight
	// TODO: maybe check if startOfPoCBlockHeight is consistent with current block height or smth?
	logging.Info("ValidateReceivedBatches. Current epoch state.", types.PoC,
		"startOfValStageHeight", startOfValStageHeight,
		"epochState.CurrentBlock.Height", epochState.CurrentBlock.Height,
		"epochState.CurrentPhase", epochState.CurrentPhase,
		"epochState.LatestEpoch.PocStartBlockHeight", epochState.LatestEpoch.PocStartBlockHeight,
		"epochState.LatestEpoch.EpochIndex", epochState.LatestEpoch.EpochIndex)

	blockHash, err := o.chainBridge.GetBlockHash(startOfPoCBlockHeight)
	if err != nil {
		logging.Error("ValidateReceivedBatches. Failed to get block hash", types.PoC, "startOfValStageHeight", startOfValStageHeight, "error", err)
		return
	}
	logging.Info("ValidateReceivedBatches. Got start of PoC block hash.", types.PoC,
		"startOfValStageHeight", startOfValStageHeight, "pocStartBlockHeight", startOfPoCBlockHeight, "blockHash", blockHash)

	// 1. GET ALL SUBMITTED BATCHES!
	// batches, err := o.cosmosClient.GetPoCBatchesByStage(startOfPoCBlockHeight)
	// FIXME: might be too long of a transaction, paging might be needed
	batches, err := o.chainBridge.PoCBatchesForStage(startOfPoCBlockHeight)
	if err != nil {
		logging.Error("ValidateReceivedBatches. Failed to get PoC batches", types.PoC, "startOfValStageHeight", startOfValStageHeight, "error", err)
		return
	}
	participants := make([]string, len(batches.PocBatch))
	for i, batch := range batches.PocBatch {
		participants[i] = batch.Participant
	}
	logging.Info("ValidateReceivedBatches. Got PoC batches.", types.PoC,
		"startOfValStageHeight", startOfValStageHeight,
		"numParticipants", len(batches.PocBatch),
		"participants", participants)

	nodes, err := o.nodeBroker.GetNodes()
	if err != nil {
		logging.Error("ValidateReceivedBatches. Failed to get nodes", types.PoC, "startOfValStageHeight", startOfValStageHeight, "error", err)
		return
	}
	logging.Info("ValidateReceivedBatches. Got nodes.", types.PoC, "startOfValStageHeight", startOfValStageHeight, "numNodes", len(nodes))
	nodes = filterNodes(nodes)
	logging.Info("ValidateReceivedBatches. Filtered nodes available for PoC validation.", types.PoC, "numNodes", len(nodes))

	if len(nodes) == 0 {
		logging.Error("ValidateReceivedBatches. No nodes available to validate PoC batches", types.PoC, "startOfValStageHeight", startOfValStageHeight)
		return
	}

	pocParams, err := o.chainBridge.GetPocParams()
	if err != nil {
		logging.Error("ValidateReceivedBatches. Failed to get chain parameters", types.PoC, "startOfValStageHeight", startOfValStageHeight, "error", err)
		return
	}
	samplesPerBatch := int64(pocParams.ValidationSampleSize)
	if pocParams.ValidationSampleSize == 0 {
		logging.Info("Defaulting to 200 samples per batch", types.PoC, "startOfValStageHeight", startOfValStageHeight)
		samplesPerBatch = POC_VALIDATE_SAMPLES_PER_BATCH
	}

	attemptCounter := 0
	successfulValidations := 0
	failedValidations := 0

	for _, batch := range batches.PocBatch {
		joinedBatch := mlnodeclient.ProofBatch{
			PublicKey:   batch.HexPubKey,
			BlockHash:   blockHash,
			BlockHeight: startOfPoCBlockHeight,
		}

		for _, b := range batch.PocBatch {
			joinedBatch.Dist = append(joinedBatch.Dist, b.Dist...)
			joinedBatch.Nonces = append(joinedBatch.Nonces, b.Nonces...)
		}

		batchToValidate := joinedBatch.SampleNoncesToValidate(o.pubKey, samplesPerBatch)

		validationSucceeded := false
		for attempt := range POC_VALIDATE_BATCH_RETRIES {
			node := nodes[attemptCounter%len(nodes)]
			attemptCounter++

			logging.Info("ValidateReceivedBatches. Sending sampled batch for validation.", types.PoC,
				"attempt", attempt,
				"length", len(batchToValidate.Nonces),
				"startOfValStageHeight", startOfValStageHeight,
				"node.Id", node.Node.Id, "node.Host", node.Node.Host,
				"batch.Participant", batch.Participant)
			logging.Debug("ValidateReceivedBatches. Sending batch", types.PoC, "node", node.Node.Host, "batch", batchToValidate)

			// FIXME: copying: doesn't look good for large PoCBatch structures?
			nodeClient := o.nodeBroker.NewNodeClient(&node.Node)
			err = nodeClient.ValidateBatch(context.Background(), batchToValidate)
			if err != nil {
				logging.Error("ValidateReceivedBatches. Failed to send validate batch request to node", types.PoC, "startOfValStageHeight", startOfValStageHeight, "node", node.Node.Host, "error", err)
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
				"startOfValStageHeight", startOfValStageHeight,
				"batch.Participant", batch.Participant,
				"maxAttempts", POC_VALIDATE_BATCH_RETRIES)
		}
	}

	logging.Info("ValidateReceivedBatches. Finished.", types.PoC,
		"startOfValStageHeight", startOfValStageHeight,
		"totalBatches", len(batches.PocBatch),
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
