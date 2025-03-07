package poc

import (
	"bytes"
	"context"
	"decentralized-api/broker"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"encoding/json"
	"fmt"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	StopAllPath       = "/api/v1/stop"
	InitGeneratePath  = "/api/v1/pow/init/generate"
	InitValidatePath  = "/api/v1/pow/init/validate"
	ValidateBatchPath = "/api/v1/pow/validate"
	PoCStopPath       = "/api/v1/pow/stop"
	InferenceUpPath   = "/api/v1/inference/up"
	InferenceDownPath = "/api/v1/inference/down"
	PoCBatchesPath    = "/v1/poc-batches"

	DefaultRTarget        = 1.390051443
	DefaultBatchSize      = 8000
	DefaultFraudThreshold = 0.01
)

type NodePoCOrchestrator struct {
	pubKey       string
	HTTPClient   *http.Client
	nodeBroker   *broker.Broker
	callbackUrl  string
	chainNodeUrl string
	cosmosClient *cosmos_client.InferenceCosmosClient
	noOp         bool
	parameters   *types.Params
	sync         sync.Mutex
}

func NewNodePoCOrchestrator(pubKey string, nodeBroker *broker.Broker, callbackUrl string, chainNodeUrl string, cosmosClient *cosmos_client.InferenceCosmosClient, parameters *types.Params) *NodePoCOrchestrator {
	return &NodePoCOrchestrator{
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

func (o *NodePoCOrchestrator) GetParams() *types.Params {
	o.sync.Lock()
	defer o.sync.Unlock()
	return o.parameters
}

func (o *NodePoCOrchestrator) SetParams(params *types.Params) {
	o.sync.Lock()
	defer o.sync.Unlock()
	o.parameters = params
}

func (o *NodePoCOrchestrator) getPocBatchesCallbackUrl() string {
	return fmt.Sprintf("%s"+PoCBatchesPath, o.callbackUrl)
}

func (o *NodePoCOrchestrator) getPocValidateCallbackUrl() string {
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

func (o *NodePoCOrchestrator) StartPoC(blockHeight int64, blockHash string) {
	if o.noOp {
		logging.Info("NodePoCOrchestrator.Start. NoOp is set. Skipping start.", types.PoC)
		return
	}

	logging.Info("Starting PoC on nodes", types.PoC, "blockHeight", blockHeight, "blockHash", blockHash)
	nodes, err := o.nodeBroker.GetNodes()
	if err != nil {
		logging.Error("NodePoCOrchestrator.Start. Failed to get nodes", types.PoC, "error", err)
		return
	}

	totalNodes := len(nodes)
	for _, n := range nodes {
		_, err := o.sendStopAllRequest(n.Node)
		if err != nil {
			logging.Error("Failed to send init-generate request to node", types.PoC, "node", n.Node.Host, "error", err)
			continue
		}

		// PRTODO: analyze response somehow?
		_, err = o.sendInitGenerateRequest(n.Node, int64(totalNodes), blockHeight, blockHash)
		if err != nil {
			logging.Error("Failed to send init-generate request to node", types.Nodes, n.Node.Host, "error", err)
			continue
		}
	}
}

func (o *NodePoCOrchestrator) sendInitGenerateRequest(node *broker.Node, totalNodes, blockHeight int64, blockHash string) (*http.Response, error) {
	initDto := o.buildInitDto(blockHeight, totalNodes, int64(node.NodeNum), blockHash, o.getPocBatchesCallbackUrl())

	initUrl, err := url.JoinPath(node.PoCUrl(), InitGeneratePath)
	if err != nil {
		return nil, err
	}

	logging.Info("Sending init-generate request to node.", types.PoC, "url", initUrl, "initDto", initDto)

	return sendPostRequest(o.HTTPClient, initUrl, initDto)
}

func (o *NodePoCOrchestrator) buildInitDto(blockHeight, totalNodes, nodeNum int64, blockHash, callbackUrl string) InitDto {
	return InitDto{
		BlockHeight:    blockHeight,
		BlockHash:      blockHash,
		PublicKey:      o.pubKey,
		BatchSize:      DefaultBatchSize,
		RTarget:        DefaultRTarget,
		FraudThreshold: DefaultFraudThreshold,
		Params:         &DevTestParams,
		URL:            callbackUrl,
		TotalNodes:     totalNodes,
		NodeNum:        nodeNum,
	}
}

func (o *NodePoCOrchestrator) StopPoC() {
	if o.noOp {
		logging.Info("NodePoCOrchestrator.Stop. NoOp is set. Skipping stop.", types.PoC)
		return
	}

	nodes, err := o.nodeBroker.GetNodes()
	if err != nil {
		// PRTODO: log error
		return
	}

	for _, n := range nodes {
		_, err := o.sendStopRequest(n.Node)
		if err != nil {
			logging.Error("Failed to send stop request to node", types.PoC, "node", n.Node.Host, "error", err)
			continue
		}

		_, err = o.sendInferenceUpRequest(n.Node)
		if err != nil {
			logging.Error("Failed to send inference/up request to node", types.PoC, "node", n.Node.Host, "error", err)
			continue
		}
	}
}

func (o *NodePoCOrchestrator) sendStopRequest(node *broker.Node) (*http.Response, error) {
	stopUrl, err := url.JoinPath(node.PoCUrl(), PoCStopPath)
	if err != nil {
		return nil, err
	}

	logging.Info("Sending stop request to node", types.PoC, "stopUrl", stopUrl)

	return sendPostRequest(o.HTTPClient, stopUrl, nil)
}

func (o *NodePoCOrchestrator) sendStopAllRequest(node *broker.Node) (*http.Response, error) {
	stopUrl, err := url.JoinPath(node.PoCUrl(), StopAllPath)
	if err != nil {
		return nil, err
	}

	logging.Info("Sending stop all request to node", types.Nodes, stopUrl)
	return sendPostRequest(o.HTTPClient, stopUrl, nil)
}

func (o *NodePoCOrchestrator) sendInferenceUpRequest(node *broker.Node) (*http.Response, error) {
	inferenceUpUrl, err := url.JoinPath(node.PoCUrl(), InferenceUpPath)
	if err != nil {
		return nil, err
	}

	model := node.Models[0]
	inferenceUpDto := InferenceUpDto{
		Model: model,
		Dtype: "float16",
		Args:  []string{"--enforce-eager"},
	}

	logging.Info("Sending inference/up request to node", types.PoC, "inferenceUpUrl", inferenceUpUrl, "inferenceUpDto", inferenceUpDto)

	return sendPostRequest(o.HTTPClient, inferenceUpUrl, inferenceUpDto)
}

func (o *NodePoCOrchestrator) sendInferenceDownRequest(node *broker.Node) (*http.Response, error) {
	inferenceDownUrl, err := url.JoinPath(node.PoCUrl(), InferenceDownPath)
	if err != nil {
		return nil, err
	}

	logging.Info("Sending inference/down request to node", types.Nodes, inferenceDownUrl)
	return sendPostRequest(o.HTTPClient, inferenceDownUrl, nil)
}

func (o *NodePoCOrchestrator) sendInitValidateRequest(node *broker.Node, totalNodes, blockHeight int64, blockHash string) (*http.Response, error) {
	initDto := o.buildInitDto(blockHeight, totalNodes, int64(node.NodeNum), blockHash, o.getPocValidateCallbackUrl())
	initUrl, err := url.JoinPath(node.PoCUrl(), InitValidatePath)
	if err != nil {
		return nil, err
	}

	return sendPostRequest(o.HTTPClient, initUrl, initDto)
}

func sendPostRequest(client *http.Client, url string, payload any) (*http.Response, error) {
	var req *http.Request
	var err error

	if payload == nil {
		// Create a POST request with no body if payload is nil.
		req, err = http.NewRequest(http.MethodPost, url, nil)
	} else {
		// Marshal the payload to JSON.
		jsonData, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		req, err = http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonData))
	}

	if err != nil {
		return nil, err
	}

	return client.Do(req)
}

func (o *NodePoCOrchestrator) MoveToValidationStage(encOfPoCBlockHeight int64) {
	if o.noOp {
		logging.Info("NodePoCOrchestrator.MoveToValidationStage. NoOp is set. Skipping move to validation stage.", types.PoC)
		return
	}
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
		// PRTODO: log error
		return
	}

	totalNodes := int64(len(nodes))
	for _, n := range nodes {
		_, err := o.sendInitValidateRequest(n.Node, totalNodes, startOfPoCBlockHeight, blockHash)
		if err != nil {
			logging.Error("Failed to send init-generate request to node", types.PoC, "node", n.Node.Host, "error", err)
			continue
		}
	}
}

func (o *NodePoCOrchestrator) ValidateReceivedBatches(startOfValStageHeight int64) {
	if o.noOp {
		logging.Info("NodePoCOrchestrator.ValidateReceivedBatches. NoOp is set. Skipping validation.", types.PoC)
		return
	}

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
func (o *NodePoCOrchestrator) sendValidateBatchRequest(node *broker.Node, batch ProofBatch) (*http.Response, error) {
	validateBatchUrl, err := url.JoinPath(node.PoCUrl(), ValidateBatchPath)
	if err != nil {
		return nil, err
	}
	return sendPostRequest(o.HTTPClient, validateBatchUrl, batch)
}

func (o *NodePoCOrchestrator) getBlockHash(height int64) (string, error) {
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
