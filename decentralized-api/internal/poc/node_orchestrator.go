package poc

import (
	"context"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
	"fmt"
	"github.com/productscience/inference/x/inference/types"
)

const (
	PoCBatchesPath = "/v1/poc-batches"
)

type NodePoCOrchestrator interface {
	StartPoC(blockHeight int64, blockHash string, currentEpoch uint64, currentPhase chainphase.Phase)
	StopPoC()
	MoveToValidationStage(encOfPoCBlockHeight int64)
	ValidateReceivedBatches(startOfValStageHeight int64)
}

type NodePoCOrchestratorImpl struct {
	pubKey       string
	nodeBroker   *broker.Broker
	callbackUrl  string
	chainNodeUrl string
	cosmosClient *cosmos_client.InferenceCosmosClient
	phaseTracker *chainphase.ChainPhaseTracker
}

func NewNodePoCOrchestrator(pubKey string, nodeBroker *broker.Broker, callbackUrl string, chainNodeUrl string, cosmosClient *cosmos_client.InferenceCosmosClient, phaseTracker *chainphase.ChainPhaseTracker) NodePoCOrchestrator {
	return &NodePoCOrchestratorImpl{
		pubKey:       pubKey,
		nodeBroker:   nodeBroker,
		callbackUrl:  callbackUrl,
		chainNodeUrl: chainNodeUrl,
		cosmosClient: cosmosClient,
		phaseTracker: phaseTracker,
	}
}

func (o *NodePoCOrchestratorImpl) getPocBatchesCallbackUrl() string {
	return fmt.Sprintf("%s"+PoCBatchesPath, o.callbackUrl)
}

func (o *NodePoCOrchestratorImpl) getPocValidateCallbackUrl() string {
	// For now the URl is the same, the node inference server appends "/validated" to the URL
	//  or "/generated" (in case of init-generate)
	return fmt.Sprintf("%s"+PoCBatchesPath, o.callbackUrl)
}

func (o *NodePoCOrchestratorImpl) StartPoC(blockHeight int64, blockHash string, currentEpoch uint64, currentPhase chainphase.Phase) {
	command := broker.StartPocCommand{
		BlockHeight:  blockHeight,
		BlockHash:    blockHash,
		PubKey:       o.pubKey,
		CallbackUrl:  o.getPocBatchesCallbackUrl(),
		CurrentEpoch: currentEpoch,
		CurrentPhase: currentPhase,
		Response:     make(chan bool, 2),
	}
	err := o.nodeBroker.QueueMessage(command)
	if err != nil {
		logging.Error("Failed to send start PoC command", types.PoC, "error", err)
		return
	}

	success := <-command.Response
	logging.Info("NodePoCOrchestrator.Start. Start PoC command response", types.PoC, "success", success)
}

func (o *NodePoCOrchestratorImpl) StopPoC() {
	command := broker.NewInferenceUpAllCommand()
	err := o.nodeBroker.QueueMessage(command)
	if err != nil {
		logging.Error("Failed to send inference up command", types.PoC, "error", err)
		return
	}

	success := <-command.Response
	logging.Info("NodePoCOrchestrator.Stop. Inference up command response", types.PoC, "success", success)
}

func (o *NodePoCOrchestratorImpl) MoveToValidationStage(encOfPoCBlockHeight int64) {
	epochParams := o.phaseTracker.GetEpochParams()

	startOfPoCBlockHeight := epochParams.GetStartBlockHeightFromEndOfPocStage(encOfPoCBlockHeight)
	blockHash, err := o.getBlockHash(startOfPoCBlockHeight)
	if err != nil {
		logging.Error("MoveToValidationStage. Failed to get block hash", types.PoC, "error", err)
		return
	}

	logging.Info("Moving to PoC Validation Stage", types.PoC, "startOfPoCBlockHeight", startOfPoCBlockHeight, "blockHash", blockHash)

	logging.Info("Starting PoC Validation on nodes", types.PoC)
	nodes, err := o.nodeBroker.GetNodes()
	if err != nil {
		logging.Error("Failed to get nodes", types.PoC, "error", err)
		return
	}

	totalNodes := int64(len(nodes))
	for _, n := range nodes {
		initDto := mlnodeclient.BuildInitDto(startOfPoCBlockHeight, o.pubKey, totalNodes, totalNodes, blockHash, o.getPocValidateCallbackUrl())
		nodeClient := o.nodeBroker.NewNodeClient(n.Node)
		err = nodeClient.InitValidate(initDto)

		if err != nil {
			logging.Error("Failed to send init-generate request to node", types.PoC, "node", n.Node.Host, "error", err)
			continue
		}
	}
}

func (o *NodePoCOrchestratorImpl) ValidateReceivedBatches(startOfValStageHeight int64) {
	epochParams := o.phaseTracker.GetEpochParams()
	startOfPoCBlockHeight := epochParams.GetStartBlockHeightFromStartOfPocValidationStage(startOfValStageHeight)
	blockHash, err := o.getBlockHash(startOfPoCBlockHeight)
	if err != nil {
		logging.Error("ValidateReceivedBatches. Failed to get block hash", types.PoC, "error", err)
		return
	}

	// 1. GET ALL SUBMITTED BATCHES!
	// batches, err := o.cosmosClient.GetPoCBatchesByStage(startOfPoCBlockHeight)
	// FIXME: might be too long of a transaction, paging might be needed
	queryClient := o.cosmosClient.NewInferenceQueryClient()
	batches, err := queryClient.PocBatchesForStage(o.cosmosClient.Context, &types.QueryPocBatchesForStageRequest{BlockHeight: startOfPoCBlockHeight})
	if err != nil {
		logging.Error("Failed to get PoC batches", types.PoC, "error", err)
		return
	}

	nodes, err := o.nodeBroker.GetNodes()
	if err != nil {
		logging.Error("Failed to get nodes", types.PoC, "error", err)
		return
	}

	if len(nodes) == 0 {
		logging.Error("No nodes available to validate PoC batches", types.PoC)
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

		logging.Debug("ValidateReceivedBatches. pubKey", types.PoC, "pubKey", batch.HexPubKey)
		logging.Debug("ValidateReceivedBatches. sending batch", types.PoC, "node", node.Node.Host, "batch", joinedBatch)

		// FIXME: copying: doesn't look good for large PoCBatch structures?
		nodeClient := o.nodeBroker.NewNodeClient(node.Node)
		err = nodeClient.ValidateBatch(joinedBatch)
		if err != nil {
			logging.Error("Failed to send validate batch request to node", types.PoC, "node", node.Node.Host, "error", err)
			continue
		}
	}
}

func (o *NodePoCOrchestratorImpl) getBlockHash(height int64) (string, error) {
	client, err := cosmos_client.NewRpcClient(o.chainNodeUrl)
	if err != nil {
		return "", err
	}

	block, err := client.Block(context.Background(), &height)
	if err != nil {
		return "", err
	}

	return block.Block.Hash().String(), err
}
