package poc

import (
	"context"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
	"decentralized-api/utils"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

const (
	InitValidatePath  = "/api/v1/pow/init/validate"
	ValidateBatchPath = "/api/v1/pow/validate"
	PoCBatchesPath    = "/v1/poc-batches"
)

type NodePoCOrchestrator interface {
	GetParams() *types.Params
	SetParams(params *types.Params)
	StartPoC(blockHeight int64, blockHash string, currentEpoch uint64, currentPhase chainphase.Phase)
	StopPoC()
	MoveToValidationStage(encOfPoCBlockHeight int64)
	ValidateReceivedBatches(startOfValStageHeight int64)
}

type NodePoCOrchestratorImpl struct {
	pubKey       string
	HTTPClient   *http.Client
	nodeBroker   *broker.Broker
	callbackUrl  string
	chainNodeUrl string
	cosmosClient *cosmos_client.InferenceCosmosClient
	parameters   *types.Params
	sync         sync.Mutex
}

func NewNodePoCOrchestrator(pubKey string, nodeBroker *broker.Broker, callbackUrl string, chainNodeUrl string, cosmosClient *cosmos_client.InferenceCosmosClient, parameters *types.Params) NodePoCOrchestrator {
	return &NodePoCOrchestratorImpl{
		pubKey: pubKey,
		HTTPClient: &http.Client{
			Timeout: 180 * time.Second,
		},
		nodeBroker:   nodeBroker,
		callbackUrl:  callbackUrl,
		chainNodeUrl: chainNodeUrl,
		cosmosClient: cosmosClient,
		parameters:   parameters,
	}
}

func (o *NodePoCOrchestratorImpl) GetParams() *types.Params {
	o.sync.Lock()
	defer o.sync.Unlock()
	return o.parameters
}

func (o *NodePoCOrchestratorImpl) SetParams(params *types.Params) {
	o.sync.Lock()
	defer o.sync.Unlock()
	o.parameters = params
}

func (o *NodePoCOrchestratorImpl) getPocBatchesCallbackUrl() string {
	return fmt.Sprintf("%s"+PoCBatchesPath, o.callbackUrl)
}

func (o *NodePoCOrchestratorImpl) getPocValidateCallbackUrl() string {
	// For now the URl is the same, the node inference server appends "/validated" to the URL
	//  or "/generated" (in case of init-generate)
	return fmt.Sprintf("%s"+PoCBatchesPath, o.callbackUrl)
}

var DefaultParams = Params{
	Dim:              512,
	NLayers:          64,
	NHeads:           128,
	NKVHeads:         128,
	VocabSize:        8192,
	FFNDimMultiplier: 16.0,
	MultipleOf:       1024,
	NormEps:          1e-05,
	RopeTheta:        500000.0,
	UseScaledRope:    true,
	SeqLen:           4,
}

var DevTestParams = Params{
	Dim:              512,
	NLayers:          16,
	NHeads:           16,
	NKVHeads:         16,
	VocabSize:        8192,
	FFNDimMultiplier: 1.3,
	MultipleOf:       1024,
	NormEps:          1e-05,
	RopeTheta:        500000.0,
	UseScaledRope:    true,
	SeqLen:           4,
}

var TestNetParams = Params{
	Dim:              2048,
	NLayers:          16,
	NHeads:           16,
	NKVHeads:         16,
	VocabSize:        8192,
	FFNDimMultiplier: 1.3,
	MultipleOf:       1024,
	NormEps:          1e-5,
	RopeTheta:        500000.0,
	UseScaledRope:    true,
	SeqLen:           16,
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

func (o *NodePoCOrchestratorImpl) sendInitValidateRequest(node *broker.Node, totalNodes, blockHeight int64, blockHash string) (*http.Response, error) {
	initDto := mlnodeclient.BuildInitDto(blockHeight, o.pubKey, totalNodes, int64(node.NodeNum), blockHash, o.getPocValidateCallbackUrl())
	initUrl, err := url.JoinPath(node.PoCUrl(), InitValidatePath)
	if err != nil {
		return nil, err
	}

	return utils.SendPostJsonRequest(o.HTTPClient, initUrl, initDto)
}

func (o *NodePoCOrchestratorImpl) MoveToValidationStage(encOfPoCBlockHeight int64) {
	epochParams := o.GetParams().EpochParams

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
		_, err := o.sendInitValidateRequest(n.Node, totalNodes, startOfPoCBlockHeight, blockHash)
		if err != nil {
			logging.Error("Failed to send init-generate request to node", types.PoC, "node", n.Node.Host, "error", err)
			continue
		}
		// TODO: analyze response somehow?
	}
}

func (o *NodePoCOrchestratorImpl) ValidateReceivedBatches(startOfValStageHeight int64) {
	epochParams := o.GetParams().EpochParams
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
		joinedBatch := ProofBatch{
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
		_, err := o.sendValidateBatchRequest(node.Node, joinedBatch)
		if err != nil {
			logging.Error("Failed to send validate batch request to node", types.PoC, "node", node.Node.Host, "error", err)
			continue
		}
	}
}

// FIXME: copying ;( doesn't look good for large PoCBatch structures
func (o *NodePoCOrchestratorImpl) sendValidateBatchRequest(node *broker.Node, batch ProofBatch) (*http.Response, error) {
	validateBatchUrl, err := url.JoinPath(node.PoCUrl(), ValidateBatchPath)
	if err != nil {
		return nil, err
	}

	return utils.SendPostJsonRequest(o.HTTPClient, validateBatchUrl, batch)
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
