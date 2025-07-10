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
	if epochState == nil {
		logging.Error("ValidateReceivedBatches. Current epoch state is nil", types.PoC,
			"startOfValStageHeight", startOfValStageHeight)
		return
	}

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

	if len(nodes) == 0 {
		logging.Error("ValidateReceivedBatches. No nodes available to validate PoC batches", types.PoC, "startOfValStageHeight", startOfValStageHeight)
		return
	}

	for i, batch := range batches.PocBatch {
		joinedBatch := mlnodeclient.ProofBatch{
			PublicKey:   batch.HexPubKey,
			BlockHash:   blockHash,
			BlockHeight: startOfPoCBlockHeight,
		}

		for _, b := range batch.PocBatch {
			joinedBatch.Dist = append(joinedBatch.Dist, b.Dist...)
			joinedBatch.Nonces = append(joinedBatch.Nonces, b.Nonces...)
		}
		node := nodes[i%len(nodes)]

		logging.Info("ValidateReceivedBatches. Sending joined batch for validation.", types.PoC,
			"startOfValStageHeight", startOfValStageHeight,
			"node.Id", node.Node.Id, "node.Host", node.Node.Host,
			"batch.Participant", batch.Participant)
		logging.Debug("ValidateReceivedBatches. sending batch", types.PoC, "node", node.Node.Host, "batch", joinedBatch)

		// FIXME: copying: doesn't look good for large PoCBatch structures?
		nodeClient := o.nodeBroker.NewNodeClient(&node.Node)
		err = nodeClient.ValidateBatch(context.Background(), joinedBatch)
		if err != nil {
			logging.Error("ValidateReceivedBatches. Failed to send validate batch request to node", types.PoC, "startOfValStageHeight", startOfValStageHeight, "node", node.Node.Host, "error", err)
			continue
		}
	}

	logging.Info("ValidateReceivedBatches. Finished.", types.PoC, "startOfValStageHeight", startOfValStageHeight)
}
